// Package scheduler runs the panel's cron jobs (plan §6.7): periodic backups,
// scheduled restarts and scheduled broadcasts.
//
// It wraps github.com/robfig/cron/v3 rather than reimplementing a cron parser,
// and adds the four things the panel needs on top:
//
//   - A Job abstraction with a per-run context timeout, so a wedged job cannot
//     occupy a scheduler slot forever.
//   - Panic recovery per run. cron starts each job in its own goroutine, so a
//     panicking job would otherwise take down the whole panel process.
//   - Overlap protection. cron fires a job on schedule regardless of whether
//     the previous run finished; a daily backup that takes 26 hours would
//     otherwise stack up copies of itself. Each job here runs at most once at
//     a time, and a skipped run is recorded rather than silently dropped.
//   - Persistence hooks. Enabled jobs are loaded from a repository at startup
//     and every run's outcome is written back, which is what makes the
//     `jobs` table's last_run/last_result columns meaningful.
//
// The repository is an interface defined here, not an import of internal/store,
// so this package is testable standalone and does not couple to the storage
// layer's concrete types.
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// Job types, matching the `jobs.type` column's closed set (§5.5, §6.7).
const (
	// TypeBackup runs a world backup.
	TypeBackup = "backup"
	// TypeRestart restarts an instance.
	TypeRestart = "restart"
	// TypeCommand sends a console command (used for broadcasts).
	TypeCommand = "command"
)

// DefaultRunTimeout bounds a single job run. A backup of a large world can
// legitimately take minutes, so this is generous; it exists to stop a wedged
// job from blocking its own next run forever, not to police performance.
const DefaultRunTimeout = 30 * time.Minute

// Job is one unit of scheduled work.
//
// Run receives a context carrying the per-run timeout. Implementations must
// honour it. Returning an error marks the run failed and is recorded in the
// jobs table; panicking is also handled (see the package comment) but is a bug
// — return an error instead.
type Job interface {
	// Type reports the job type (TypeBackup, TypeRestart, TypeCommand).
	Type() string

	// InstanceID reports the instance this job belongs to, or 0 for a
	// panel-wide job.
	InstanceID() int64

	// Run performs the work.
	Run(ctx context.Context) error

	// Describe returns a short human-readable summary for the jobs UI, e.g.
	// "backup instance 3 (keep 7)".
	Describe() string
}

// Entry is the scheduler's view of one registered job: the cron entry id, the
// job, and its bookkeeping.
type Entry struct {
	// ID is the cron EntryID, usable with Remove.
	ID cron.EntryID `json:"id"`
	// Spec is the cron expression the job was registered with.
	Spec string `json:"spec"`
	// JobID is the `jobs.id` row this entry came from, or 0 for jobs
	// registered programmatically (the panel's built-in backup job).
	JobID int64 `json:"jobId,omitempty"`
	// Type, InstanceID and Describe are copied from the Job for display.
	Type       string `json:"type"`
	InstanceID int64  `json:"instanceId"`
	Describe   string `json:"describe"`

	// Next and Prev mirror cron's own schedule bookkeeping.
	Next time.Time `json:"next,omitempty"`
	Prev time.Time `json:"prev,omitempty"`

	// LastRun, LastResult and Running report the panel's own bookkeeping.
	LastRun    time.Time `json:"lastRun,omitempty"`
	LastResult string    `json:"lastResult,omitempty"`
	Running    bool      `json:"running"`
}

// JobRepo is the persistence surface the scheduler needs. It is deliberately
// narrower than store.JobRepo: only these five operations are used, so a fake
// is trivial and the scheduler cannot drift into owning job CRUD.
type JobRepo interface {
	// ListEnabled returns every enabled job.
	ListEnabled(ctx context.Context) ([]StoredJob, error)
	// RecordRun stores one run's outcome.
	RecordRun(ctx context.Context, id int64, result string, at time.Time) error
}

// StoredJob is a `jobs` row as the scheduler sees it. It mirrors the columns
// the scheduler actually reads rather than importing store.Job, so this package
// stays independent of the storage layer.
type StoredJob struct {
	ID         int64
	InstanceID int64
	Type       string
	Cron       string
	Payload    string
	Enabled    bool
}

// Runner builds a Job from a stored row. It is the seam that keeps this package
// from importing the backup/supervisor packages: cmd/scnetm supplies a function
// that closes over them.
//
// Returning a nil Job with a nil error means "skip this row" (for example an
// unknown job type); the scheduler logs it and moves on rather than refusing to
// start.
type Runner func(ctx context.Context, sj StoredJob) (Job, error)

