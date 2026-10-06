package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

// testLogger discards output.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeJob is a scriptable Job.
type fakeJob struct {
	typ        string
	instanceID int64
	describe   string

	// run is invoked by Run. When nil, the job returns runErr immediately.
	run func(ctx context.Context) error
	// runErr is returned when run is nil.
	runErr error

	calls   atomic.Int64
	running atomic.Int64
	maxSeen atomic.Int64
}

func (f *fakeJob) Type() string      { return f.typ }
func (f *fakeJob) InstanceID() int64 { return f.instanceID }
func (f *fakeJob) Describe() string {
	if f.describe != "" {
		return f.describe
	}
	return f.typ
}

func (f *fakeJob) Run(ctx context.Context) error {
	f.calls.Add(1)
	cur := f.running.Add(1)
	defer f.running.Add(-1)

	// Track the high-water mark of concurrent runs; the overlap test asserts
	// this never exceeds 1.
	for {
		max := f.maxSeen.Load()
		if cur <= max || f.maxSeen.CompareAndSwap(max, cur) {
			break
		}
	}

	if f.run != nil {
		return f.run(ctx)
	}
	return f.runErr
}

// fakeRepo is an in-memory JobRepo.
type fakeRepo struct {
	mu     sync.Mutex
	stored []StoredJob
	runs   []recordedRun

	listErr   error
	recordErr error
}

type recordedRun struct {
	ID     int64
	Result string
	At     time.Time
}

func (r *fakeRepo) ListEnabled(context.Context) ([]StoredJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listErr != nil {
		return nil, r.listErr
	}
	out := make([]StoredJob, len(r.stored))
	copy(out, r.stored)
	return out, nil
}

func (r *fakeRepo) RecordRun(_ context.Context, id int64, result string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recordErr != nil {
		return r.recordErr
	}
	r.runs = append(r.runs, recordedRun{ID: id, Result: result, At: at})
	return nil
}

func (r *fakeRepo) runCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.runs)
}

func (r *fakeRepo) lastRun() (recordedRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.runs) == 0 {
		return recordedRun{}, false
	}
	return r.runs[len(r.runs)-1], true
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", timeout, what)
}

// ---------------------------------------------------------------------------
// Spec validation
// ---------------------------------------------------------------------------

func TestValidateSpec(t *testing.T) {
	t.Parallel()

	valid := []string{
		"0 4 * * *",     // the plan's default daily backup (§6.5)
		"*/5 * * * *",   // every 5 minutes
		"@daily",        // descriptor
		"@every 1h",     // interval descriptor
		"0 0 4 * * *",   // six-field with seconds
		"30 4 * * 1-5",  // weekdays
		"0 0 1 */3 *",   // quarterly
		"  0 4 * * *  ", // surrounding whitespace
	}
	for _, spec := range valid {
		if err := ValidateSpec(spec); err != nil {
			t.Errorf("ValidateSpec(%q) = %v, want nil", spec, err)
		}
	}

	invalid := []string{
		"",
		"   ",
		"not a cron",
		"0 4 * *",       // too few fields
		"0 4 * * * * *", // too many fields
		"60 * * * *",    // minute out of range
		"* 25 * * *",    // hour out of range
		"* * 32 * *",    // day-of-month out of range
		"* * * 13 *",    // month out of range
		"*/0 * * * *",   // zero step
		"a b c d e",     // non-numeric
	}
	for _, spec := range invalid {
		err := ValidateSpec(spec)
		if err == nil {
			t.Errorf("ValidateSpec(%q) = nil, want an error", spec)
			continue
		}
		if !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("ValidateSpec(%q) = %v, want ErrInvalidSpec", spec, err)
		}
	}
}

// TestAddRejectsMalformedSpecWithUsefulError pins the error contract: a bad
// expression must be named in the message, because "parsing failed" tells an
// operator nothing.
func TestAddRejectsMalformedSpecWithUsefulError(t *testing.T) {
	t.Parallel()

	s := New(Options{Logger: testLogger()})

	_, err := s.Add("0 4 * *", &fakeJob{typ: TypeBackup}, 0)
	if err == nil {
		t.Fatal("Add must reject a malformed cron expression")
	}
	if !errors.Is(err, ErrInvalidSpec) {
		t.Errorf("error = %v, want ErrInvalidSpec", err)
	}
	if !strings.Contains(err.Error(), "0 4 * *") {
		t.Errorf("error = %q, want it to quote the offending expression", err)
	}
}

func TestAddRejectsNilJob(t *testing.T) {
	t.Parallel()

	s := New(Options{Logger: testLogger()})
	if _, err := s.Add("0 4 * * *", nil, 0); err == nil {
		t.Fatal("Add must reject a nil job")
	}
}

// ---------------------------------------------------------------------------
// Firing
// ---------------------------------------------------------------------------

// TestJobActuallyFires is the baseline: a job registered with a
// every-second schedule runs, with nobody calling RunNow.
func TestJobActuallyFires(t *testing.T) {
	t.Parallel()

	job := &fakeJob{typ: TypeBackup, instanceID: 7}
	s := New(Options{Logger: testLogger(), RunTimeout: 5 * time.Second})

	if _, err := s.Add("@every 1s", job, 0); err != nil {
		t.Fatalf("Add: %v", err)
	}

	s.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Stop(ctx)
	})

	waitFor(t, 10*time.Second, "the job to fire on its own", func() bool {
		return job.calls.Load() >= 2
	})
}

