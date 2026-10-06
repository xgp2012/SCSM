package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestAuditInsertAndList(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	uid := int64(1)

	entries := []AuditLog{
		{UserID: &uid, Action: "instance.create", Target: "instance:1", Detail: "created", IP: "127.0.0.1", TS: base},
		{UserID: &uid, Action: "instance.start", Target: "instance:1", Detail: "started", IP: "127.0.0.1", TS: base.Add(time.Hour)},
		{UserID: &uid, Action: "instance.stop", Target: "instance:1", Detail: "stopped", IP: "127.0.0.1", TS: base.Add(2 * time.Hour)},
	}
	for i := range entries {
		if err := s.Audit().Insert(ctx, &entries[i]); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
		if entries[i].ID == 0 {
			t.Errorf("Insert must fill in ID for entry %d", i)
		}
	}

	all, err := s.Audit().List(ctx, AuditFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("List = %d entries, want 3", len(all))
	}
	// Newest first.
	if all[0].Action != "instance.stop" {
		t.Errorf("List must be newest-first, got %q first", all[0].Action)
	}
	if all[2].Action != "instance.create" {
		t.Errorf("List must be newest-first, got %q last", all[2].Action)
	}
	if all[0].UserID == nil || *all[0].UserID != 1 {
		t.Errorf("UserID = %v, want 1", all[0].UserID)
	}
	if !all[0].TS.Equal(base.Add(2 * time.Hour)) {
		t.Errorf("TS = %v, want %v", all[0].TS, base.Add(2*time.Hour))
	}
	if all[0].IP != "127.0.0.1" {
		t.Errorf("IP = %q", all[0].IP)
	}
}

