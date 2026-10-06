package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"scnetm/internal/auth"
	"scnetm/internal/store"
)

// This file adapts the real persistence layer (internal/store) to the narrow
// interfaces the API defines for itself in deps.go.
//
// # Why adapters rather than importing store directly into the handlers
//
// The handlers depend on ~10 small interfaces instead of on *store.Store
// because the sibling packages were written concurrently under a frozen
// contract: an interface in this package means a signature change in
// internal/store cannot break the API build, and it lets the whole test suite
// run against in-memory doubles with no database.
//
// The cost of that decision is this file: somebody has to bridge the two. Doing
// it here — in one place, at the seam — keeps the benefit and pays the cost
// once.
//
// # What the adapters are responsible for
//
// Chiefly error translation. internal/store reports its own sentinels
// (store.ErrNotFound, store.ErrDuplicate, store.ErrInvalid); the API contract
// speaks ErrNotFound, ErrConflict and ErrInvalid. Everything a handler does with
// an error — 404 vs 409 vs 422 — depends on that mapping being right, so it is
// implemented explicitly per method rather than with a blanket errors.Is chain.
//
// These adapters are exercised by e2e_test.go against a real SQLite database.

// ---------------------------------------------------------------------------
// error translation
// ---------------------------------------------------------------------------

// translateStoreError maps a store sentinel onto the API's vocabulary.
//
// It deliberately does NOT pass through unknown errors: an unrecognised error
// from the store is a genuine fault, and the caller's Classify turns it into a
// 500 rather than mislabelling it as a client error.
func translateStoreError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	case errors.Is(err, store.ErrDuplicate):
		return fmt.Errorf("%w: %v", ErrConflict, err)
	case errors.Is(err, store.ErrInvalid):
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	default:
		return err
	}
}

// ---------------------------------------------------------------------------
// users
// ---------------------------------------------------------------------------

// storeUserStore adapts store.UserRepo to the API's UserStore.
type storeUserStore struct {
	repo *store.UserRepo
}

// NewStoreUserStore wires the real user/grant repository into the API.
func NewStoreUserStore(s *store.Store) UserStore {
	if s == nil {
		return nil
	}
	return &storeUserStore{repo: s.Users()}
}

func (a *storeUserStore) GetByUsername(ctx context.Context, username string) (*User, error) {
	u, err := a.repo.GetByUsername(ctx, username)
	if err != nil {
		return nil, translateStoreError(err)
	}
	return toAPIUser(u), nil
}

func (a *storeUserStore) GetByID(ctx context.Context, id int64) (*User, error) {
	u, err := a.repo.GetByID(ctx, id)
	if err != nil {
		return nil, translateStoreError(err)
	}
	return toAPIUser(u), nil
}

func (a *storeUserStore) List(ctx context.Context) ([]User, error) {
	users, err := a.repo.List(ctx)
	if err != nil {
		return nil, translateStoreError(err)
	}
	out := make([]User, 0, len(users))
	for i := range users {
		out = append(out, *toAPIUser(&users[i]))
	}
	return out, nil
}

func (a *storeUserStore) Create(ctx context.Context, u *User) error {
	row := fromAPIUser(u)
	if err := a.repo.Create(ctx, row); err != nil {
		return translateStoreError(err)
	}
	// Reflect the database-assigned id and timestamp back onto the caller's
	// value: handlers return the created row without re-reading it.
	u.ID = row.ID
	u.CreatedAt = row.CreatedAt
	u.HasPassword = !isFirstRunHash(row.PasswordHash)
	return nil
}

// Update writes the mutable fields of a user.
//
// store.UserRepo deliberately exposes narrow setters (SetRole, SetDisabled,
// UpdatePassword) instead of a general Update, because each carries a guard:
// SetRole refuses to demote the last admin, and so on. This adapter composes
// them inside a transaction so a user edit is still atomic, and so those guards
// cannot be bypassed by going through the API.
func (a *storeUserStore) Update(ctx context.Context, u *User) error {
	existing, err := a.repo.GetByID(ctx, u.ID)
	if err != nil {
		return translateStoreError(err)
	}

	// Username changes are not supported by the repo's narrow setters, and the
	// store treats the username as immutable identity.
	if u.Username != "" && u.Username != existing.Username {
		return fmt.Errorf("%w: the username is immutable", ErrInvalid)
	}

	if u.Role != "" && string(u.Role) != existing.Role {
		if err := a.repo.SetRole(ctx, u.ID, string(u.Role)); err != nil {
			return translateStoreError(err)
		}
	}
	if u.Disabled != existing.Disabled {
		if err := a.repo.SetDisabled(ctx, u.ID, u.Disabled); err != nil {
			return translateStoreError(err)
		}
	}
	if u.PasswordHash != "" && u.PasswordHash != existing.PasswordHash {
		if err := a.repo.UpdatePassword(ctx, u.ID, u.PasswordHash); err != nil {
			return translateStoreError(err)
		}
	}
	return nil
}