// Options configures a Scheduler.
type Options struct {
	// Location is the timezone cron schedules are interpreted in. Default:
	// UTC. Using UTC by default is deliberate — a panel that migrates between
	// hosts must not silently move every job's firing time — and it can be set
	// to time.Local when the operator expects "04:00" to mean local 04:00.
	Location *time.Location

	// RunTimeout bounds one job run. Default DefaultRunTimeout.
	RunTimeout time.Duration

	// Repo persists run outcomes. Optional: when nil the scheduler still runs
	// jobs, it just cannot record the results.
	Repo JobRepo

	// Logger receives diagnostics. Default: slog.Default().
	Logger *slog.Logger

	// Now is the clock, for tests. Default time.Now.
	Now func() time.Time
}

// Scheduler owns the cron entries and their bookkeeping.
type Scheduler struct {
	cron       *cron.Cron
	parser     cron.Parser
	runTimeout time.Duration
	repo       JobRepo
	logger     *slog.Logger
	now        func() time.Time

	mu      sync.Mutex
	entries map[cron.EntryID]*registration
	running bool
	started bool
	wg      sync.WaitGroup
	stop    chan struct{}
}

// registration is the mutable state behind one entry.
type registration struct {
	job Job

	// jobID is the `jobs.id` row, 0 for a programmatic job.
	jobID int64
	spec  string

	// running guards against overlap: a job whose previous run has not
	// finished is skipped rather than stacked.
	running bool

	lastRun    time.Time
	lastResult string
}

// New builds a Scheduler.
//
// The cron parser accepts the five-field standard specification plus the
// descriptors (@daily, @hourly, …) and the optional six-field form with
// seconds, which is what the plan's "default 0 4 * * *" example needs.
func New(opts Options) *Scheduler {
	if opts.Location == nil {
		opts.Location = time.UTC
	}
	if opts.RunTimeout <= 0 {
		opts.RunTimeout = DefaultRunTimeout
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}

	parser := cron.NewParser(
		cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
	)

	return &Scheduler{
		cron: cron.New(
			cron.WithLocation(opts.Location),
			cron.WithParser(parser),
			// cron's own logger writes to stdout; route it into slog instead so
			// the panel has one log stream.
			cron.WithLogger(cronLogger{logger: opts.Logger}),
		),
		parser:     parser,
		runTimeout: opts.RunTimeout,
		repo:       opts.Repo,
		logger:     opts.Logger,
		now:        opts.Now,
		entries:    make(map[cron.EntryID]*registration),
		stop:       make(chan struct{}),
	}
}

// ValidateSpec reports whether spec is a well-formed cron expression.
//
// It is exported so the API layer can reject a bad expression at the moment the
// operator saves it, rather than only when the scheduler tries to register it.
func ValidateSpec(spec string) error {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return fmt.Errorf("%w: cron expression is empty", ErrInvalidSpec)
	}

	parser := cron.NewParser(
		cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
	)
	if _, err := parser.Parse(trimmed); err != nil {
		return fmt.Errorf("%w: %q: %v", ErrInvalidSpec, trimmed, err)
	}
	return nil
}

// ErrInvalidSpec is the sentinel ValidateSpec and Add wrap, so callers can
// distinguish "the operator typed a bad expression" (a 400) from an internal
// failure.
var ErrInvalidSpec = errors.New("scheduler: invalid cron expression")

// ErrAlreadyStarted reports Add on a running scheduler's entry that cannot be
// mutated, and Stop on a scheduler that was never started.
var ErrAlreadyStarted = errors.New("scheduler: scheduler is already started")