// TestEntriesExposeSchedule checks the UI-facing snapshot: next/prev times come
// from cron, and the job's own metadata is present.
func TestEntriesExposeSchedule(t *testing.T) {
	t.Parallel()

	job := &fakeJob{typ: TypeRestart, instanceID: 3, describe: "restart instance 3"}
	s := New(Options{Logger: testLogger()})

	id, err := s.Add("0 4 * * *", job, 42)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	entries := s.Entries()
	if len(entries) != 1 {
		t.Fatalf("Entries() = %d, want 1", len(entries))
	}

	e := entries[0]
	if e.ID != id {
		t.Errorf("ID = %d, want %d", e.ID, id)
	}
	if e.Spec != "0 4 * * *" {
		t.Errorf("Spec = %q", e.Spec)
	}
	if e.JobID != 42 {
		t.Errorf("JobID = %d, want 42", e.JobID)
	}
	if e.Type != TypeRestart || e.InstanceID != 3 {
		t.Errorf("type/instance = %q/%d, want restart/3", e.Type, e.InstanceID)
	}
	if e.Describe != "restart instance 3" {
		t.Errorf("Describe = %q", e.Describe)
	}
	if e.Running {
		t.Error("Running = true before any run")
	}
	if !e.Next.IsZero() {
		t.Errorf("Next = %v, want zero before Start (cron has not scheduled yet)", e.Next)
	}

	// After Start, cron must have computed the next run.
	s.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Stop(ctx)
	})

	waitFor(t, 5*time.Second, "cron to compute the next run time", func() bool {
		entries := s.Entries()
		return len(entries) == 1 && !entries[0].Next.IsZero()
	})

	e = s.Entries()[0]
	if e.Next.Hour() != 4 {
		t.Errorf("Next = %v, want 04:00 in the scheduler's location", e.Next)
	}
	if e.Next.Before(time.Now().Add(-time.Minute)) {
		t.Errorf("Next = %v, want a future time", e.Next)
	}
}

func TestRemove(t *testing.T) {
	t.Parallel()

	job := &fakeJob{typ: TypeBackup}
	s := New(Options{Logger: testLogger()})

	id, err := s.Add("@every 1s", job, 0)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	s.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Stop(ctx)
	})

	waitFor(t, 10*time.Second, "the job to fire once", func() bool { return job.calls.Load() >= 1 })

	s.Remove(id)
	if got := len(s.Entries()); got != 0 {
		t.Fatalf("Entries() = %d after Remove, want 0", got)
	}

	after := job.calls.Load()
	time.Sleep(1500 * time.Millisecond)
	if got := job.calls.Load(); got != after {
		t.Errorf("the job fired %d more times after Remove", got-after)
	}

	// Removing an unknown id must be a no-op, so the "deleted in the UI" path
	// is idempotent.
	s.Remove(cron.EntryID(9999))
}

// TestRunNow checks the manual trigger path used by the UI's "run now" button.
func TestRunNow(t *testing.T) {
	t.Parallel()

	job := &fakeJob{typ: TypeBackup, instanceID: 1}
	s := New(Options{Logger: testLogger(), RunTimeout: time.Second})

	id, err := s.Add("0 4 * * *", job, 0)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := s.RunNow(context.Background(), id); err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	if got := job.calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1", got)
	}

	entries := s.Entries()
	if entries[0].LastResult != "ok" {
		t.Errorf("LastResult = %q, want ok", entries[0].LastResult)
	}
	if entries[0].LastRun.IsZero() {
		t.Error("LastRun must be recorded")
	}

	if err := s.RunNow(context.Background(), cron.EntryID(4242)); err == nil {
		t.Error("RunNow on an unknown entry must fail")
	}
}

// ---------------------------------------------------------------------------
// Overlap protection
// ---------------------------------------------------------------------------

// TestOverlapProtection is the §6.7 requirement: a job whose previous run is
// still going must not start again. The schedule here fires far faster than the
// job completes.
func TestOverlapProtection(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	var started sync.Once
	startedCh := make(chan struct{})

	job := &fakeJob{
		typ: TypeBackup,
		run: func(ctx context.Context) error {
			started.Do(func() { close(startedCh) })
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}

	repo := &fakeRepo{}
	s := New(Options{
		Logger:     testLogger(),
		RunTimeout: 30 * time.Second,
		Repo:       repo,
	})

	if _, err := s.Add("@every 1s", job, 5); err != nil {
		t.Fatalf("Add: %v", err)
	}

	s.Start()
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unblock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Stop(ctx)
	})

	<-startedCh

	// Let several schedule ticks pass while the first run is still blocked.
	//
	// The assertion is maxSeen == 1 rather than calls == 1: the block below has
	// ensured the first run is in flight, but @every 1s counts from
	// registration, so a tick can land while a *previous* run is completing,
	// which is a legitimate second completed run, not an overlap. maxSeen is
	// the property the guard actually promises: never two runs at once.
	time.Sleep(3500 * time.Millisecond)

	if got := job.maxSeen.Load(); got != 1 {
		t.Fatalf("observed %d concurrent runs, want exactly 1 (the overlap guard failed)", got)
	}

	// The skipped runs must be recorded, not silently dropped: an operator
	// seeing "skipped" in the jobs table learns their backup is too slow.
	waitFor(t, 5*time.Second, "a skipped run to be recorded", func() bool {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		for _, r := range repo.runs {
			if strings.Contains(r.Result, "skipped") {
				return true
			}
		}
		return false
	})

	// Once the first run finishes, the job runs again.
	unblock()
	waitFor(t, 15*time.Second, "the job to become eligible again", func() bool {
		return job.calls.Load() >= 2
	})
}