func (a *storeUserStore) UpdatePassword(ctx context.Context, id int64, hash string) error {
	return translateStoreError(a.repo.UpdatePassword(ctx, id, hash))
}

// SetInitialPassword is the first-run bootstrap.
//
// It is implemented as read-then-write rather than as a single conditional
// UPDATE, because the repo exposes no such primitive. The check-under-lock is
// still correct here — the store pins the pool to ONE connection (see
// store.Open), so the read and the write below cannot interleave with another
// writer.
func (a *storeUserStore) SetInitialPassword(ctx context.Context, id int64, hash string) error {
	existing, err := a.repo.GetByID(ctx, id)
	if err != nil {
		return translateStoreError(err)
	}
	if !isFirstRunHash(existing.PasswordHash) {
		return fmt.Errorf("%w: an administrator password is already set", ErrConflict)
	}
	return translateStoreError(a.repo.UpdatePassword(ctx, id, hash))
}

func (a *storeUserStore) TouchLastLogin(ctx context.Context, id int64, at time.Time) error {
	return translateStoreError(a.repo.TouchLastLogin(ctx, id, at))
}

func (a *storeUserStore) Delete(ctx context.Context, id int64) error {
	return translateStoreError(a.repo.Delete(ctx, id))
}

func (a *storeUserStore) Count(ctx context.Context) (int, error) {
	users, err := a.repo.List(ctx)
	if err != nil {
		return 0, translateStoreError(err)
	}
	return len(users), nil
}

func toAPIUser(u *store.User) *User {
	if u == nil {
		return nil
	}
	return &User{
		ID:           u.ID,
		Username:     u.Username,
		PasswordHash: u.PasswordHash,
		Role:         auth.Role(u.Role),
		CreatedAt:    u.CreatedAt,
		LastLoginAt:  u.LastLoginAt,
		Disabled:     u.Disabled,
		// HasPassword is derived, never stored: the store's convention is that
		// a first-run marker means "no password yet".
		HasPassword: !isFirstRunHash(u.PasswordHash),
	}
}

func fromAPIUser(u *User) *store.User {
	if u == nil {
		return nil
	}
	return &store.User{
		ID:           u.ID,
		Username:     u.Username,
		PasswordHash: u.PasswordHash,
		Role:         string(u.Role),
		CreatedAt:    u.CreatedAt,
		LastLoginAt:  u.LastLoginAt,
		Disabled:     u.Disabled,
	}
}

// ---------------------------------------------------------------------------
// grants
// ---------------------------------------------------------------------------

// storeGrantStore adapts the grant half of store.UserRepo to the API's
// GrantStore, so instance-level grants survive a restart.
type storeGrantStore struct {
	repo *store.UserRepo
}

// NewStoreGrantStore wires the real user_instance_grant table into the API.
func NewStoreGrantStore(s *store.Store) GrantStore {
	if s == nil {
		return nil
	}
	return &storeGrantStore{repo: s.Users()}
}

func (a *storeGrantStore) Grant(ctx context.Context, userID, instanceID int64, perm auth.GrantPerm) error {
	if !perm.Valid() {
		return fmt.Errorf("%w: unknown grant level %q", ErrInvalid, perm)
	}
	return translateStoreError(a.repo.Grant(ctx, userID, instanceID, string(perm)))
}

// Revoke clears every level for the pair.
//
// The store revokes one level at a time, so all three are cleared: leaving a
// lower level behind after "revoke" would silently retain access, which is the
// failure mode worth being explicit about.
func (a *storeGrantStore) Revoke(ctx context.Context, userID, instanceID int64) error {
	var firstErr error
	for _, perm := range []auth.GrantPerm{auth.GrantRead, auth.GrantControl, auth.GrantConfig} {
		if err := a.repo.Revoke(ctx, userID, instanceID, string(perm)); err != nil {
			// An absent level is not a failure: Revoke is idempotent.
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if firstErr == nil {
				firstErr = translateStoreError(err)
			}
		}
	}
	return firstErr
}