func TestAuditDefaultsAndValidation(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	if err := s.Audit().Insert(ctx, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("Insert(nil) = %v, want ErrInvalid", err)
	}
	if err := s.Audit().Insert(ctx, &AuditLog{Target: "instance:1"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("Insert without action = %v, want ErrInvalid", err)
	}

	// TS defaults to now; UserID may legitimately be nil (a failed login has
	// no authenticated user).
	e := &AuditLog{Action: "auth.login.failed", Target: "user:admin", IP: "10.0.0.1"}
	if err := s.Audit().Insert(ctx, e); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if e.TS.IsZero() {
		t.Error("TS must default to now")
	}
	if e.UserID != nil {
		t.Errorf("UserID must stay nil when unset, got %v", *e.UserID)
	}

	got, err := s.Audit().List(ctx, AuditFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List = %d, want 1", len(got))
	}
	if got[0].UserID != nil {
		t.Errorf("a NULL user_id must scan as nil, got %v", *got[0].UserID)
	}
}

func TestAuditLogHelper(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	uid := int64(1)
	if err := s.Audit().Log(ctx, &uid, "panel.start", "panel", "booted", "127.0.0.1"); err != nil {
		t.Fatalf("Log: %v", err)
	}

	got, err := s.Audit().List(ctx, AuditFilter{Action: "panel.start"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List = %d, want 1", len(got))
	}
	if got[0].Detail != "booted" || got[0].Target != "panel" {
		t.Errorf("Log did not record the fields: %+v", got[0])
	}
	if got[0].TS.IsZero() {
		t.Error("Log must stamp the time")
	}
}

func TestAuditFilterByUserActionAndRange(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	u1, u2 := int64(1), int64(2)

	type spec struct {
		user   *int64
		action string
		offset time.Duration
	}
	specs := []spec{
		{&u1, "login", 0},
		{&u1, "start", time.Hour},
		{&u1, "start", 2 * time.Hour},
		{&u2, "login", 3 * time.Hour},
		{&u2, "stop", 4 * time.Hour},
		{&u1, "stop", 5 * time.Hour},
	}
	for _, sp := range specs {
		if err := s.Audit().Insert(ctx, &AuditLog{
			UserID: sp.user, Action: sp.action, Target: "instance:1", TS: base.Add(sp.offset),
		}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	u1ID, u2ID := u1, u2
	from, to := base.Add(time.Hour), base.Add(4*time.Hour)

	cases := []struct {
		name   string
		filter AuditFilter
		want   int
	}{
		{"no filter", AuditFilter{}, 6},
		{"by user 1", AuditFilter{UserID: &u1ID}, 4},
		{"by user 2", AuditFilter{UserID: &u2ID}, 2},
		{"by action login", AuditFilter{Action: "login"}, 2},
		{"by action start", AuditFilter{Action: "start"}, 2},
		{"by action and user", AuditFilter{UserID: &u1ID, Action: "stop"}, 1},
		{"from only", AuditFilter{From: &from}, 5},
		{"to only", AuditFilter{To: &to}, 5},
		{"inclusive range", AuditFilter{From: &from, To: &to}, 4},
		// User 1's entries sit at +0h, +1h, +2h and +5h; only +1h and +2h
		// are inside [1h, 4h] (the bounds are inclusive).
		{"range and user", AuditFilter{UserID: &u1ID, From: &from, To: &to}, 2},
		{"no match", AuditFilter{Action: "delete"}, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Audit().List(ctx, tc.filter)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("List = %d, want %d", len(got), tc.want)
			}
			for _, e := range got {
				if tc.filter.UserID != nil && (e.UserID == nil || *e.UserID != *tc.filter.UserID) {
					t.Errorf("entry for user %v leaked into a filter for %d", e.UserID, *tc.filter.UserID)
				}
				if tc.filter.Action != "" && e.Action != tc.filter.Action {
					t.Errorf("action %q leaked into a filter for %q", e.Action, tc.filter.Action)
				}
				if tc.filter.From != nil && e.TS.Before(*tc.filter.From) {
					t.Errorf("entry at %v is before the From bound %v", e.TS, *tc.filter.From)
				}
				if tc.filter.To != nil && e.TS.After(*tc.filter.To) {
					t.Errorf("entry at %v is after the To bound %v", e.TS, *tc.filter.To)
				}
			}

			// Count must agree with the unpaginated result.
			n, err := s.Audit().Count(ctx, tc.filter)
			if err != nil {
				t.Fatalf("Count: %v", err)
			}
			if int(n) != tc.want {
				t.Errorf("Count = %d, want %d", n, tc.want)
			}
		})
	}
}

// TestAuditPagination pins that Limit/Offset partition the result set in a
// stable, non-overlapping way, even when many rows share a timestamp.
func TestAuditPagination(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	// Deliberately all at the same instant: the id tie-break must make
	// pagination deterministic.
	sameSecond := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	const total = 25
	for i := 0; i < total; i++ {
		if err := s.Audit().Insert(ctx, &AuditLog{
			Action: fmt.Sprintf("action-%02d", i),
			Target: "instance:1",
			TS:     sameSecond,
		}); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	var seen []string
	for offset := 0; offset < total; offset += 10 {
		page, err := s.Audit().List(ctx, AuditFilter{Limit: 10, Offset: offset})
		if err != nil {
			t.Fatalf("List offset %d: %v", offset, err)
		}
		for _, e := range page {
			seen = append(seen, e.Action)
		}
	}
	if len(seen) != total {
		t.Fatalf("paging returned %d entries, want %d", len(seen), total)
	}

	// No duplicates, no gaps: descending id means descending action number.
	uniq := map[string]bool{}
	for i, action := range seen {
		if uniq[action] {
			t.Fatalf("entry %q appeared twice across pages", action)
		}
		uniq[action] = true
		want := fmt.Sprintf("action-%02d", total-1-i)
		if action != want {
			t.Fatalf("page position %d = %q, want %q", i, action, want)
		}
	}

	// An offset past the end is empty, not an error.
	page, err := s.Audit().List(ctx, AuditFilter{Limit: 10, Offset: 1000})
	if err != nil {
		t.Fatalf("List with a large offset: %v", err)
	}
	if len(page) != 0 {
		t.Errorf("offset past the end returned %d entries", len(page))
	}

	// Count ignores Limit/Offset.
	n, err := s.Audit().Count(ctx, AuditFilter{Limit: 1, Offset: 20})
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != total {
		t.Errorf("Count = %d, want %d", n, total)
	}
}

func TestAuditListByInstance(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		if err := s.Audit().Insert(ctx, &AuditLog{
			Action: "instance.start", Target: "instance:7", TS: base.Add(time.Duration(i) * time.Hour),
		}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	if err := s.Audit().Insert(ctx, &AuditLog{
		Action: "instance.start", Target: "instance:8", TS: base,
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := s.Audit().ListByInstance(ctx, 7, 10, 0)
	if err != nil {
		t.Fatalf("ListByInstance: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("ListByInstance(7) = %d, want 4", len(got))
	}
	for _, e := range got {
		if e.Target != "instance:7" {
			t.Errorf("entry for %q leaked into instance 7", e.Target)
		}
	}

	// Offset works through the helper too.
	page, err := s.Audit().ListByInstance(ctx, 7, 2, 2)
	if err != nil {
		t.Fatalf("ListByInstance paged: %v", err)
	}
	if len(page) != 2 {
		t.Errorf("paged ListByInstance = %d, want 2", len(page))
	}
}

func TestAuditPrune(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		if err := s.Audit().Insert(ctx, &AuditLog{
			Action: "tick", TS: base.AddDate(0, 0, i),
		}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	cutoff := base.AddDate(0, 0, 6)
	n, err := s.Audit().Prune(ctx, cutoff)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 6 {
		t.Errorf("Prune removed %d, want 6", n)
	}

	remaining, err := s.Audit().List(ctx, AuditFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(remaining) != 4 {
		t.Fatalf("%d entries remain, want 4", len(remaining))
	}
	// Prune is exclusive of the cutoff.
	if remaining[len(remaining)-1].TS.Before(cutoff) {
		t.Errorf("Prune removed an entry at or after the cutoff: %v", remaining[len(remaining)-1].TS)
	}

	// Idempotent.
	if n, err = s.Audit().Prune(ctx, cutoff); err != nil || n != 0 {
		t.Errorf("second Prune = (%d, %v), want (0, nil)", n, err)
	}
}

// TestAuditIsAppendOnly documents the deliberate absence of mutation: the
// concrete assertion is that a pruned entry cannot be brought back and that
// there is no exported way to edit a row in place.
func TestAuditIsAppendOnly(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	e := &AuditLog{Action: "sealed", Target: "panel", Detail: "original"}
	if err := s.Audit().Insert(ctx, e); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Writing the same struct again appends a second row rather than
	// overwriting the first; the ID it carries is ignored on insert.
	if err := s.Audit().Insert(ctx, e); err != nil {
		t.Fatalf("second Insert: %v", err)
	}
	all, err := s.Audit().List(ctx, AuditFilter{Action: "sealed"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("append-only insert produced %d rows, want 2", len(all))
	}
	if all[0].ID == all[1].ID {
		t.Errorf("two appends share an id: %d", all[0].ID)
	}
}