// TestRunNowRejectsOverlap checks the guard also covers the manual path.
func TestRunNowRejectsOverlap(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	startedCh := make(chan struct{})
	var once sync.Once

	job := &fakeJob{
		typ: TypeBackup,
		run: func(ctx context.Context) error {
			once.Do(func() { close(startedCh) })
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}

	s := New(Options{Logger: testLogger(), RunTimeout: 30 * time.Second})
	id, err := s.Add("0 4 * * *", job, 0)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	go func() { _ = s.RunNow(context.Background(), id) }()
	<-startedCh

	err = s.RunNow(context.Background(), id)
	if !errors.Is(err, ErrOverlap) {
		t.Fatalf("second RunNow = %v, want ErrOverlap", err)
	}

	close(release)
}

// ---------------------------------------------------------------------------
// Panic recovery
// ---------------------------------------------------------------------------

// TestPanicRecovery is the "one bad job cannot kill the scheduler" requirement.
func TestPanicRecovery(t *testing.T) {
	t.Parallel()

	panicky := &fakeJob{
		typ: TypeBackup,
		run: func(context.Context) error { panic("job exploded") },
	}
	healthy := &fakeJob{typ: TypeCommand}

	s := New(Options{Logger: testLogger(), RunTimeout: time.Second})

	panickyID, err := s.Add("@every 1s", panicky, 0)
	if err != nil {
		t.Fatalf("Add panicky: %v", err)
	}
	if _, err := s.Add("@every 1s", healthy, 0); err != nil {
		t.Fatalf("Add healthy: %v", err)
	}

	s.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Stop(ctx)
	})

	// The panicking job must keep being scheduled (recovered each time)...
	waitFor(t, 15*time.Second, "the panicking job to be retried after recovering", func() bool {
		return panicky.calls.Load() >= 2
	})

	// ... and the healthy one must be unaffected.
	waitFor(t, 15*time.Second, "the healthy job to keep running alongside the panicking one", func() bool {
		return healthy.calls.Load() >= 2
	})

	// The panic must be visible as a failure, not swallowed.
	waitFor(t, 5*time.Second, "the panic to be recorded as a failed run", func() bool {
		for _, e := range s.Entries() {
			if e.ID == panickyID && strings.Contains(e.LastResult, "panicked") {
				return true
			}
		}
		return false
	})

	// RunNow must also survive it and report it as an error.
	err = s.RunNow(context.Background(), panickyID)
	if err == nil {
		t.Fatal("RunNow on a panicking job must return an error, not panic the caller")
	}
	if !strings.Contains(err.Error(), "panicked") {
		t.Errorf("error = %q, want it to mention the panic", err)
	}
}

// ---------------------------------------------------------------------------
// Timeouts
// ---------------------------------------------------------------------------

// TestRunContextCarriesTimeout checks that a job is given a deadline, so a
// well-behaved job can abort its own work.
func TestRunContextCarriesTimeout(t *testing.T) {
	t.Parallel()

	var seen atomic.Bool
	var hasDeadline atomic.Bool

	job := &fakeJob{
		typ: TypeBackup,
		run: func(ctx context.Context) error {
			seen.Store(true)
			if _, ok := ctx.Deadline(); ok {
				hasDeadline.Store(true)
			}
			return nil
		},
	}

	s := New(Options{Logger: testLogger(), RunTimeout: 42 * time.Second})
	id, err := s.Add("0 4 * * *", job, 0)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := s.RunNow(context.Background(), id); err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	if !seen.Load() {
		t.Fatal("the job did not run")
	}
	if !hasDeadline.Load() {
		t.Error("the run context must carry a deadline")
	}
}

// TestRunTimeoutIsEnforced checks the timeout actually fires for a job that
// honours its context.
func TestRunTimeoutIsEnforced(t *testing.T) {
	t.Parallel()

	job := &fakeJob{
		typ: TypeBackup,
		run: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}

	s := New(Options{Logger: testLogger(), RunTimeout: 100 * time.Millisecond})
	id, err := s.Add("0 4 * * *", job, 0)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	start := time.Now()
	err = s.RunNow(context.Background(), id)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunNow = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("the timeout took %v to fire", elapsed)
	}

	for _, e := range s.Entries() {
		if !strings.Contains(e.LastResult, "failed") {
			t.Errorf("LastResult = %q, want a failure", e.LastResult)
		}
	}
}

// ---------------------------------------------------------------------------
// Graceful stop
// ---------------------------------------------------------------------------

