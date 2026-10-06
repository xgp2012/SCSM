package api

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"scnetm/internal/auth"
)

// This file contains the in-memory test doubles used by the API test suite.
//
// They implement the *production* interfaces (UserStore, InstanceStore, ...) so
// the tests exercise the same code paths the real store will, with no database,
// no network and no game server. They also record what was called, which is how
// the "every mutating route writes an audit row" assertions work.

// ---------------------------------------------------------------------------
// fakeUserStore
// ---------------------------------------------------------------------------

type fakeUserStore struct {
	mu     sync.Mutex
	users  map[int64]*User
	nextID int64
	// calls counts method invocations, for assertions.
	calls map[string]int
	// failNext forces the next call of a method to return this error.
	failNext map[string]error
}

func newFakeUserStore() *fakeUserStore {
	return &fakeUserStore{
		users:    make(map[int64]*User),
		nextID:   1,
		calls:    make(map[string]int),
		failNext: make(map[string]error),
	}
}

// seedAdmin inserts the first-run administrator row, mimicking store.Migrate:
// id=1, role=admin, password_hash = the first-run marker.
func (f *fakeUserStore) seedAdmin() *User {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := &User{
		ID:           1,
		Username:     "admin",
		PasswordHash: FirstRunPasswordMarker,
		Role:         auth.RoleAdmin,
		CreatedAt:    time.Now().UTC(),
	}
	f.users[1] = u
	f.nextID = 2
	return u
}

// FirstRunPasswordMarker mirrors internal/store's sentinel so the fake exercises
// the same first-run detection path as production.
const FirstRunPasswordMarker = "!first-run-password-not-set"

func (f *fakeUserStore) add(u *User) *User {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u.ID == 0 {
		u.ID = f.nextID
		f.nextID++
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now().UTC()
	}
	cp := *u
	f.users[u.ID] = &cp
	return &cp
}

func (f *fakeUserStore) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

func (f *fakeUserStore) bump(method string) error {
	f.calls[method]++
	if err, ok := f.failNext[method]; ok {
		delete(f.failNext, method)
		return err
	}
	return nil
}