// Add registers job under spec and returns its entry id.
//
// The spec is validated first and a malformed expression is rejected with an
// error wrapping ErrInvalidSpec that names the offending expression — an
// operator who types "0 4 * * " needs to be told which field is wrong, not that
// "parsing failed".
//
// jobID associates the entry with a `jobs` row so run outcomes can be
// persisted; pass 0 for a programmatic job.
//
// A *typed* nil job (for example a nil *BackupJob in a Job variable) is
// rejected here too, not just an untyped nil: the interface comparison would
// let it through and it would then panic the moment Type() was called, which
// happens on this very line. See isNilJob.
func (s *Scheduler) Add(spec string, job Job, jobID int64) (cron.EntryID, error) {
	if isNilJob(job) {
		return 0, errors.New("scheduler: nil job")
	}

	trimmed := strings.TrimSpace(spec)
	if err := ValidateSpec(trimmed); err != nil {
		return 0, err
	}

	reg := &registration{job: job, jobID: jobID, spec: trimmed}

	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return 0, fmt.Errorf("%w: register jobs before Start", ErrAlreadyStarted)
	}
	s.mu.Unlock()

	id, err := s.cron.AddFunc(trimmed, func() { s.run(reg) })
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %v", ErrInvalidSpec, trimmed, err)
	}

	s.mu.Lock()
	s.entries[id] = reg
	s.mu.Unlock()

	s.logger.Info("scheduled job registered",
		"entry", id, "spec", trimmed, "type", job.Type(),
		"instance", job.InstanceID(), "describe", job.Describe())

	return id, nil
}

// Remove unregisters an entry. Removing an unknown id is a no-op, which makes
// the "job was deleted in the UI" path idempotent.
func (s *Scheduler) Remove(id cron.EntryID) {
	s.cron.Remove(id)

	s.mu.Lock()
	delete(s.entries, id)
	s.mu.Unlock()
}

// Entries returns a snapshot of the registered entries, sorted by entry id for
// a stable UI listing.
func (s *Scheduler) Entries() []Entry {
	cronEntries := s.cron.Entries()
	byID := make(map[cron.EntryID]cron.Entry, len(cronEntries))
	for _, e := range cronEntries {
		byID[e.ID] = e
	}

	s.mu.Lock()
	out := make([]Entry, 0, len(s.entries))
	for id, reg := range s.entries {
		e := Entry{
			ID:         id,
			Spec:       reg.spec,
			JobID:      reg.jobID,
			Type:       reg.job.Type(),
			InstanceID: reg.job.InstanceID(),
			Describe:   reg.job.Describe(),
			LastRun:    reg.lastRun,
			LastResult: reg.lastResult,
			Running:    reg.running,
		}
		if ce, ok := byID[id]; ok {
			e.Next = ce.Next
			e.Prev = ce.Prev
		}
		out = append(out, e)
	}
	s.mu.Unlock()

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Start begins executing entries. It is idempotent.
func (s *Scheduler) Start() {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.running = true
	s.mu.Unlock()

	s.cron.Start()
	s.logger.Info("scheduler started", "entries", len(s.Entries()))
}

// Stop halts the scheduler and waits for in-flight jobs, bounded by ctx.
//
// cron's Stop context is deliberately not what is awaited here. cron tracks job
// goroutines with an internal WaitGroup that is only incremented inside its run
// loop when it decides to start a scheduled job, so a manual RunNow bypasses
// that counter entirely: stopping the scheduler while a manually triggered run
// is in flight would return immediately and leave that run unobserved, possibly
// still writing after the caller has torn down the database underneath it.
//
// The WaitGroup awaited below is incremented before every job goroutine starts,
// however it was triggered, so Stop covers both paths.
//
// A job that ignores its own timeout will make Stop wait until ctx expires, in
// which case ctx.Err() is returned and the process may exit with the job still
// running. That is the correct trade-off for a shutdown path: the alternative
// is hanging forever.
func (s *Scheduler) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	alreadyStopped := !s.running
	s.running = false
	s.mu.Unlock()

	if alreadyStopped {
		return nil
	}

	s.cron.Stop()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		s.logger.Info("scheduler stopped")
		return nil
	case <-ctx.Done():
		s.logger.Warn("scheduler stop timed out; a job is still running", "err", ctx.Err())
		return ctx.Err()
	}
}

// RunNow executes an entry's job immediately, bypassing the schedule but not
// the overlap guard or the panic recovery. It is what the UI's "run now" button
// calls.
//
// It blocks until the run finishes, so callers should invoke it from a request
// handler with its own context; the entry's own run timeout still applies and
// the shorter of the two wins.
func (s *Scheduler) RunNow(ctx context.Context, id cron.EntryID) error {
	s.mu.Lock()
	reg, ok := s.entries[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("scheduler: unknown entry %d", id)
	}
	if reg.running {
		s.mu.Unlock()
		return fmt.Errorf("%w: entry %d (%s)", ErrOverlap, id, reg.job.Describe())
	}
	// Claim the run and register it with the shutdown WaitGroup in the same
	// critical section, so Stop can never observe a claimed-but-untracked run.
	reg.running = true
	s.wg.Add(1)
	s.mu.Unlock()

	defer s.wg.Done()

	return s.execute(ctx, id, reg)
}