// TestStopIsGraceful checks Stop waits for an in-flight job rather than
// abandoning it mid-write.
func TestStopIsGraceful(t *testing.T) {
	t.Parallel()

	startedCh := make(chan struct{})
	finished := atomic.Bool{}

	job := &fakeJob{
		typ: TypeBackup,
		run: func(ctx context.Context) error {
			close(startedCh)
			select {
			case <-time.After(200 * time.Millisecond):
				finished.Store(true)
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}

	s := New(Options{Logger: testLogger(), RunTimeout: 10 * time.Second})
	id, err := s.Add("0 4 * * *", job, 0)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	s.Start()
	go func() { _ = s.RunNow(context.Background(), id) }()
	<-startedCh

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !finished.Load() {
		t.Error("Stop returned before the in-flight job finished; the run was abandoned")
	}
}

func TestStopWithoutStart(t *testing.T) {
	t.Parallel()

	s := New(Options{Logger: testLogger()})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := s.Stop(ctx); err != nil {
		t.Errorf("Stop before Start = %v, want nil", err)
	}
}

// TestStopHonoursContext checks a shutdown timeout is respected.
func TestStopHonoursContext(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	startedCh := make(chan struct{})

	job := &fakeJob{
		typ: TypeBackup,
		run: func(ctx context.Context) error {
			close(startedCh)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}

	s := New(Options{Logger: testLogger(), RunTimeout: 30 * time.Second})
	id, err := s.Add("0 4 * * *", job, 0)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	s.Start()
	go func() { _ = s.RunNow(context.Background(), id) }()
	<-startedCh
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := s.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop = %v, want context.DeadlineExceeded", err)
	}
}

// TestAddAfterStartIsRejected documents the registration contract: jobs are
// registered before Start (or via Reload), which keeps the entry table's
// mutation rules simple.
func TestAddAfterStartIsRejected(t *testing.T) {
	t.Parallel()

	s := New(Options{Logger: testLogger()})
	s.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Stop(ctx)
	})

	_, err := s.Add("0 4 * * *", &fakeJob{typ: TypeBackup}, 0)
	if !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("Add after Start = %v, want ErrAlreadyStarted", err)
	}
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

// TestRunResultIsRecorded checks the integration with the jobs table: each run
// writes last_run/last_result back, which is what makes those columns useful.
func TestRunResultIsRecorded(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{}
	job := &fakeJob{typ: TypeBackup, instanceID: 9}

	s := New(Options{Logger: testLogger(), Repo: repo, RunTimeout: time.Second})
	id, err := s.Add("0 4 * * *", job, 77)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := s.RunNow(context.Background(), id); err != nil {
		t.Fatalf("RunNow: %v", err)
	}

	waitFor(t, 2*time.Second, "the successful run to be recorded", func() bool {
		return repo.runCount() == 1
	})

	got, _ := repo.lastRun()
	if got.ID != 77 {
		t.Errorf("recorded job id = %d, want 77", got.ID)
	}
	if got.Result != "ok" {
		t.Errorf("recorded result = %q, want ok", got.Result)
	}
	if got.At.IsZero() {
		t.Error("recorded at must not be zero")
	}
}

// TestFailedRunIsRecordedWithReason checks a failure is persisted with a reason
// an operator can act on.
func TestFailedRunIsRecordedWithReason(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{}
	job := &fakeJob{typ: TypeBackup, runErr: errors.New("disk full")}

	s := New(Options{Logger: testLogger(), Repo: repo, RunTimeout: time.Second})
	id, err := s.Add("0 4 * * *", job, 5)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := s.RunNow(context.Background(), id); err == nil {
		t.Fatal("RunNow must return the job's error")
	}

	waitFor(t, 2*time.Second, "the failed run to be recorded", func() bool {
		return repo.runCount() == 1
	})

	got, _ := repo.lastRun()
	if !strings.Contains(got.Result, "disk full") {
		t.Errorf("recorded result = %q, want it to include the failure reason", got.Result)
	}
	if !strings.Contains(got.Result, "failed") {
		t.Errorf("recorded result = %q, want it marked as failed", got.Result)
	}
}

// TestProgrammaticJobIsNotPersisted checks that a job with no row (the panel's
// built-in default backup job has jobID 0) does not try to update a nonexistent
// row.
func TestProgrammaticJobIsNotPersisted(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{}
	job := &fakeJob{typ: TypeBackup}

	s := New(Options{Logger: testLogger(), Repo: repo, RunTimeout: time.Second})
	id, err := s.Add("0 4 * * *", job, 0)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := s.RunNow(context.Background(), id); err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	if got := repo.runCount(); got != 0 {
		t.Errorf("RecordRun was called %d times for a job with no row, want 0", got)
	}
}

// TestRecordRunFailureDoesNotFailTheJob checks that a database hiccup does not
// turn a successful backup into a reported failure.
func TestRecordRunFailureDoesNotFailTheJob(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{recordErr: errors.New("database is locked")}
	job := &fakeJob{typ: TypeBackup}

	s := New(Options{Logger: testLogger(), Repo: repo, RunTimeout: time.Second})
	id, err := s.Add("0 4 * * *", job, 3)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := s.RunNow(context.Background(), id); err != nil {
		t.Fatalf("RunNow = %v, want nil: a bookkeeping failure must not fail the job", err)
	}
}