func (f *fakeUserStore) GetByUsername(_ context.Context, username string) (*User, error) {
	if err := f.bump("GetByUsername"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if strings.EqualFold(u.Username, username) {
			cp := *u
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("%w: user %q", ErrNotFound, username)
}

func (f *fakeUserStore) GetByID(_ context.Context, id int64) (*User, error) {
	if err := f.bump("GetByID"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok {
		return nil, fmt.Errorf("%w: user %d", ErrNotFound, id)
	}
	cp := *u
	return &cp, nil
}

func (f *fakeUserStore) List(context.Context) ([]User, error) {
	if err := f.bump("List"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]User, 0, len(f.users))
	for _, u := range f.users {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeUserStore) Create(_ context.Context, u *User) error {
	if err := f.bump("Create"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.users {
		if strings.EqualFold(existing.Username, u.Username) {
			return fmt.Errorf("%w: username %q taken", ErrConflict, u.Username)
		}
	}
	if u.ID == 0 {
		u.ID = f.nextID
		f.nextID++
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now().UTC()
	}
	u.HasPassword = !isFirstRunHash(u.PasswordHash)
	cp := *u
	f.users[u.ID] = &cp
	return nil
}

func (f *fakeUserStore) Update(_ context.Context, u *User) error {
	if err := f.bump("Update"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.users[u.ID]; !ok {
		return fmt.Errorf("%w: user %d", ErrNotFound, u.ID)
	}
	for _, existing := range f.users {
		if existing.ID != u.ID && strings.EqualFold(existing.Username, u.Username) {
			return fmt.Errorf("%w: username %q taken", ErrConflict, u.Username)
		}
	}
	u.HasPassword = !isFirstRunHash(u.PasswordHash)
	cp := *u
	f.users[u.ID] = &cp
	return nil
}

func (f *fakeUserStore) UpdatePassword(_ context.Context, id int64, hash string) error {
	if err := f.bump("UpdatePassword"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok {
		return fmt.Errorf("%w: user %d", ErrNotFound, id)
	}
	u.PasswordHash = hash
	u.HasPassword = !isFirstRunHash(hash)
	return nil
}

// SetInitialPassword is the atomic first-run bootstrap. It only applies when the
// current hash is a first-run value, which is what makes POST /auth/setup
// one-shot even under concurrency.
func (f *fakeUserStore) SetInitialPassword(_ context.Context, id int64, hash string) error {
	if err := f.bump("SetInitialPassword"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok {
		return fmt.Errorf("%w: user %d", ErrNotFound, id)
	}
	if !isFirstRunHash(u.PasswordHash) {
		return fmt.Errorf("%w: password already set", ErrConflict)
	}
	u.PasswordHash = hash
	u.HasPassword = true
	return nil
}

func (f *fakeUserStore) TouchLastLogin(_ context.Context, id int64, at time.Time) error {
	if err := f.bump("TouchLastLogin"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.users[id]; ok {
		t := at
		u.LastLoginAt = &t
	}
	return nil
}

func (f *fakeUserStore) Delete(_ context.Context, id int64) error {
	if err := f.bump("Delete"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.users[id]; !ok {
		return fmt.Errorf("%w: user %d", ErrNotFound, id)
	}
	delete(f.users, id)
	return nil
}

func (f *fakeUserStore) Count(context.Context) (int, error) {
	if err := f.bump("Count"); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.users), nil
}

// ---------------------------------------------------------------------------
// fakeInstanceStore
// ---------------------------------------------------------------------------

type fakeInstanceStore struct {
	mu        sync.Mutex
	instances map[int64]*Instance
	states    map[int64]*InstanceState
	nextID    int64
	calls     map[string]int
	failNext  map[string]error
}

func newFakeInstanceStore() *fakeInstanceStore {
	return &fakeInstanceStore{
		instances: make(map[int64]*Instance),
		states:    make(map[int64]*InstanceState),
		nextID:    1,
		calls:     make(map[string]int),
		failNext:  make(map[string]error),
	}
}

func (f *fakeInstanceStore) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

func (f *fakeInstanceStore) bump(method string) error {
	f.calls[method]++
	if err, ok := f.failNext[method]; ok {
		delete(f.failNext, method)
		return err
	}
	return nil
}

func (f *fakeInstanceStore) List(_ context.Context, filter InstanceListFilter) ([]Instance, error) {
	if err := f.bump("List"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Instance, 0, len(f.instances))
	for _, in := range f.instances {
		if filter.OwnerID != 0 && in.OwnerID != filter.OwnerID {
			continue
		}
		out = append(out, *in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeInstanceStore) GetByID(_ context.Context, id int64) (*Instance, error) {
	if err := f.bump("GetByID"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	in, ok := f.instances[id]
	if !ok {
		return nil, fmt.Errorf("%w: instance %d", ErrNotFound, id)
	}
	cp := *in
	return &cp, nil
}

func (f *fakeInstanceStore) GetByName(_ context.Context, name string) (*Instance, error) {
	if err := f.bump("GetByName"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, in := range f.instances {
		if in.Name == name {
			cp := *in
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("%w: instance %q", ErrNotFound, name)
}

func (f *fakeInstanceStore) Create(_ context.Context, in *Instance) error {
	if err := f.bump("Create"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.instances {
		if existing.Name == in.Name {
			return fmt.Errorf("%w: instance name %q taken", ErrConflict, in.Name)
		}
	}
	if in.ID == 0 {
		in.ID = f.nextID
		f.nextID++
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now().UTC()
	}
	cp := *in
	f.instances[in.ID] = &cp
	return nil
}

func (f *fakeInstanceStore) Update(_ context.Context, in *Instance) error {
	if err := f.bump("Update"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.instances[in.ID]; !ok {
		return fmt.Errorf("%w: instance %d", ErrNotFound, in.ID)
	}
	for _, existing := range f.instances {
		if existing.ID != in.ID && existing.Name == in.Name {
			return fmt.Errorf("%w: instance name %q taken", ErrConflict, in.Name)
		}
	}
	cp := *in
	f.instances[in.ID] = &cp
	return nil
}

func (f *fakeInstanceStore) Delete(_ context.Context, id int64) error {
	if err := f.bump("Delete"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.instances[id]; !ok {
		return fmt.Errorf("%w: instance %d", ErrNotFound, id)
	}
	delete(f.instances, id)
	delete(f.states, id)
	return nil
}

func (f *fakeInstanceStore) ListUsedPorts(context.Context) ([]int, error) {
	if err := f.bump("ListUsedPorts"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int, 0, len(f.instances))
	for _, in := range f.instances {
		out = append(out, in.Port)
	}
	return out, nil
}

func (f *fakeInstanceStore) GetState(_ context.Context, id int64) (*InstanceState, error) {
	if err := f.bump("GetState"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.states[id]
	if !ok {
		return nil, fmt.Errorf("%w: state for instance %d", ErrNotFound, id)
	}
	cp := *st
	return &cp, nil
}

func (f *fakeInstanceStore) SaveState(_ context.Context, st *InstanceState) error {
	if err := f.bump("SaveState"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *st
	f.states[st.InstanceID] = &cp
	return nil
}

// ---------------------------------------------------------------------------
// fakeAuditStore — records every row for assertions
// ---------------------------------------------------------------------------

type fakeAuditStore struct {
	mu      sync.Mutex
	entries []AuditEntry
	calls   int
	// failWrite exercises the "audit failures must not fail the request" path.
	failWrite error
}

func newFakeAuditStore() *fakeAuditStore { return &fakeAuditStore{} }

func (f *fakeAuditStore) Write(_ context.Context, e *AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failWrite != nil {
		return f.failWrite
	}
	cp := *e
	if cp.ID == 0 {
		cp.ID = int64(len(f.entries) + 1)
	}
	if cp.Timestamp.IsZero() {
		cp.Timestamp = time.Now().UTC()
	}
	f.entries = append(f.entries, cp)
	return nil
}

func (f *fakeAuditStore) List(_ context.Context, filter AuditFilter) ([]AuditEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]AuditEntry, 0, len(f.entries))
	for _, e := range f.entries {
		if filter.UserID != 0 && e.UserID != filter.UserID {
			continue
		}
		if filter.Action != "" && e.Action != filter.Action {
			continue
		}
		if filter.InstanceID != 0 && !strings.Contains(e.Target, fmt.Sprintf("instance:%d", filter.InstanceID)) {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// rows returns a snapshot of the recorded rows.
func (f *fakeAuditStore) rows() []AuditEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]AuditEntry, len(f.entries))
	copy(out, f.entries)
	return out
}

// actions returns the recorded action names.
func (f *fakeAuditStore) actions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.entries))
	for _, e := range f.entries {
		out = append(out, e.Action)
	}
	return out
}

// hasAction reports whether any row has the given action.
func (f *fakeAuditStore) hasAction(action string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.entries {
		if e.Action == action {
			return true
		}
	}
	return false
}

// find returns the first row with the given action.
func (f *fakeAuditStore) find(action string) (AuditEntry, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.entries {
		if e.Action == action {
			return e, true
		}
	}
	return AuditEntry{}, false
}

func (f *fakeAuditStore) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = nil
	f.calls = 0
}

// ---------------------------------------------------------------------------
// fakeBackupStore / fakeJobStore
// ---------------------------------------------------------------------------

type fakeBackupStore struct {
	mu      sync.Mutex
	backups map[int64]*Backup
	nextID  int64
}

func newFakeBackupStore() *fakeBackupStore {
	return &fakeBackupStore{backups: make(map[int64]*Backup), nextID: 1}
}

func (f *fakeBackupStore) List(_ context.Context, filter BackupFilter) ([]Backup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Backup, 0, len(f.backups))
	for _, b := range f.backups {
		if filter.InstanceID != 0 && b.InstanceID != filter.InstanceID {
			continue
		}
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeBackupStore) GetByID(_ context.Context, id int64) (*Backup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.backups[id]
	if !ok {
		return nil, fmt.Errorf("%w: backup %d", ErrNotFound, id)
	}
	cp := *b
	return &cp, nil
}

func (f *fakeBackupStore) Create(_ context.Context, b *Backup) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b.ID == 0 {
		b.ID = f.nextID
		f.nextID++
	}
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Now().UTC()
	}
	cp := *b
	f.backups[b.ID] = &cp
	return nil
}

func (f *fakeBackupStore) Delete(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.backups[id]; !ok {
		return fmt.Errorf("%w: backup %d", ErrNotFound, id)
	}
	delete(f.backups, id)
	return nil
}

type fakeJobStore struct {
	mu     sync.Mutex
	jobs   map[int64]*Job
	nextID int64
}

func newFakeJobStore() *fakeJobStore {
	return &fakeJobStore{jobs: make(map[int64]*Job), nextID: 1}
}

func (f *fakeJobStore) List(context.Context) ([]Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Job, 0, len(f.jobs))
	for _, j := range f.jobs {
		out = append(out, *j)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeJobStore) GetByID(_ context.Context, id int64) (*Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok {
		return nil, fmt.Errorf("%w: job %d", ErrNotFound, id)
	}
	cp := *j
	return &cp, nil
}

func (f *fakeJobStore) Create(_ context.Context, j *Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if j.ID == 0 {
		j.ID = f.nextID
		f.nextID++
	}
	cp := *j
	f.jobs[j.ID] = &cp
	return nil
}

func (f *fakeJobStore) Update(_ context.Context, j *Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.jobs[j.ID]; !ok {
		return fmt.Errorf("%w: job %d", ErrNotFound, j.ID)
	}
	cp := *j
	f.jobs[j.ID] = &cp
	return nil
}

func (f *fakeJobStore) Delete(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.jobs[id]; !ok {
		return fmt.Errorf("%w: job %d", ErrNotFound, id)
	}
	delete(f.jobs, id)
	return nil
}

// ---------------------------------------------------------------------------
// fakeConfigService — an in-memory implementation of the §6.2 contract
// ---------------------------------------------------------------------------

// fakeConfigService implements ConfigService in memory, including the two
// behaviours the §6.2 tests assert: validate-before-write, and a backup taken
// before any overwrite.
type fakeConfigService struct {
	mu      sync.Mutex
	docs    map[string]*ConfigDoc
	backups []configBackup
	// validators lets a test inject a failure for a specific field.
	validators map[string]func(map[string]any) []ValidationIssue
	// writes counts successful writes.
	writes int
}

type configBackup struct {
	Filename string
	Content  []byte
}

func newFakeConfigService() *fakeConfigService {
	return &fakeConfigService{
		docs:       make(map[string]*ConfigDoc),
		validators: make(map[string]func(map[string]any) []ValidationIssue),
	}
}

func (f *fakeConfigService) key(inst *Instance, kind ConfigKind) string {
	return fmt.Sprintf("%d/%s", inst.ID, kind)
}

// seed installs a document without recording a backup.
func (f *fakeConfigService) seed(inst *Instance, kind ConfigKind, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.docs[f.key(inst, kind)] = &ConfigDoc{
		Kind:     kind,
		Filename: string(kind),
		Content:  []byte(content),
		ModTime:  time.Now().UTC(),
	}
}

func (f *fakeConfigService) Get(_ context.Context, inst *Instance, kind ConfigKind) (*ConfigDoc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	doc, ok := f.docs[f.key(inst, kind)]
	if !ok {
		return nil, fmt.Errorf("%w: config %s for instance %d", ErrNotFound, kind, inst.ID)
	}
	cp := *doc
	cp.Content = append([]byte(nil), doc.Content...)
	return &cp, nil
}

func (f *fakeConfigService) Write(_ context.Context, inst *Instance, upd ConfigUpdate) (*ConfigDoc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	key := f.key(inst, upd.Kind)
	// Take the backup BEFORE overwriting — the §6.2 requirement.
	if prev, ok := f.docs[key]; ok {
		f.backups = append(f.backups, configBackup{
			Filename: string(upd.Kind),
			Content:  append([]byte(nil), prev.Content...),
		})
	}

	doc := &ConfigDoc{
		Kind:     upd.Kind,
		Filename: string(upd.Kind),
		Content:  append([]byte(nil), upd.Content...),
		ModTime:  time.Now().UTC(),
	}
	f.docs[key] = doc
	f.writes++
	cp := *doc
	cp.Content = append([]byte(nil), doc.Content...)
	return &cp, nil
}

// Validate owns the §6.2 validator rules the tests exercise: the port range, the
// player cap, and the WorldPath suffix rule (no separators, no "..").
func (f *fakeConfigService) Validate(_ context.Context, _ *Instance, kind ConfigKind, content []byte) (*ValidationResult, error) {
	obj, err := decodeJSONObject(content)
	if err != nil {
		// A non-JSON document (Settings.xml) is pass-through: valid by default.
		return &ValidationResult{Valid: true}, nil
	}

	f.mu.Lock()
	validator := f.validators[string(kind)]
	f.mu.Unlock()

	var issues []ValidationIssue
	if validator != nil {
		issues = append(issues, validator(obj)...)
	}

	if port, ok := numberField(obj, "ServerPort"); ok {
		if port < 1024 || port > 65535 {
			issues = append(issues, ValidationIssue{
				Field:    "ServerPort",
				Code:     "port_out_of_range",
				Message:  fmt.Sprintf("ServerPort must be between 1024 and 65535, got %d", port),
				Severity: "error",
			})
		}
	}
	if maxPlayers, ok := numberField(obj, "MaxOnlinePlayerCount"); ok {
		if maxPlayers < 1 || maxPlayers > 255 {
			issues = append(issues, ValidationIssue{
				Field:    "MaxOnlinePlayerCount",
				Code:     "max_players_out_of_range",
				Message:  fmt.Sprintf("MaxOnlinePlayerCount must be between 1 and 255, got %d", maxPlayers),
				Severity: "error",
			})
		}
	}
	if wp, ok := obj["WorldPath"].(string); ok {
		name := strings.TrimPrefix(wp, "app:/Worlds/")
		if name == wp || name == "" {
			issues = append(issues, ValidationIssue{
				Field:    "WorldPath",
				Code:     "world_path_prefix",
				Message:  `WorldPath must look like "app:/Worlds/<dir>"`,
				Severity: "error",
			})
		} else if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
			issues = append(issues, ValidationIssue{
				Field:    "WorldPath",
				Code:     "world_dir_invalid",
				Message:  `the WorldPath suffix must not contain path separators or ".."`,
				Severity: "error",
			})
		}
	}
	if gm, ok := numberField(obj, "GameMode"); ok {
		if gm < 0 || gm > 6 {
			issues = append(issues, ValidationIssue{
				Field:    "GameMode",
				Code:     "game_mode_inferred_range",
				Message:  "GameMode must be between 0 and 6 (mapping is inferred, see §6.2.1)",
				Severity: "warning",
			})
		}
	}

	valid := true
	for _, i := range issues {
		if !strings.EqualFold(i.Severity, "warning") {
			valid = false
		}
	}
	return &ValidationResult{Valid: valid, Issues: issues}, nil
}

// backupCount reports how many pre-write backups were taken.
func (f *fakeConfigService) backupCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.backups)
}

func (f *fakeConfigService) currentContent(inst *Instance, kind ConfigKind) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	doc, ok := f.docs[f.key(inst, kind)]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), doc.Content...), true
}

// ---------------------------------------------------------------------------
// fakeWorldService / fakeFileService / fakeLogService / fakeBackupService
// ---------------------------------------------------------------------------

type fakeWorldService struct {
	mu     sync.Mutex
	worlds []WorldInfo
	// activateErr lets a test force the "refuse while running" path.
	activateErr error
	deleted     []string
	activated   []string
}

func newFakeWorldService() *fakeWorldService {
	return &fakeWorldService{
		worlds: []WorldInfo{
			{
				DirName:        "Alpha",
				DisplayName:    "Alpha World",
				Guid:           "guid-alpha",
				GameMode:       "Harmless",
				MaxPlayers:     20,
				SizeBytes:      1024,
				ModTime:        time.Now().UTC(),
				Active:         true,
				HasProjectJSON: true,
			},
			{
				DirName:        "Beta",
				DisplayName:    "Beta World",
				Guid:           "guid-beta",
				GameMode:       "Survival",
				MaxPlayers:     10,
				SizeBytes:      2048,
				ModTime:        time.Now().UTC(),
				HasProjectJSON: true,
			},
		},
	}
}

func (f *fakeWorldService) List(context.Context, *Instance) ([]WorldInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]WorldInfo(nil), f.worlds...), nil
}

func (f *fakeWorldService) Import(_ context.Context, _ *Instance, name string, _ []byte) (*WorldInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name == "" {
		name = "Imported"
	}
	w := WorldInfo{DirName: name, DisplayName: name, HasProjectJSON: false, ModTime: time.Now().UTC()}
	f.worlds = append(f.worlds, w)
	return &w, nil
}

func (f *fakeWorldService) Export(_ context.Context, _ *Instance, dirName string, _ bool) ([]byte, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, w := range f.worlds {
		if w.DirName == dirName {
			return []byte("PK\x03\x04fake-zip"), dirName + ".zip", nil
		}
	}
	return nil, "", fmt.Errorf("%w: world %q", ErrNotFound, dirName)
}

func (f *fakeWorldService) Backup(_ context.Context, inst *Instance, dirName, note string) (*Backup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, w := range f.worlds {
		if w.DirName == dirName {
			return &Backup{
				ID:         1,
				InstanceID: inst.ID,
				World:      dirName,
				Path:       fmt.Sprintf("/backups/%d/%s.zip", inst.ID, dirName),
				SizeBytes:  w.SizeBytes,
				Kind:       "manual",
				CreatedAt:  time.Now().UTC(),
				Note:       note,
			}, nil
		}
	}
	return nil, fmt.Errorf("%w: world %q", ErrNotFound, dirName)
}

func (f *fakeWorldService) Restore(_ context.Context, _ *Instance, dirName string, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, w := range f.worlds {
		if w.DirName == dirName {
			return nil
		}
	}
	return fmt.Errorf("%w: world %q", ErrNotFound, dirName)
}

func (f *fakeWorldService) Activate(_ context.Context, _ *Instance, dirName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.activateErr != nil {
		return f.activateErr
	}
	found := false
	for i := range f.worlds {
		f.worlds[i].Active = f.worlds[i].DirName == dirName
		if f.worlds[i].DirName == dirName {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("%w: world %q", ErrNotFound, dirName)
	}
	f.activated = append(f.activated, dirName)
	return nil
}

func (f *fakeWorldService) Delete(_ context.Context, _ *Instance, dirName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.worlds {
		if f.worlds[i].DirName == dirName {
			f.worlds = append(f.worlds[:i], f.worlds[i+1:]...)
			f.deleted = append(f.deleted, dirName)
			return nil
		}
	}
	return fmt.Errorf("%w: world %q", ErrNotFound, dirName)
}

// fakeFileService records the paths it was asked to touch, which is how the
// traversal tests prove a rejected path never reached the service layer.
type fakeFileService struct {
	mu     sync.Mutex
	seen   []string
	files  map[string][]byte
	listFn func(inst *Instance, rel string) ([]FileEntry, error)
}

func newFakeFileService() *fakeFileService {
	return &fakeFileService{files: make(map[string][]byte)}
}

func (f *fakeFileService) record(rel string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, rel)
}

func (f *fakeFileService) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func (f *fakeFileService) List(_ context.Context, _ *Instance, rel string) ([]FileEntry, error) {
	f.record(rel)
	if f.listFn != nil {
		return f.listFn(nil, rel)
	}
	return []FileEntry{
		{Name: "ServerSetting.json", Path: "ServerSetting.json", Size: 42, Mode: "-rw-r--r--", ModTime: time.Now().UTC()},
		{Name: "Worlds", Path: "Worlds", IsDir: true, Mode: "drwxr-xr-x", ModTime: time.Now().UTC()},
	}, nil
}

func (f *fakeFileService) Read(_ context.Context, _ *Instance, rel string) ([]byte, string, error) {
	f.record(rel)
	f.mu.Lock()
	defer f.mu.Unlock()
	if data, ok := f.files[rel]; ok {
		return data, "application/octet-stream", nil
	}
	return []byte("content of " + rel), "text/plain", nil
}

func (f *fakeFileService) Write(_ context.Context, _ *Instance, rel string, data []byte) error {
	f.record(rel)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[rel] = append([]byte(nil), data...)
	return nil
}

func (f *fakeFileService) Mkdir(_ context.Context, _ *Instance, rel string) error {
	f.record(rel)
	return nil
}

func (f *fakeFileService) Rename(_ context.Context, _ *Instance, from, to string) error {
	f.record(from)
	f.record(to)
	return nil
}

func (f *fakeFileService) Delete(_ context.Context, _ *Instance, rel string) error {
	f.record(rel)
	return nil
}

func (f *fakeFileService) Unzip(_ context.Context, _ *Instance, rel, dest string) error {
	f.record(rel)
	f.record(dest)
	return nil
}

func (f *fakeFileService) SaveUpload(_ context.Context, _ *Instance, rel string, data []byte) (*FileEntry, error) {
	f.record(rel)
	f.mu.Lock()
	f.files[rel] = append([]byte(nil), data...)
	f.mu.Unlock()
	return &FileEntry{
		Name:    filepathBase(rel),
		Path:    rel,
		Size:    int64(len(data)),
		Mode:    "-rw-r--r--",
		ModTime: time.Now().UTC(),
	}, nil
}

type fakeLogService struct {
	lines []string
	path  string
	// tailErr forces the 501 path when set to ErrNotImplemented.
	tailErr error
}

func newFakeLogService() *fakeLogService {
	return &fakeLogService{
		lines: []string{
			"[12:00:00] [Info] Survivalcraft server starting",
			"[12:00:01] [Info] Loading world, GameMode=Harmless",
			"[12:00:02] [Info] Loaded world, GameMode=Harmless",
			"[12:00:03] [Error] Something went wrong",
		},
		path: "/tmp/scnetm-test-console.log",
	}
}

func (f *fakeLogService) Tail(_ context.Context, _ *Instance, n int, grep string) ([]string, error) {
	if f.tailErr != nil {
		return nil, f.tailErr
	}
	lines := f.lines
	if grep != "" {
		filtered := make([]string, 0, len(lines))
		for _, l := range lines {
			if strings.Contains(l, grep) {
				filtered = append(filtered, l)
			}
		}
		lines = filtered
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return append([]string(nil), lines...), nil
}

func (f *fakeLogService) LogsPath(context.Context, *Instance) (string, error) {
	if f.tailErr != nil {
		return "", f.tailErr
	}
	return f.path, nil
}

type fakeBackupService struct {
	created int
}

func (f *fakeBackupService) Create(_ context.Context, inst *Instance, kind, note string) (*Backup, error) {
	f.created++
	return &Backup{
		ID:         int64(f.created),
		InstanceID: inst.ID,
		Path:       fmt.Sprintf("/backups/%d/manual-%d.zip", inst.ID, f.created),
		SizeBytes:  4096,
		Kind:       kind,
		CreatedAt:  time.Now().UTC(),
		Note:       note,
	}, nil
}

func (f *fakeBackupService) Restore(context.Context, *Instance, int64) error { return nil }

// fakeSystemService lets a test control the /system/info payload, in particular
// the .NET detection result.
type fakeSystemService struct {
	info *SystemInfo
}

func (f *fakeSystemService) Info(context.Context) (*SystemInfo, error) { return f.info, nil }

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

func decodeJSONObject(data []byte) (map[string]any, error) {
	var obj map[string]any
	if err := jsonUnmarshal(stripBOM(data), &obj); err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, errors.New("not a JSON object")
	}
	return obj, nil
}

func numberField(obj map[string]any, key string) (int, bool) {
	v, ok := obj[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	default:
		return 0, false
	}
}

func filepathBase(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