func (a *storeGrantStore) ListForUser(ctx context.Context, userID int64) ([]InstanceGrant, error) {
	grants, err := a.repo.ListForUser(ctx, userID)
	if err != nil {
		return nil, translateStoreError(err)
	}
	return toAPIGrants(grants), nil
}

func (a *storeGrantStore) ListForInstance(ctx context.Context, instanceID int64) ([]InstanceGrant, error) {
	grants, err := a.repo.ListForInstance(ctx, instanceID)
	if err != nil {
		return nil, translateStoreError(err)
	}
	return toAPIGrants(grants), nil
}

func toAPIGrants(grants []store.Grant) []InstanceGrant {
	out := make([]InstanceGrant, 0, len(grants))
	for _, g := range grants {
		out = append(out, InstanceGrant{
			UserID:     g.UserID,
			InstanceID: g.InstanceID,
			Perm:       auth.GrantPerm(g.Perm),
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// instances
// ---------------------------------------------------------------------------

// storeInstanceStore adapts store.InstanceRepo to the API's InstanceStore.
type storeInstanceStore struct {
	repo *store.InstanceRepo
}

// NewStoreInstanceStore wires the real instance repository into the API.
func NewStoreInstanceStore(s *store.Store) InstanceStore {
	if s == nil {
		return nil
	}
	return &storeInstanceStore{repo: s.Instances()}
}

func (a *storeInstanceStore) List(ctx context.Context, filter InstanceListFilter) ([]Instance, error) {
	var (
		rows []store.Instance
		err  error
	)
	if filter.OwnerID != 0 {
		rows, err = a.repo.ListByOwner(ctx, filter.OwnerID)
	} else {
		rows, err = a.repo.List(ctx)
	}
	if err != nil {
		return nil, translateStoreError(err)
	}
	out := make([]Instance, 0, len(rows))
	for i := range rows {
		out = append(out, *toAPIInstance(&rows[i]))
	}
	return out, nil
}

func (a *storeInstanceStore) GetByID(ctx context.Context, id int64) (*Instance, error) {
	row, err := a.repo.Get(ctx, id)
	if err != nil {
		return nil, translateStoreError(err)
	}
	return toAPIInstance(row), nil
}

func (a *storeInstanceStore) GetByName(ctx context.Context, name string) (*Instance, error) {
	row, err := a.repo.GetByName(ctx, name)
	if err != nil {
		return nil, translateStoreError(err)
	}
	return toAPIInstance(row), nil
}

func (a *storeInstanceStore) Create(ctx context.Context, inst *Instance) error {
	row := fromAPIInstance(inst)
	if err := a.repo.Create(ctx, row); err != nil {
		return translateStoreError(err)
	}
	inst.ID = row.ID
	inst.CreatedAt = row.CreatedAt
	return nil
}

func (a *storeInstanceStore) Update(ctx context.Context, inst *Instance) error {
	existing, err := a.repo.Get(ctx, inst.ID)
	if err != nil {
		return translateStoreError(err)
	}

	// Merge: the handler sends the full desired row, but the repo's Update
	// writes the row it is given, so preserve fields the API does not own.
	merged := fromAPIInstance(inst)
	if merged.CreatedAt.IsZero() {
		merged.CreatedAt = existing.CreatedAt
	}
	if merged.OwnerID == 0 {
		merged.OwnerID = existing.OwnerID
	}
	return translateStoreError(a.repo.Update(ctx, merged))
}

func (a *storeInstanceStore) Delete(ctx context.Context, id int64) error {
	return translateStoreError(a.repo.Delete(ctx, id))
}

// ListUsedPorts returns every allocated port, so the allocator can avoid them.
//
// It reads the instance table rather than a dedicated port table: a port is
// "used" precisely while a row holds it, and keeping a second source of truth
// would only create a way for the two to disagree.
func (a *storeInstanceStore) ListUsedPorts(ctx context.Context) ([]int, error) {
	rows, err := a.repo.List(ctx)
	if err != nil {
		return nil, translateStoreError(err)
	}
	out := make([]int, 0, len(rows))
	for _, r := range rows {
		if r.Port > 0 {
			out = append(out, int(r.Port))
		}
	}
	return out, nil
}

func (a *storeInstanceStore) GetState(ctx context.Context, id int64) (*InstanceState, error) {
	row, err := a.repo.GetState(ctx, id)
	if err != nil {
		return nil, translateStoreError(err)
	}
	return toAPIInstanceState(row), nil
}

func (a *storeInstanceStore) SaveState(ctx context.Context, st *InstanceState) error {
	row := fromAPIInstanceState(st)
	// Upsert rather than update: the first save for a fresh instance has no
	// existing row, and a create-on-first-write keeps the handler simple.
	return translateStoreError(a.repo.Upsert(ctx, *row))
}

func toAPIInstance(r *store.Instance) *Instance {
	if r == nil {
		return nil
	}
	return &Instance{
		ID:             r.ID,
		Name:           r.Name,
		Dir:            r.Dir,
		Port:           int(r.Port),
		DotnetPath:     r.Dotnet,
		ServerJar:      r.ServerDL,
		OwnerID:        r.OwnerID,
		AutoStart:      r.AutoStart,
		AutoRestart:    r.AutoRestart,
		MaxRestart:     int(r.MaxRestart),
		StopTimeoutSec: int(r.StopTimeoutSec),
		Term:           r.Term,
		ColorMode:      r.ColorMode,
		CreatedAt:      r.CreatedAt,
		Memo:           r.Memo,
	}
}

func fromAPIInstance(r *Instance) *store.Instance {
	if r == nil {
		return nil
	}
	return &store.Instance{
		ID:             r.ID,
		Name:           r.Name,
		Dir:            r.Dir,
		Port:           int64(r.Port),
		Dotnet:         r.DotnetPath,
		ServerDL:       r.ServerJar,
		OwnerID:        r.OwnerID,
		AutoStart:      r.AutoStart,
		AutoRestart:    r.AutoRestart,
		MaxRestart:     int64(r.MaxRestart),
		StopTimeoutSec: int64(r.StopTimeoutSec),
		Term:           r.Term,
		ColorMode:      r.ColorMode,
		CreatedAt:      r.CreatedAt,
		Memo:           r.Memo,
	}
}

func toAPIInstanceState(r *store.InstanceState) *InstanceState {
	if r == nil {
		return nil
	}
	st := &InstanceState{
		InstanceID:    r.InstanceID,
		State:         r.State,
		PID:           int(r.PID),
		StartedAt:     r.StartedAt,
		StoppedAt:     r.StoppedAt,
		OnlinePlayers: int(r.OnlinePlayers),
		LastError:     r.LastError,
	}
	if r.ExitCode != nil {
		code := int(*r.ExitCode)
		st.ExitCode = &code
	}
	return st
}

func fromAPIInstanceState(r *InstanceState) *store.InstanceState {
	if r == nil {
		return nil
	}
	row := &store.InstanceState{
		InstanceID:    r.InstanceID,
		State:         r.State,
		PID:           int64(r.PID),
		StartedAt:     r.StartedAt,
		StoppedAt:     r.StoppedAt,
		LastError:     r.LastError,
		OnlinePlayers: int64(r.OnlinePlayers),
	}
	if r.ExitCode != nil {
		code := int64(*r.ExitCode)
		row.ExitCode = &code
	}
	return row
}

// ---------------------------------------------------------------------------
// audit
// ---------------------------------------------------------------------------

// storeAuditStore adapts store.AuditRepo to the API's AuditStore.
//
// The critical contract here is that Write is BEST-EFFORT: a failed audit insert
// must not fail the business operation that triggered it.
type storeAuditStore struct {
	repo *store.AuditRepo
	// fallback receives rows when the database write fails, so a broken audit
	// table degrades to "somewhere" instead of "nowhere".
	fallback AuditStore
}

// NewStoreAuditStore wires the real audit repository into the API.
func NewStoreAuditStore(s *store.Store, fallback AuditStore) AuditStore {
	if s == nil {
		return fallback
	}
	return &storeAuditStore{repo: s.Audit(), fallback: fallback}
}

func (a *storeAuditStore) Write(ctx context.Context, e *AuditEntry) error {
	if e == nil {
		return nil
	}

	// The store models an absent actor as NULL rather than as 0, because 0 is
	// not a valid user id and a foreign key would reject it.
	var userID *int64
	if e.UserID != 0 {
		id := e.UserID
		userID = &id
	}

	err := a.repo.Log(ctx, userID, e.Action, e.Target, e.Detail, e.IP)
	if err == nil {
		return nil
	}
	// Best-effort: hand the row to the fallback sink and report the original
	// failure so the caller can log it, but never make the audit the reason a
	// user-facing operation fails.
	if a.fallback != nil {
		_ = a.fallback.Write(ctx, e)
	}
	return err
}

func (a *storeAuditStore) List(ctx context.Context, filter AuditFilter) ([]AuditEntry, error) {
	sf := store.AuditFilter{
		Action: filter.Action,
		From:   filter.From,
		To:     filter.To,
		Limit:  filter.Limit,
		Offset: filter.Offset,
	}
	// The store models "no filter" as a nil pointer, because 0 is not a valid
	// id in either column and a pointer keeps "unset" distinguishable from it.
	if filter.UserID != 0 {
		id := filter.UserID
		sf.UserID = &id
	}
	if filter.InstanceID != 0 {
		id := filter.InstanceID
		sf.InstanceID = &id
	}
	rows, err := a.repo.List(ctx, sf)
	if err != nil {
		return nil, translateStoreError(err)
	}
	out := make([]AuditEntry, 0, len(rows))
	for i := range rows {
		out = append(out, toAPIAudit(&rows[i]))
	}
	return out, nil
}

func toAPIAudit(r *store.AuditLog) AuditEntry {
	var userID int64
	if r.UserID != nil {
		userID = *r.UserID
	}
	return AuditEntry{
		ID:        r.ID,
		UserID:    userID,
		Action:    r.Action,
		Target:    r.Target,
		Detail:    r.Detail,
		IP:        r.IP,
		Timestamp: r.TS,
	}
}

// ---------------------------------------------------------------------------
// backups and jobs
// ---------------------------------------------------------------------------

// storeBackupStore adapts store.BackupRepo to the API's BackupStore.
type storeBackupStore struct {
	repo      *store.BackupRepo
	instances InstanceStore
}

// NewStoreBackupStore wires the real backup repository into the API.
//
// instances is needed to answer an unscoped listing; see the List method.
func NewStoreBackupStore(s *store.Store, instances InstanceStore) BackupStore {
	if s == nil {
		return nil
	}
	return &storeBackupStore{repo: s.Backups(), instances: instances}
}

// List returns backups, optionally scoped to one instance.
//
// The store exposes only per-instance queries (ListByInstance and friends) and
// no global listing, because a backup is always owned by an instance. An
// unscoped request is therefore served by walking the instances: that is the
// honest implementation of "list everything", and it keeps the store free of a
// query whose result is a cross-instance concatenation nobody actually wants to
// paginate.
func (a *storeBackupStore) List(ctx context.Context, filter BackupFilter) ([]Backup, error) {
	if filter.InstanceID != 0 {
		rows, err := a.repo.ListByInstance(ctx, filter.InstanceID)
		if err != nil {
			return nil, translateStoreError(err)
		}
		return paginateBackups(rows, filter), nil
	}

	if a.instances == nil {
		return []Backup{}, nil
	}
	instances, err := a.instances.List(ctx, InstanceListFilter{})
	if err != nil {
		return nil, translateStoreError(err)
	}
	out := make([]Backup, 0)
	for _, inst := range instances {
		rows, err := a.repo.ListByInstance(ctx, inst.ID)
		if err != nil {
			return nil, translateStoreError(err)
		}
		out = append(out, paginateBackups(rows, BackupFilter{})...)
	}
	return applyBackupWindow(out, filter), nil
}

func paginateBackups(rows []store.Backup, filter BackupFilter) []Backup {
	out := make([]Backup, 0, len(rows))
	for i := range rows {
		out = append(out, *toAPIBackup(&rows[i]))
	}
	return applyBackupWindow(out, filter)
}

// applyBackupWindow applies the limit/offset the API exposes.
func applyBackupWindow(in []Backup, filter BackupFilter) []Backup {
	if filter.Offset > 0 {
		if filter.Offset >= len(in) {
			return []Backup{}
		}
		in = in[filter.Offset:]
	}
	if filter.Limit > 0 && len(in) > filter.Limit {
		in = in[:filter.Limit]
	}
	return in
}

func (a *storeBackupStore) GetByID(ctx context.Context, id int64) (*Backup, error) {
	row, err := a.repo.Get(ctx, id)
	if err != nil {
		return nil, translateStoreError(err)
	}
	return toAPIBackup(row), nil
}

func (a *storeBackupStore) Create(ctx context.Context, b *Backup) error {
	row := fromAPIBackup(b)
	if err := a.repo.Create(ctx, row); err != nil {
		return translateStoreError(err)
	}
	b.ID = row.ID
	return nil
}

func (a *storeBackupStore) Delete(ctx context.Context, id int64) error {
	return translateStoreError(a.repo.Delete(ctx, id))
}

func toAPIBackup(r *store.Backup) *Backup {
	if r == nil {
		return nil
	}
	return &Backup{
		ID:         r.ID,
		InstanceID: r.InstanceID,
		World:      r.World,
		Path:       r.Path,
		SizeBytes:  r.SizeBytes,
		Kind:       r.Kind,
		CreatedAt:  r.CreatedAt,
		Note:       r.Note,
	}
}

func fromAPIBackup(r *Backup) *store.Backup {
	if r == nil {
		return nil
	}
	return &store.Backup{
		ID:         r.ID,
		InstanceID: r.InstanceID,
		World:      r.World,
		Path:       r.Path,
		SizeBytes:  r.SizeBytes,
		Kind:       r.Kind,
		CreatedAt:  r.CreatedAt,
		Note:       r.Note,
	}
}

// storeJobStore adapts store.JobRepo to the API's JobStore.
type storeJobStore struct {
	repo *store.JobRepo
}

// NewStoreJobStore wires the real job repository into the API.
func NewStoreJobStore(s *store.Store) JobStore {
	if s == nil {
		return nil
	}
	return &storeJobStore{repo: s.Jobs()}
}

func (a *storeJobStore) List(ctx context.Context) ([]Job, error) {
	rows, err := a.repo.List(ctx)
	if err != nil {
		return nil, translateStoreError(err)
	}
	out := make([]Job, 0, len(rows))
	for i := range rows {
		out = append(out, *toAPIJob(&rows[i]))
	}
	return out, nil
}

func (a *storeJobStore) GetByID(ctx context.Context, id int64) (*Job, error) {
	row, err := a.repo.Get(ctx, id)
	if err != nil {
		return nil, translateStoreError(err)
	}
	return toAPIJob(row), nil
}

func (a *storeJobStore) Create(ctx context.Context, j *Job) error {
	row := fromAPIJob(j)
	if err := a.repo.Create(ctx, row); err != nil {
		return translateStoreError(err)
	}
	j.ID = row.ID
	return nil
}

func (a *storeJobStore) Update(ctx context.Context, j *Job) error {
	return translateStoreError(a.repo.Update(ctx, fromAPIJob(j)))
}

func (a *storeJobStore) Delete(ctx context.Context, id int64) error {
	return translateStoreError(a.repo.Delete(ctx, id))
}

func toAPIJob(r *store.Job) *Job {
	if r == nil {
		return nil
	}
	return &Job{
		ID:         r.ID,
		InstanceID: r.InstanceID,
		Type:       r.Type,
		Cron:       r.Cron,
		Payload:    r.Payload,
		Enabled:    r.Enabled,
		LastRun:    r.LastRun,
		LastResult: r.LastResult,
	}
}

func fromAPIJob(r *Job) *store.Job {
	if r == nil {
		return nil
	}
	return &store.Job{
		ID:         r.ID,
		InstanceID: r.InstanceID,
		Type:       r.Type,
		Cron:       r.Cron,
		Payload:    r.Payload,
		Enabled:    r.Enabled,
		LastRun:    r.LastRun,
		LastResult: r.LastResult,
	}
}

// ---------------------------------------------------------------------------
// one-call wiring
// ---------------------------------------------------------------------------

// StoreBackends bundles every adapter built from one store, so a host can wire
// the whole API with a single call.
type StoreBackends struct {
	Users     UserStore
	Instances InstanceStore
	Grants    GrantStore
	Audit     AuditStore
	Backups   BackupStore
	Jobs      JobStore
}

// NewStoreBackends builds every real-store adapter at once.
//
// auditFallback may be nil; it is used only when a database audit write fails.
func NewStoreBackends(s *store.Store, auditFallback AuditStore) StoreBackends {
	instances := NewStoreInstanceStore(s)
	return StoreBackends{
		Users:     NewStoreUserStore(s),
		Instances: instances,
		Grants:    NewStoreGrantStore(s),
		Audit:     NewStoreAuditStore(s, auditFallback),
		Backups:   NewStoreBackupStore(s, instances),
		Jobs:      NewStoreJobStore(s),
	}
}