// TestLoadRegistersEnabledJobs checks the boot path.
func TestLoadRegistersEnabledJobs(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{stored: []StoredJob{
		{ID: 1, InstanceID: 10, Type: TypeBackup, Cron: "0 4 * * *", Enabled: true},
		{ID: 2, InstanceID: 11, Type: TypeRestart, Cron: "0 6 * * 0", Payload: `{"warnSeconds":30}`, Enabled: true},
		{ID: 3, InstanceID: 12, Type: TypeCommand, Cron: "@daily", Payload: `{"command":"say hi"}`, Enabled: true},
	}}

	s := New(Options{Logger: testLogger(), Repo: repo, RunTimeout: time.Second})

	loaded, err := s.Load(context.Background(), BuildRunner(nil, nil, nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded != 3 {
		t.Fatalf("loaded = %d, want 3", loaded)
	}

	entries := s.Entries()
	if len(entries) != 3 {
		t.Fatalf("Entries() = %d, want 3", len(entries))
	}
	// Entries are not sorted by JobID (cron assigns its own ids), so compare as
	// a set.
	jobIDs := map[int64]bool{}
	for _, e := range entries {
		jobIDs[e.JobID] = true
	}
	for _, want := range []int64{1, 2, 3} {
		if !jobIDs[want] {
			t.Errorf("job %d was not registered", want)
		}
	}
}

// TestLoadSkipsBadRows checks that one unusable row does not stop the rest.
func TestLoadSkipsBadRows(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{stored: []StoredJob{
		{ID: 1, InstanceID: 10, Type: TypeBackup, Cron: "0 4 * * *", Enabled: true},
		{ID: 2, InstanceID: 11, Type: TypeBackup, Cron: "not a cron", Enabled: true},
		{ID: 3, InstanceID: 12, Type: TypeCommand, Cron: "@daily", Payload: `{bad json`, Enabled: true},
		{ID: 4, InstanceID: 13, Type: "unknown-type", Cron: "@daily", Enabled: true},
		{ID: 5, InstanceID: 14, Type: TypeBackup, Cron: "@hourly", Enabled: true},
	}}

	s := New(Options{Logger: testLogger(), Repo: repo, RunTimeout: time.Second})

	loaded, err := s.Load(context.Background(), BuildRunner(nil, nil, nil))
	if err != nil {
		t.Fatalf("Load must not fail because of bad rows: %v", err)
	}
	if loaded != 2 {
		t.Fatalf("loaded = %d, want 2 (the malformed, unparsable and unknown rows skipped)", loaded)
	}
}

func TestLoadWithoutRepo(t *testing.T) {
	t.Parallel()

	s := New(Options{Logger: testLogger()})
	if _, err := s.Load(context.Background(), BuildRunner(nil, nil, nil)); err == nil {
		t.Fatal("Load without a repository must be an error")
	}
	if _, err := s.Reload(context.Background(), BuildRunner(nil, nil, nil)); err == nil {
		t.Fatal("Reload without a repository must be an error")
	}
}

func TestLoadPropagatesRepoError(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{listErr: errors.New("database is locked")}
	s := New(Options{Logger: testLogger(), Repo: repo})

	if _, err := s.Load(context.Background(), BuildRunner(nil, nil, nil)); err == nil {
		t.Fatal("Load must propagate a repository error")
	}
}

// TestReloadReplacesJobs checks the "operator edited a job" path: the old entry
// goes away and the new one takes effect without a restart.
func TestReloadReplacesJobs(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{stored: []StoredJob{
		{ID: 1, InstanceID: 10, Type: TypeBackup, Cron: "0 4 * * *", Enabled: true},
	}}

	s := New(Options{Logger: testLogger(), Repo: repo, RunTimeout: time.Second})
	if _, err := s.Load(context.Background(), BuildRunner(nil, nil, nil)); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(s.Entries()); got != 1 {
		t.Fatalf("Entries() = %d, want 1", got)
	}

	// The operator adds a second job and changes the first.
	repo.mu.Lock()
	repo.stored = []StoredJob{
		{ID: 1, InstanceID: 10, Type: TypeRestart, Cron: "0 5 * * *", Enabled: true},
		{ID: 2, InstanceID: 11, Type: TypeCommand, Cron: "@daily", Payload: `{"command":"say hi"}`, Enabled: true},
	}
	repo.mu.Unlock()

	s.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Stop(ctx)
	})

	loaded, err := s.Reload(context.Background(), BuildRunner(nil, nil, nil))
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if loaded != 2 {
		t.Fatalf("reloaded = %d, want 2", loaded)
	}

	entries := s.Entries()
	if len(entries) != 2 {
		t.Fatalf("Entries() = %d after Reload, want 2 (the stale entry must be gone)", len(entries))
	}
	for _, e := range entries {
		if e.JobID == 1 && e.Type != TypeRestart {
			t.Errorf("job 1 type = %q, want the reloaded restart", e.Type)
		}
		if e.JobID == 1 && e.Spec != "0 5 * * *" {
			t.Errorf("job 1 spec = %q, want the reloaded expression", e.Spec)
		}
	}
}

// TestReloadLeavesProgrammaticEntries checks that Reload only clears entries
// that came from the repository.
func TestReloadLeavesProgrammaticEntries(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{stored: []StoredJob{
		{ID: 1, InstanceID: 10, Type: TypeBackup, Cron: "0 4 * * *", Enabled: true},
	}}

	s := New(Options{Logger: testLogger(), Repo: repo, RunTimeout: time.Second})
	if _, err := s.Load(context.Background(), BuildRunner(nil, nil, nil)); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := s.Add("@every 6h", &fakeJob{typ: TypeBackup, describe: "built-in"}, 0); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if _, err := s.Reload(context.Background(), BuildRunner(nil, nil, nil)); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	entries := s.Entries()
	if len(entries) != 2 {
		t.Fatalf("Entries() = %d, want 2 (the programmatic entry must survive)", len(entries))
	}
	var programmatic int
	for _, e := range entries {
		if e.JobID == 0 {
			programmatic++
		}
	}
	if programmatic != 1 {
		t.Errorf("programmatic entries = %d, want 1", programmatic)
	}
}

// ---------------------------------------------------------------------------
// Concrete jobs
// ---------------------------------------------------------------------------

func TestParseBackupPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    BackupPayload
		wantErr bool
	}{
		{"empty", "", BackupPayload{}, false},
		{"empty object", "{}", BackupPayload{}, false},
		{"full", `{"keep":7,"note":"nightly"}`, BackupPayload{Keep: 7, Note: "nightly"}, false},
		{"whitespace", "  {\"keep\":3}  ", BackupPayload{Keep: 3}, false},
		{"bad json", "{nope", BackupPayload{}, true},
		{"negative keep", `{"keep":-1}`, BackupPayload{}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseBackupPayload(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseBackupPayload(%q) = %+v, want an error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseBackupPayload(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseRestartPayload(t *testing.T) {
	t.Parallel()

	got, err := ParseRestartPayload(`{"warnSeconds":60,"message":"bye"}`)
	if err != nil {
		t.Fatalf("ParseRestartPayload: %v", err)
	}
	if got.WarnSeconds != 60 || got.Message != "bye" {
		t.Errorf("got %+v", got)
	}

	if _, err := ParseRestartPayload(`{"warnSeconds":-5}`); err == nil {
		t.Error("a negative warnSeconds must be rejected")
	}
	if _, err := ParseRestartPayload(`{nope`); err == nil {
		t.Error("malformed JSON must be rejected")
	}
	if got, err := ParseRestartPayload(""); err != nil || got.WarnSeconds != 0 {
		t.Errorf("empty payload = %+v, %v; want the zero value and no error", got, err)
	}
}

func TestParseCommandPayload(t *testing.T) {
	t.Parallel()

	got, err := ParseCommandPayload(`{"command":"/time set 12:00"}`)
	if err != nil {
		t.Fatalf("ParseCommandPayload: %v", err)
	}
	if got.Command != "/time set 12:00" {
		t.Errorf("Command = %q", got.Command)
	}

	// A broadcast job that sends nothing is certainly a mistake.
	if _, err := ParseCommandPayload(""); err == nil {
		t.Error("an empty payload must be rejected (no command)")
	}
	if _, err := ParseCommandPayload("{}"); err == nil {
		t.Error("a payload without a command key must be rejected")
	}
	if _, err := ParseCommandPayload(`{"command":"   "}`); err == nil {
		t.Error("a whitespace-only command must be rejected")
	}
	if _, err := ParseCommandPayload(`{bad`); err == nil {
		t.Error("malformed JSON must be rejected")
	}
}

func TestBackupJob(t *testing.T) {
	t.Parallel()

	var gotInstance int64
	var gotNote string
	calls := 0

	fn := func(_ context.Context, instanceID int64, note string) error {
		calls++
		gotInstance, gotNote = instanceID, note
		return nil
	}

	job := NewBackupJob(7, BackupPayload{Keep: 7, Note: "nightly"}, fn)

	if job.Type() != TypeBackup {
		t.Errorf("Type() = %q", job.Type())
	}
	if job.InstanceID() != 7 {
		t.Errorf("InstanceID() = %d", job.InstanceID())
	}
	if !strings.Contains(job.Describe(), "keep 7") {
		t.Errorf("Describe() = %q, want it to mention the retention", job.Describe())
	}

	if err := job.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls != 1 || gotInstance != 7 || gotNote != "nightly" {
		t.Errorf("calls=%d instance=%d note=%q", calls, gotInstance, gotNote)
	}

	// Describe without a keep value.
	if got := NewBackupJob(7, BackupPayload{}, fn).Describe(); strings.Contains(got, "keep") {
		t.Errorf("Describe() = %q, want no retention clause when keep is 0", got)
	}

	// A nil function must produce an error, not a nil-pointer panic.
	if err := NewBackupJob(1, BackupPayload{}, nil).Run(context.Background()); err == nil {
		t.Error("a backup job without a function must fail cleanly")
	}
}

func TestRestartJob(t *testing.T) {
	t.Parallel()

	var restarted atomic.Int64
	var warned atomic.Int64

	restart := func(context.Context, int64) error {
		restarted.Add(1)
		return nil
	}
	var gotWarning atomic.Value
	warn := func(_ context.Context, id int64, msg string) error {
		if id != 3 {
			t.Errorf("warn instance = %d, want 3", id)
		}
		warned.Add(1)
		gotWarning.Store(msg)
		return nil
	}

	// A configured Message is used verbatim as the broadcast text, so the hook
	// below only removes the real wait; the message itself must pass through.
	job := NewRestartJob(3, RestartPayload{WarnSeconds: 1, Message: "going down"}, restart, warn)
	job.sleep = func(context.Context, time.Duration) bool { return true }

	if job.Type() != TypeRestart {
		t.Errorf("Type() = %q", job.Type())
	}
	if !strings.Contains(job.Describe(), "warn 1s") {
		t.Errorf("Describe() = %q", job.Describe())
	}
	if err := job.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if restarted.Load() != 1 {
		t.Errorf("restarted = %d, want 1", restarted.Load())
	}
	if warned.Load() != 1 {
		t.Errorf("warned = %d, want 1", warned.Load())
	}
	if got, _ := gotWarning.Load().(string); got != "going down" {
		t.Errorf("warning message = %q, want the configured message verbatim", got)
	}

	// With no Message configured, the default must mention the restart.
	defaulted := NewRestartJob(1, RestartPayload{WarnSeconds: 30},
		func(context.Context, int64) error { return nil },
		func(_ context.Context, _ int64, msg string) error {
			gotWarning.Store(msg)
			return nil
		},
	)
	defaulted.sleep = func(context.Context, time.Duration) bool { return true }
	if err := defaulted.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, _ := gotWarning.Load().(string); !strings.Contains(got, "restarting") {
		t.Errorf("default warning message = %q, want a restart notice", got)
	}
}

// TestRestartJobProceedsWhenWarningFails pins the deliberate choice: the
// restart is the point of the job, so a failed broadcast must not cancel it.
func TestRestartJobProceedsWhenWarningFails(t *testing.T) {
	t.Parallel()

	var restarted atomic.Bool

	job := NewRestartJob(1, RestartPayload{WarnSeconds: 5},
		func(context.Context, int64) error { restarted.Store(true); return nil },
		func(context.Context, int64, string) error { return errors.New("console is down") },
	)
	job.sleep = func(context.Context, time.Duration) bool { return true }

	if err := job.Run(context.Background()); err != nil {
		t.Fatalf("Run = %v, want the restart to proceed despite the failed warning", err)
	}
	if !restarted.Load() {
		t.Error("the restart did not happen")
	}
}

func TestRestartJobHonoursCancellationDuringWarning(t *testing.T) {
	t.Parallel()

	var restarted atomic.Bool

	job := NewRestartJob(1, RestartPayload{WarnSeconds: 600},
		func(context.Context, int64) error { restarted.Store(true); return nil },
		func(context.Context, int64, string) error { return nil },
	)
	// The run context expires before the warning period elapses, which is what
	// happens when the scheduler's per-run timeout is shorter than the
	// configured warning window.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := job.Run(ctx)
	if err == nil {
		t.Fatal("Run must report the cancellation")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Run = %v, want context.DeadlineExceeded", err)
	}
	if restarted.Load() {
		t.Error("the restart must not happen once the run context has expired")
	}
}

func TestCommandJob(t *testing.T) {
	t.Parallel()

	var gotCommand string
	job := NewCommandJob(4, CommandPayload{Command: "/player list"}, func(_ context.Context, id int64, cmd string) error {
		if id != 4 {
			t.Errorf("instance = %d, want 4", id)
		}
		gotCommand = cmd
		return nil
	})

	if job.Type() != TypeCommand {
		t.Errorf("Type() = %q", job.Type())
	}
	if job.InstanceID() != 4 {
		t.Errorf("InstanceID() = %d", job.InstanceID())
	}
	if !strings.Contains(job.Describe(), "/player list") {
		t.Errorf("Describe() = %q", job.Describe())
	}
	if err := job.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotCommand != "/player list" {
		t.Errorf("command = %q", gotCommand)
	}

	if err := NewCommandJob(1, CommandPayload{Command: "x"}, nil).Run(context.Background()); err == nil {
		t.Error("a command job without a sender must fail cleanly")
	}
}

func TestDescribeTruncatesLongCommands(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 200)
	job := NewCommandJob(1, CommandPayload{Command: long}, nil)
	if got := job.Describe(); len(got) > 120 {
		t.Errorf("Describe() = %d chars, want a truncated description: %q", len(got), got)
	}
}

// TestBuildRunner covers all three types plus the unknown-type fallback.
func TestBuildRunner(t *testing.T) {
	t.Parallel()

	runner := BuildRunner(
		func(context.Context, int64, string) error { return nil },
		func(context.Context, int64) error { return nil },
		func(context.Context, int64, string) error { return nil },
	)

	tests := []struct {
		name    string
		sj      StoredJob
		wantTyp string
	}{
		{"backup", StoredJob{Type: TypeBackup, InstanceID: 1, Payload: `{"keep":3}`}, TypeBackup},
		{"backup empty payload", StoredJob{Type: TypeBackup, InstanceID: 1}, TypeBackup},
		{"restart", StoredJob{Type: TypeRestart, InstanceID: 2, Payload: `{"warnSeconds":10}`}, TypeRestart},
		{"command", StoredJob{Type: TypeCommand, InstanceID: 3, Payload: `{"command":"say hi"}`}, TypeCommand},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			job, err := runner(context.Background(), tc.sj)
			if err != nil {
				t.Fatalf("runner: %v", err)
			}
			if job == nil {
				t.Fatal("runner returned a nil job")
			}
			if job.Type() != tc.wantTyp {
				t.Errorf("Type() = %q, want %q", job.Type(), tc.wantTyp)
			}
			if job.InstanceID() != tc.sj.InstanceID {
				t.Errorf("InstanceID() = %d, want %d", job.InstanceID(), tc.sj.InstanceID)
			}
		})
	}

	// An unknown type is declined with a nil error, which makes the scheduler
	// log and skip rather than fail the whole load.
	job, err := runner(context.Background(), StoredJob{Type: "wat"})
	if err != nil {
		t.Fatalf("runner(unknown) = %v, want nil", err)
	}
	if job != nil {
		t.Errorf("runner(unknown) = %v, want nil", job)
	}

	// A malformed payload is an error, so a bad row is visible rather than
	// silently defaulted.
	if _, err := runner(context.Background(), StoredJob{Type: TypeBackup, Payload: "{bad"}); err == nil {
		t.Error("runner must reject a malformed backup payload")
	}
	if _, err := runner(context.Background(), StoredJob{Type: TypeCommand, Payload: "{}"}); err == nil {
		t.Error("runner must reject a command payload with no command")
	}
}

// TestEntriesRunBookskeepingInTable drives two entries and checks the snapshot
// reflects per-entry results rather than a single global one.
func TestEntriesBookkeeping(t *testing.T) {
	t.Parallel()

	ok := &fakeJob{typ: TypeBackup, instanceID: 1}
	bad := &fakeJob{typ: TypeBackup, instanceID: 2, runErr: errors.New("nope")}

	s := New(Options{Logger: testLogger(), RunTimeout: time.Second})
	okID, err := s.Add("0 4 * * *", ok, 0)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	badID, err := s.Add("0 5 * * *", bad, 0)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := s.RunNow(context.Background(), okID); err != nil {
		t.Fatalf("RunNow(ok): %v", err)
	}
	if err := s.RunNow(context.Background(), badID); err == nil {
		t.Fatal("RunNow(bad) must fail")
	}

	byID := map[cron.EntryID]Entry{}
	for _, e := range s.Entries() {
		byID[e.ID] = e
	}

	if got := byID[okID].LastResult; got != "ok" {
		t.Errorf("ok entry LastResult = %q, want ok", got)
	}
	if got := byID[okID].Running; got {
		t.Error("ok entry Running = true after the run finished")
	}
	if got := byID[badID].LastResult; !strings.Contains(got, "nope") {
		t.Errorf("bad entry LastResult = %q, want it to contain the error", got)
	}
	if byID[badID].LastRun.IsZero() {
		t.Error("bad entry LastRun must be recorded")
	}
}

// TestConcurrentEntriesRunSafely exercises the scheduler under -race with
// several entries firing at once.
func TestConcurrentEntriesRunSafely(t *testing.T) {
	t.Parallel()

	s := New(Options{Logger: testLogger(), RunTimeout: 5 * time.Second})

	jobs := make([]*fakeJob, 0, 8)
	for i := 0; i < 8; i++ {
		j := &fakeJob{typ: TypeBackup, instanceID: int64(i)}
		jobs = append(jobs, j)
		if _, err := s.Add("@every 1s", j, int64(i+1)); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	s.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.Stop(ctx)
	})

	// Hammer the read paths while jobs fire.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			_ = s.Entries()
		}
	}()

	waitFor(t, 15*time.Second, "every entry to fire at least once", func() bool {
		for _, j := range jobs {
			if j.calls.Load() == 0 {
				return false
			}
		}
		return true
	})
	<-done
}

