package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestJobCreateAndGet(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "job-create")

	j := &Job{
		InstanceID: inst.ID,
		Type:       JobBackup,
		Cron:       "0 4 * * *",
		Payload:    `{"kind":"scheduled"}`,
		Enabled:    true,
	}
	if err := s.Jobs().Create(ctx, j); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if j.ID == 0 {
		t.Fatal("Create must fill in ID")
	}

	got, err := s.Jobs().Get(ctx, j.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Type != JobBackup || got.Cron != "0 4 * * *" || !got.Enabled {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if got.Payload != `{"kind":"scheduled"}` {
		t.Errorf("Payload = %q", got.Payload)
	}
	if got.LastRun != nil {
		t.Errorf("a new job must have no last_run, got %v", got.LastRun)
	}
	if got.LastResult != "" {
		t.Errorf("a new job must have no last_result, got %q", got.LastResult)
	}
}

func TestJobCreateDefaultsAndValidation(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "job-val")

	// Payload defaults to an empty JSON object.
	j := &Job{InstanceID: inst.ID, Type: JobCommand, Cron: "* * * * *"}
	if err := s.Jobs().Create(ctx, j); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if j.Payload != "{}" {
		t.Errorf("Payload = %q, want {}", j.Payload)
	}

	cases := map[string]*Job{
		"nil":          nil,
		"unknown type": {InstanceID: inst.ID, Type: "email", Cron: "* * * * *"},
		"no cron":      {InstanceID: inst.ID, Type: JobBackup},
		"no instance":  {Type: JobBackup, Cron: "* * * * *"},
	}
	for name, bad := range cases {
		if err := s.Jobs().Create(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("Create(%s) = %v, want ErrInvalid", name, err)
		}
	}
}

func TestJobNotFoundIsTyped(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	if _, err := s.Jobs().Get(ctx, 777); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(missing) = %v, want ErrNotFound", err)
	}
	if err := s.Jobs().Delete(ctx, 777); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete(missing) = %v, want ErrNotFound", err)
	}
	if err := s.Jobs().SetEnabled(ctx, 777, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetEnabled(missing) = %v, want ErrNotFound", err)
	}
	if err := s.Jobs().RecordRun(ctx, 777, "ok", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("RecordRun(missing) = %v, want ErrNotFound", err)
	}
	if err := s.Jobs().Update(ctx, &Job{ID: 777, InstanceID: 1, Type: JobBackup, Cron: "* * * * *"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update(missing) = %v, want ErrNotFound", err)
	}
}