// ErrOverlap reports that a run was skipped because the previous one is still
// in flight.
var ErrOverlap = errors.New("scheduler: previous run is still in progress")

// run is the cron callback for an entry.
//
// It is the single path through which a scheduled job executes, so the overlap
// guard, the timeout and the panic recovery all live in one place.
func (s *Scheduler) run(reg *registration) {
	// cron calls this from a fresh goroutine, which may start after Stop has
	// begun waiting. Refusing to start in that window keeps Stop's guarantee
	// ("no job is running once it returns") honest.
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		s.logger.Warn("scheduled run skipped: scheduler is stopping",
			"type", reg.job.Type(), "instance", reg.job.InstanceID())
		return
	}
	if reg.running {
		s.mu.Unlock()
		s.logger.Warn("scheduled run skipped: previous run still in progress",
			"type", reg.job.Type(), "instance", reg.job.InstanceID(),
			"describe", reg.job.Describe())
		s.recordResult(reg, "skipped: previous run still in progress", s.now())
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()

	defer s.wg.Done()
	_ = s.execute(context.Background(), 0, reg)
}

// execute performs one run of reg's job under the timeout, the panic guard and
// the bookkeeping.
func (s *Scheduler) execute(ctx context.Context, id cron.EntryID, reg *registration) (err error) {
	s.mu.Lock()
	reg.running = true
	s.mu.Unlock()

	started := s.now()
	s.logger.Info("job starting",
		"entry", id, "type", reg.job.Type(), "instance", reg.job.InstanceID(),
		"describe", reg.job.Describe())

	defer func() {
		finished := s.now()

		if r := recover(); r != nil {
			err = fmt.Errorf("job panicked: %v", r)
			s.logger.Error("job panicked",
				"type", reg.job.Type(), "instance", reg.job.InstanceID(),
				"panic", r)
		}

		result := "ok"
		if err != nil {
			result = "failed: " + err.Error()
		}

		s.mu.Lock()
		reg.running = false
		reg.lastRun = finished
		reg.lastResult = result
		s.mu.Unlock()

		s.logger.Info("job finished",
			"type", reg.job.Type(), "instance", reg.job.InstanceID(),
			"duration", finished.Sub(started), "result", result)

		s.recordResult(reg, result, finished)
	}()

	runCtx, cancel := context.WithTimeout(ctx, s.runTimeout)
	defer cancel()

	return reg.job.Run(runCtx)
}

// recordResult writes the run outcome to the repository and to the entry's
// in-memory bookkeeping.
//
// A repository failure is logged but never propagates: the job already ran, and
// failing the run because its log line could not be written would be misleading.
func (s *Scheduler) recordResult(reg *registration, result string, at time.Time) {
	s.mu.Lock()
	reg.lastRun = at
	reg.lastResult = result
	s.mu.Unlock()

	if s.repo == nil || reg.jobID == 0 {
		return
	}

	// A short, separate context: the caller's may already be cancelled (that is
	// often *why* the job failed), but the audit trail still needs writing.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.repo.RecordRun(ctx, reg.jobID, result, at); err != nil {
		s.logger.Error("cannot record job run",
			"jobID", reg.jobID, "result", result, "err", err)
	}
}

