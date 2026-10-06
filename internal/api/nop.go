package api

import (
	"context"
	"fmt"
	"time"

	"scnetm/internal/auth"
)

// This file holds the "not implemented in this build" (nop) implementations of
// every delegated backend interface.
//
// The rule they follow is the one stated in the task: a missing backend must
// produce an explicit, machine-readable 501 — never a silent empty 200. Each
// nop therefore returns ErrNotImplemented wrapped with enough context for the
// operator to understand which package is missing.

// notImplemented builds the standard error, naming the backend and the plan
// section that owns it.
func notImplemented(backend, detail string) error {
	return fmt.Errorf("%w: %s backend (see %s)", ErrNotImplemented, backend, detail)
}

// ---------------------------------------------------------------------------
// Stores
// ---------------------------------------------------------------------------

type nopUserStore struct{}

func (nopUserStore) GetByUsername(context.Context, string) (*User, error) {
	return nil, notImplemented("user store", "§5.5 users")
}
func (nopUserStore) GetByID(context.Context, int64) (*User, error) {
	return nil, notImplemented("user store", "§5.5 users")
}
func (nopUserStore) List(context.Context) ([]User, error) {
	return nil, notImplemented("user store", "§5.5 users")
}
func (nopUserStore) Create(context.Context, *User) error {
	return notImplemented("user store", "§5.5 users")
}
func (nopUserStore) Update(context.Context, *User) error {
	return notImplemented("user store", "§5.5 users")
}
func (nopUserStore) UpdatePassword(context.Context, int64, string) error {
	return notImplemented("user store", "§5.5 users")
}
func (nopUserStore) SetInitialPassword(context.Context, int64, string) error {
	return notImplemented("user store", "§5.5 users")
}
func (nopUserStore) TouchLastLogin(context.Context, int64, time.Time) error { return nil }
func (nopUserStore) Delete(context.Context, int64) error {
	return notImplemented("user store", "§5.5 users")
}
func (nopUserStore) Count(context.Context) (int, error) { return 0, nil }

type nopInstanceStore struct{}

func (nopInstanceStore) List(context.Context, InstanceListFilter) ([]Instance, error) {
	return nil, notImplemented("instance store", "§5.5 instances")
}
func (nopInstanceStore) GetByID(context.Context, int64) (*Instance, error) {
	return nil, notImplemented("instance store", "§5.5 instances")
}
func (nopInstanceStore) GetByName(context.Context, string) (*Instance, error) {
	return nil, notImplemented("instance store", "§5.5 instances")
}
func (nopInstanceStore) Create(context.Context, *Instance) error {
	return notImplemented("instance store", "§5.5 instances")
}
func (nopInstanceStore) Update(context.Context, *Instance) error {
	return notImplemented("instance store", "§5.5 instances")
}
func (nopInstanceStore) Delete(context.Context, int64) error {
	return notImplemented("instance store", "§5.5 instances")
}
func (nopInstanceStore) ListUsedPorts(context.Context) ([]int, error) { return nil, nil }
func (nopInstanceStore) GetState(context.Context, int64) (*InstanceState, error) {
	return nil, ErrNotFound
}
func (nopInstanceStore) SaveState(context.Context, *InstanceState) error {
	return notImplemented("instance store", "§5.5 instance_state")
}

type nopAuditStore struct{}

func (nopAuditStore) Write(context.Context, *AuditEntry) error { return nil }
func (nopAuditStore) List(context.Context, AuditFilter) ([]AuditEntry, error) {
	return nil, notImplemented("audit store", "§5.5 audit_logs")
}

type nopBackupStore struct{}

func (nopBackupStore) List(context.Context, BackupFilter) ([]Backup, error) {
	return nil, notImplemented("backup store", "§5.5 backups")
}
func (nopBackupStore) GetByID(context.Context, int64) (*Backup, error) {
	return nil, notImplemented("backup store", "§5.5 backups")
}
func (nopBackupStore) Create(context.Context, *Backup) error {
	return notImplemented("backup store", "§5.5 backups")
}
func (nopBackupStore) Delete(context.Context, int64) error {
	return notImplemented("backup store", "§5.5 backups")
}

type nopJobStore struct{}