func TestJobListAndListEnabled(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	a := mustCreateInstance(t, s, "job-list-a")
	b := mustCreateInstance(t, s, "job-list-b")

	for _, spec := range []struct {
		inst    int64
		typ     string
		enabled bool
	}{
		{a.ID, JobBackup, true},
		{a.ID, JobRestart, false},
		{b.ID, JobCommand, true},
	} {
		if err := s.Jobs().Create(ctx, &Job{
			InstanceID: spec.inst, Type: spec.typ, Cron: "0 4 * * *", Enabled: spec.enabled,
		}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	all, err := s.Jobs().List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("List = %d jobs, want 3", len(all))
	}

	enabled, err := s.Jobs().ListEnabled(ctx)
	if err != nil {
		t.Fatalf("ListEnabled: %v", err)
	}
	if len(enabled) != 2 {
		t.Fatalf("ListEnabled = %d jobs, want 2", len(enabled))
	}
	for _, j := range enabled {
		if !j.Enabled {
			t.Errorf("ListEnabled returned a disabled job: %+v", j)
		}
	}

	byInst, err := s.Jobs().ListByInstance(ctx, a.ID)
	if err != nil {
		t.Fatalf("ListByInstance: %v", err)
	}
	if len(byInst) != 2 {
		t.Errorf("ListByInstance(a) = %d, want 2", len(byInst))
	}
}

func TestJobUpdatePreservesRunHistory(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "job-upd")

	j := &Job{InstanceID: inst.ID, Type: JobBackup, Cron: "0 4 * * *", Enabled: true}
	if err := s.Jobs().Create(ctx, j); err != nil {
		t.Fatalf("Create: %v", err)
	}

	ranAt := time.Date(2026, 9, 1, 4, 0, 0, 0, time.UTC)
	if err := s.Jobs().RecordRun(ctx, j.ID, "ok: 12 files", ranAt); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	// An edit from the UI must not rewind the scheduler's bookkeeping.
	j.Cron = "0 5 * * *"
	j.Type = JobRestart
	j.Payload = `{"force":true}`
	j.LastRun = nil
	j.LastResult = "clobbered"
	if err := s.Jobs().Update(ctx, j); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := s.Jobs().Get(ctx, j.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Cron != "0 5 * * *" || got.Type != JobRestart {
		t.Errorf("mutable columns not updated: %+v", got)
	}
	if got.LastRun == nil || !got.LastRun.Equal(ranAt) {
		t.Errorf("Update must not touch last_run, got %v want %v", got.LastRun, ranAt)
	}
	if got.LastResult != "ok: 12 files" {
		t.Errorf("Update must not touch last_result, got %q", got.LastResult)
	}

	// Update validation mirrors Create.
	if err := s.Jobs().Update(ctx, &Job{ID: j.ID, InstanceID: inst.ID, Type: "email", Cron: "* * * * *"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("Update(bogus type) = %v, want ErrInvalid", err)
	}
	if err := s.Jobs().Update(ctx, &Job{ID: j.ID, InstanceID: inst.ID, Type: JobBackup, Cron: ""}); !errors.Is(err, ErrInvalid) {
		t.Errorf("Update(empty cron) = %v, want ErrInvalid", err)
	}
}

func TestJobSetEnabledAndRecordRun(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "job-toggle")

	j := &Job{InstanceID: inst.ID, Type: JobBackup, Cron: "0 4 * * *", Enabled: true}
	if err := s.Jobs().Create(ctx, j); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.Jobs().SetEnabled(ctx, j.ID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	got, _ := s.Jobs().Get(ctx, j.ID)
	if got.Enabled {
		t.Error("job must be disabled")
	}
	// A disabled job drops out of the scheduler's load.
	if n, _ := s.Jobs().ListEnabled(ctx); len(n) != 0 {
		t.Errorf("ListEnabled = %d, want 0", len(n))
	}
	if err := s.Jobs().SetEnabled(ctx, j.ID, true); err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}

	// RecordRun with the zero time defaults to now rather than storing NULL.
	if err := s.Jobs().RecordRun(ctx, j.ID, "finished", time.Time{}); err != nil {
		t.Fatalf("RecordRun(zero): %v", err)
	}
	got, _ = s.Jobs().Get(ctx, j.ID)
	if got.LastRun == nil {
		t.Fatal("RecordRun must set last_run")
	}
	if got.LastResult != "finished" {
		t.Errorf("LastResult = %q", got.LastResult)
	}
	if time.Since(*got.LastRun) > time.Minute {
		t.Errorf("zero-time RecordRun should default to now, got %v", got.LastRun)
	}

	// A failure summary is stored verbatim so the UI can show it.
	if err := s.Jobs().RecordRun(ctx, j.ID, "backup failed: disk full", time.Now().UTC()); err != nil {
		t.Fatalf("RecordRun(failure): %v", err)
	}
	got, _ = s.Jobs().Get(ctx, j.ID)
	if got.LastResult != "backup failed: disk full" {
		t.Errorf("LastResult = %q", got.LastResult)
	}
}

func TestJobDelete(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "job-del")
	j := &Job{InstanceID: inst.ID, Type: JobBackup, Cron: "0 4 * * *"}
	if err := s.Jobs().Create(ctx, j); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.Jobs().Delete(ctx, j.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Jobs().Get(ctx, j.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete = %v, want ErrNotFound", err)
	}
}

func TestValidJobType(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{JobBackup, JobRestart, JobCommand} {
		if !ValidJobType(ok) {
			t.Errorf("ValidJobType(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "email", "BACKUP"} {
		if ValidJobType(bad) {
			t.Errorf("ValidJobType(%q) = true, want false", bad)
		}
	}
}