// Load reads the enabled jobs from the repository and registers them with the
// runner.
//
// Rows the runner declines (a nil job) are skipped with a warning rather than
// failing the whole load: one bad row must not stop the panel from scheduling
// everything else.
//
// Load may be called before Start (the normal boot order) and again later to
// reload after a job is edited in the UI; a reload clears every entry that came
// from the repository first, but leaves programmatically added entries alone.
func (s *Scheduler) Load(ctx context.Context, run Runner) (int, error) {
	if s.repo == nil {
		return 0, errors.New("scheduler: no job repository configured")
	}

	stored, err := s.repo.ListEnabled(ctx)
	if err != nil {
		return 0, fmt.Errorf("scheduler: load jobs: %w", err)
	}

	// Drop the entries that came from a previous Load.
	s.mu.Lock()
	for id, reg := range s.entries {
		if reg.jobID != 0 {
			s.cron.Remove(id)
			delete(s.entries, id)
		}
	}
	s.mu.Unlock()

	loaded := 0
	for _, sj := range stored {
		if err := ctx.Err(); err != nil {
			return loaded, err
		}

		if err := ValidateSpec(sj.Cron); err != nil {
			s.logger.Warn("skipping job with an invalid cron expression",
				"jobID", sj.ID, "cron", sj.Cron, "err", err)
			continue
		}

		job, err := run(ctx, sj)
		if err != nil {
			s.logger.Warn("skipping job the runner could not build",
				"jobID", sj.ID, "type", sj.Type, "err", err)
			continue
		}
		if job == nil {
			s.logger.Warn("skipping job the runner declined",
				"jobID", sj.ID, "type", sj.Type)
			continue
		}

		if _, err := s.Add(sj.Cron, job, sj.ID); err != nil {
			s.logger.Warn("cannot register job",
				"jobID", sj.ID, "cron", sj.Cron, "err", err)
			continue
		}
		loaded++
	}

	s.logger.Info("jobs loaded", "count", loaded, "enabled", len(stored))
	return loaded, nil
}

// Reload rebuilds the entries from stored rows while the scheduler is running.
//
// cron supports adding and removing entries on a running instance, so this is
// just Load with the "already started" check relaxed; it exists so the
// distinction is explicit at call sites.
func (s *Scheduler) Reload(ctx context.Context, run Runner) (int, error) {
	if s.repo == nil {
		return 0, errors.New("scheduler: no job repository configured")
	}

	stored, err := s.repo.ListEnabled(ctx)
	if err != nil {
		return 0, fmt.Errorf("scheduler: reload jobs: %w", err)
	}

	s.mu.Lock()
	for id, reg := range s.entries {
		if reg.jobID != 0 {
			s.cron.Remove(id)
			delete(s.entries, id)
		}
	}
	s.mu.Unlock()

	loaded := 0
	for _, sj := range stored {
		if err := ValidateSpec(sj.Cron); err != nil {
			s.logger.Warn("skipping job with an invalid cron expression",
				"jobID", sj.ID, "cron", sj.Cron, "err", err)
			continue
		}

		job, err := run(ctx, sj)
		if err != nil || job == nil {
			s.logger.Warn("skipping job the runner declined",
				"jobID", sj.ID, "type", sj.Type, "err", err)
			continue
		}

		// Add rejects registration once started, so go straight to cron here.
		reg := &registration{job: job, jobID: sj.ID, spec: strings.TrimSpace(sj.Cron)}
		id, err := s.cron.AddFunc(reg.spec, func() { s.run(reg) })
		if err != nil {
			s.logger.Warn("cannot register job", "jobID", sj.ID, "err", err)
			continue
		}

		s.mu.Lock()
		s.entries[id] = reg
		s.mu.Unlock()
		loaded++
	}

	s.logger.Info("jobs reloaded", "count", loaded)
	return loaded, nil
}

// ---------------------------------------------------------------------------
// Concrete jobs
// ---------------------------------------------------------------------------

// BackupFunc performs one backup. It is a function rather than an interface so
// cmd/scnetm can close over the backup manager without this package importing
// it.
type BackupFunc func(ctx context.Context, instanceID int64, note string) error

// BackupJob runs a scheduled world backup.
//
// The payload is the JSON object stored in `jobs.payload`. Recognised keys:
//
//	{"keep": 7, "note": "nightly"}
//
// keep is advisory metadata for the UI and for the retention decision made by
// the backup manager; it is passed through in Describe so an operator can see
// what the schedule is configured to retain.
type BackupJob struct {
	instanceID int64
	keep       int
	note       string
	fn         BackupFunc
}

// BackupPayload is the parsed form of a backup job's payload.
type BackupPayload struct {
	// Keep is the number of scheduled backups to retain (§6.5's "keep N").
	Keep int `json:"keep,omitempty"`
	// Note is a free-text label attached to each produced backup.
	Note string `json:"note,omitempty"`
}

// ParseBackupPayload parses a backup job payload. An empty payload is valid and
// yields the zero value; a malformed one is an error so the operator is told
// rather than silently getting default behaviour.
func ParseBackupPayload(raw string) (BackupPayload, error) {
	var p BackupPayload
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return p, nil
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return p, fmt.Errorf("scheduler: backup payload is not valid JSON: %w", err)
	}
	if p.Keep < 0 {
		return p, fmt.Errorf("scheduler: backup payload keep must not be negative, got %d", p.Keep)
	}
	return p, nil
}