func TestSpecNilJobInterface(t *testing.T) {
	t.Parallel()

	// A typed nil must be caught rather than panicking later.
	var job *fakeJob
	s := New(Options{Logger: testLogger()})
	if _, err := s.Add("@daily", job, 0); err == nil {
		t.Error("Add with a typed nil job must be rejected")
	}
}

func TestCronLoggerRoutesToSlog(t *testing.T) {
	t.Parallel()

	// Simply exercise both methods so they are covered and cannot panic.
	l := cronLogger{logger: testLogger()}
	l.Info("hello", "key", "value")
	l.Error(errors.New("boom"), "failed", "key", "value")
}

func TestTruncate(t *testing.T) {
	t.Parallel()

	if got := truncate("abc", 10); got != "abc" {
		t.Errorf("truncate short = %q", got)
	}
	if got := truncate("abcdef", 3); got != "abc…" {
		t.Errorf("truncate long = %q", got)
	}
}

func TestSleepCtx(t *testing.T) {
	t.Parallel()

	if !sleepCtx(context.Background(), 0) {
		t.Error("a zero sleep must succeed when the context is live")
	}
	if !sleepCtx(context.Background(), time.Millisecond) {
		t.Error("a short sleep must succeed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepCtx(ctx, time.Second) {
		t.Error("a cancelled context must abort the sleep")
	}
}

func TestEntryJSONShape(t *testing.T) {
	t.Parallel()

	e := Entry{
		ID: 1, Spec: "0 4 * * *", JobID: 9, Type: TypeBackup, InstanceID: 3,
		Describe: "backup instance 3", Next: time.Now(), Running: false,
	}
	data, err := jsonMarshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, key := range []string{`"id"`, `"spec"`, `"jobId"`, `"type"`, `"instanceId"`, `"describe"`, `"next"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("JSON %s is missing %s", data, key)
		}
	}
}

func TestSchedulerUsesConfiguredLocation(t *testing.T) {
	t.Parallel()

	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("tzdata for Asia/Shanghai is unavailable: %v", err)
	}

	s := New(Options{Logger: testLogger(), Location: loc})
	if _, err := s.Add("0 4 * * *", &fakeJob{typ: TypeBackup}, 0); err != nil {
		t.Fatalf("Add: %v", err)
	}
	s.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Stop(ctx)
	})

	waitFor(t, 5*time.Second, "cron to schedule the entry", func() bool {
		entries := s.Entries()
		return len(entries) == 1 && !entries[0].Next.IsZero()
	})

	e := s.Entries()[0]
	if e.Next.Location().String() != loc.String() {
		t.Errorf("Next location = %v, want %v", e.Next.Location(), loc)
	}
	if e.Next.Hour() != 4 {
		t.Errorf("Next hour = %d, want 4 in the configured location", e.Next.Hour())
	}
}

// TestManyJobsFire checks the scheduler scales past a handful of entries
// without the overlap guard producing false positives across *different* jobs.
func TestManyJobsFire(t *testing.T) {
	t.Parallel()

	s := New(Options{Logger: testLogger(), RunTimeout: 5 * time.Second})

	jobs := make([]*fakeJob, 4)
	for i := range jobs {
		jobs[i] = &fakeJob{typ: TypeCommand, instanceID: int64(i)}
		if _, err := s.Add("@every 1s", jobs[i], 0); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	s.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.Stop(ctx)
	})

	waitFor(t, 20*time.Second, "all four distinct jobs to fire", func() bool {
		for _, j := range jobs {
			if j.calls.Load() == 0 {
				return false
			}
		}
		return true
	})

	for i, j := range jobs {
		if got := j.maxSeen.Load(); got > 1 {
			t.Errorf("job %d saw %d concurrent runs, want at most 1", i, got)
		}
	}
	_ = fmt.Sprintf // keep fmt imported for error messages above
}

// jsonMarshal avoids importing encoding/json just for one assertion.
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