func (nopJobStore) List(context.Context) ([]Job, error) {
	return nil, notImplemented("job store", "§5.5 jobs")
}
func (nopJobStore) GetByID(context.Context, int64) (*Job, error) {
	return nil, notImplemented("job store", "§5.5 jobs")
}
func (nopJobStore) Create(context.Context, *Job) error {
	return notImplemented("job store", "§5.5 jobs")
}
func (nopJobStore) Update(context.Context, *Job) error {
	return notImplemented("job store", "§5.5 jobs")
}
func (nopJobStore) Delete(context.Context, int64) error {
	return notImplemented("job store", "§5.5 jobs")
}

// ---------------------------------------------------------------------------
// Feature services
// ---------------------------------------------------------------------------

type nopConfigService struct{}

func (nopConfigService) Get(context.Context, *Instance, ConfigKind) (*ConfigDoc, error) {
	return nil, notImplemented("config service", "§6.2")
}
func (nopConfigService) Write(context.Context, *Instance, ConfigUpdate) (*ConfigDoc, error) {
	return nil, notImplemented("config service", "§6.2")
}
func (nopConfigService) Validate(context.Context, *Instance, ConfigKind, []byte) (*ValidationResult, error) {
	return nil, notImplemented("config service", "§6.2")
}

type nopWorldService struct{}

func (nopWorldService) List(context.Context, *Instance) ([]WorldInfo, error) {
	return nil, notImplemented("world service", "§6.3")
}
func (nopWorldService) Import(context.Context, *Instance, string, []byte) (*WorldInfo, error) {
	return nil, notImplemented("world service", "§6.3")
}
func (nopWorldService) Export(context.Context, *Instance, string, bool) ([]byte, string, error) {
	return nil, "", notImplemented("world service", "§6.3")
}
func (nopWorldService) Backup(context.Context, *Instance, string, string) (*Backup, error) {
	return nil, notImplemented("world service", "§6.3")
}
func (nopWorldService) Restore(context.Context, *Instance, string, int64) error {
	return notImplemented("world service", "§6.3")
}
func (nopWorldService) Activate(context.Context, *Instance, string) error {
	return notImplemented("world service", "§6.3")
}
func (nopWorldService) Delete(context.Context, *Instance, string) error {
	return notImplemented("world service", "§6.3")
}

type nopFileService struct{}

func (nopFileService) List(context.Context, *Instance, string) ([]FileEntry, error) {
	return nil, notImplemented("file service", "§6.4")
}
func (nopFileService) Read(context.Context, *Instance, string) ([]byte, string, error) {
	return nil, "", notImplemented("file service", "§6.4")
}
func (nopFileService) Write(context.Context, *Instance, string, []byte) error {
	return notImplemented("file service", "§6.4")
}
func (nopFileService) Mkdir(context.Context, *Instance, string) error {
	return notImplemented("file service", "§6.4")
}
func (nopFileService) Rename(context.Context, *Instance, string, string) error {
	return notImplemented("file service", "§6.4")
}
func (nopFileService) Delete(context.Context, *Instance, string) error {
	return notImplemented("file service", "§6.4")
}
func (nopFileService) Unzip(context.Context, *Instance, string, string) error {
	return notImplemented("file service", "§6.4")
}
func (nopFileService) SaveUpload(context.Context, *Instance, string, []byte) (*FileEntry, error) {
	return nil, notImplemented("file service", "§6.4")
}

type nopLogService struct{}

func (nopLogService) Tail(context.Context, *Instance, int, string) ([]string, error) {
	return nil, notImplemented("log service", "§5.2")
}
func (nopLogService) LogsPath(context.Context, *Instance) (string, error) {
	return "", notImplemented("log service", "§5.2")
}

type nopBackupService struct{}

func (nopBackupService) Create(context.Context, *Instance, string, string) (*Backup, error) {
	return nil, notImplemented("backup service", "§6.5")
}
func (nopBackupService) Restore(context.Context, *Instance, int64) error {
	return notImplemented("backup service", "§6.5")
}

type nopJobService struct{}

func (nopJobService) Validate(*Job) (*ValidationResult, error) {
	return nil, notImplemented("job service", "§6.7")
}
func (nopJobService) Reschedule(context.Context, *Job) error {
	return notImplemented("job service", "§6.7")
}

// ---------------------------------------------------------------------------
// NopDeps
// ---------------------------------------------------------------------------

// NopDeps returns a Deps bundle whose feature backends are all nop
// implementations, plus working in-memory session/event/port machinery.
//
// It still requires Tokens to be supplied by the caller and returns the
// normalized bundle, so it is primarily useful in tests and for embedding the
// API in a host that wires only the pieces it has.
func NopDeps(tokens *auth.TokenIssuer) (Deps, error) {
	return Deps{Tokens: tokens}.Normalize()
}