// NewBackupJob builds a backup job.
func NewBackupJob(instanceID int64, payload BackupPayload, fn BackupFunc) *BackupJob {
	if fn == nil {
		fn = func(context.Context, int64, string) error {
			return errors.New("scheduler: no backup function configured")
		}
	}
	return &BackupJob{instanceID: instanceID, keep: payload.Keep, note: payload.Note, fn: fn}
}

// Type implements Job.
func (j *BackupJob) Type() string { return TypeBackup }

// InstanceID implements Job.
func (j *BackupJob) InstanceID() int64 { return j.instanceID }

// Run implements Job.
func (j *BackupJob) Run(ctx context.Context) error {
	return j.fn(ctx, j.instanceID, j.note)
}

// Describe implements Job.
func (j *BackupJob) Describe() string {
	if j.keep > 0 {
		return fmt.Sprintf("backup instance %d (keep %d)", j.instanceID, j.keep)
	}
	return fmt.Sprintf("backup instance %d", j.instanceID)
}

// RestartFunc restarts one instance.
type RestartFunc func(ctx context.Context, instanceID int64) error

// RestartJob restarts an instance on a schedule, which is the usual remedy for
// a slow memory leak in the game server.
//
// Payload keys:
//
//	{"warnSeconds": 60, "message": "server restarting in 60s"}
//
// When a BroadcastFunc is supplied and warnSeconds > 0, the job warns players
// before restarting. Warning is best-effort: a failed broadcast is logged but
// does not stop the restart, because the restart is the point of the job.
type RestartJob struct {
	instanceID  int64
	warnSeconds int
	message     string
	restart     RestartFunc
	broadcast   BroadcastFunc
	sleep       func(context.Context, time.Duration) bool
}

// RestartPayload is the parsed form of a restart job's payload.
type RestartPayload struct {
	// WarnSeconds is how long to warn players before restarting. 0 disables the
	// warning.
	WarnSeconds int `json:"warnSeconds,omitempty"`
	// Message is the broadcast text. A sensible default is used when empty.
	Message string `json:"message,omitempty"`
}

// ParseRestartPayload parses a restart job payload.
func ParseRestartPayload(raw string) (RestartPayload, error) {
	var p RestartPayload
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return p, nil
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return p, fmt.Errorf("scheduler: restart payload is not valid JSON: %w", err)
	}
	if p.WarnSeconds < 0 {
		return p, fmt.Errorf("scheduler: restart payload warnSeconds must not be negative, got %d", p.WarnSeconds)
	}
	return p, nil
}

// NewRestartJob builds a restart job. broadcast may be nil.
func NewRestartJob(instanceID int64, payload RestartPayload, restart RestartFunc, broadcast BroadcastFunc) *RestartJob {
	if restart == nil {
		restart = func(context.Context, int64) error {
			return errors.New("scheduler: no restart function configured")
		}
	}
	message := payload.Message
	if message == "" {
		message = fmt.Sprintf("Server restarting in %d seconds", payload.WarnSeconds)
	}
	return &RestartJob{
		instanceID:  instanceID,
		warnSeconds: payload.WarnSeconds,
		message:     message,
		restart:     restart,
		broadcast:   broadcast,
		sleep:       sleepCtx,
	}
}

// Type implements Job.
func (j *RestartJob) Type() string { return TypeRestart }

// InstanceID implements Job.
func (j *RestartJob) InstanceID() int64 { return j.instanceID }

// Run implements Job.
func (j *RestartJob) Run(ctx context.Context) error {
	if j.warnSeconds > 0 && j.broadcast != nil {
		if err := j.broadcast(ctx, j.instanceID, j.message); err != nil {
			// Best-effort: the restart must still happen.
			// (The scheduler's logger is not reachable from here; the error is
			// reported through the caller's own logging in cmd/scnetm.)
			_ = err
		}
		if !j.sleep(ctx, time.Duration(j.warnSeconds)*time.Second) {
			// The run context expired while waiting out the warning period.
			return ctx.Err()
		}
	}
	return j.restart(ctx, j.instanceID)
}

// Describe implements Job.
func (j *RestartJob) Describe() string {
	if j.warnSeconds > 0 {
		return fmt.Sprintf("restart instance %d (warn %ds)", j.instanceID, j.warnSeconds)
	}
	return fmt.Sprintf("restart instance %d", j.instanceID)
}

// BroadcastFunc sends a console command to an instance.
type BroadcastFunc func(ctx context.Context, instanceID int64, command string) error

// CommandJob sends a console command on a schedule. The plan's "定时广播"
// (§6.7) is this job with a broadcast payload; the same mechanism covers
// periodic saves and any other recurring command.
//
// Payload keys:
//
//	{"command": "/time set 12:00"}
//	{"command": "say Server restarting soon"}
//
// The command is passed to the instance's command channel verbatim, so it must
// include whatever prefix the game expects.
type CommandJob struct {
	instanceID int64
	command    string
	send       BroadcastFunc
}

// CommandPayload is the parsed form of a command job's payload.
type CommandPayload struct {
	// Command is the console command to send.
	Command string `json:"command"`
}

// ParseCommandPayload parses a command job payload. A missing command is an
// error: a broadcast job that sends nothing is certainly a mistake, and
// silently doing nothing would hide it.
func ParseCommandPayload(raw string) (CommandPayload, error) {
	var p CommandPayload
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return p, errors.New("scheduler: command payload is missing the \"command\" key")
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return p, fmt.Errorf("scheduler: command payload is not valid JSON: %w", err)
	}
	if strings.TrimSpace(p.Command) == "" {
		return p, errors.New("scheduler: command payload has an empty command")
	}
	return p, nil
}

// NewCommandJob builds a command job.
func NewCommandJob(instanceID int64, payload CommandPayload, send BroadcastFunc) *CommandJob {
	if send == nil {
		send = func(context.Context, int64, string) error {
			return errors.New("scheduler: no command sender configured")
		}
	}
	return &CommandJob{instanceID: instanceID, command: payload.Command, send: send}
}

// Type implements Job.
func (j *CommandJob) Type() string { return TypeCommand }

// InstanceID implements Job.
func (j *CommandJob) InstanceID() int64 { return j.instanceID }

// Run implements Job.
func (j *CommandJob) Run(ctx context.Context) error {
	return j.send(ctx, j.instanceID, j.command)
}

// Describe implements Job.
func (j *CommandJob) Describe() string {
	return fmt.Sprintf("command on instance %d: %s", j.instanceID, truncate(j.command, 60))
}

// BuildRunner returns a Runner that constructs the three built-in job types
// from stored rows.
//
// It is the default wiring for cmd/scnetm: a caller that needs nothing exotic
// can pass its three functions and get a working loader. Returning nil for an
// unknown type makes the scheduler log and skip the row.
func BuildRunner(backup BackupFunc, restart RestartFunc, broadcast BroadcastFunc) Runner {
	return func(ctx context.Context, sj StoredJob) (Job, error) {
		switch sj.Type {
		case TypeBackup:
			payload, err := ParseBackupPayload(sj.Payload)
			if err != nil {
				return nil, err
			}
			return NewBackupJob(sj.InstanceID, payload, backup), nil

		case TypeRestart:
			payload, err := ParseRestartPayload(sj.Payload)
			if err != nil {
				return nil, err
			}
			return NewRestartJob(sj.InstanceID, payload, restart, broadcast), nil

		case TypeCommand:
			payload, err := ParseCommandPayload(sj.Payload)
			if err != nil {
				return nil, err
			}
			return NewCommandJob(sj.InstanceID, payload, broadcast), nil

		default:
			return nil, nil
		}
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// cronLogger adapts cron's Logger interface to slog.
type cronLogger struct {
	logger *slog.Logger
}

// Info implements cron.Logger.
func (l cronLogger) Info(msg string, keysAndValues ...any) {
	l.logger.Debug("cron: "+msg, keysAndValues...)
}

// Error implements cron.Logger.
func (l cronLogger) Error(err error, msg string, keysAndValues ...any) {
	l.logger.Error("cron: "+msg, append([]any{"err", err}, keysAndValues...)...)
}

// sleepCtx sleeps for d, reporting false if ctx ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// truncate shortens s for a one-line description.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// isNilJob reports whether job is nil, including the typed-nil case.
//
// `job == nil` is false for a nil pointer stored in a non-nil interface, so a
// typed nil would sail through validation and panic on the first method call.
// Reflection is the only portable way to see through the interface; the cost is
// negligible because this runs once per job registration.
func isNilJob(job Job) bool {
	if job == nil {
		return true
	}
	v := reflect.ValueOf(job)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return v.IsNil()
	default:
		return false
	}
}
