package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestUserCreateAndGet(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	u := &User{Username: "alice", PasswordHash: "$2a$10$abcdefghijklmnopqrstuv", Role: RoleOperator}
	if err := s.Users().Create(ctx, u); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if u.ID == 0 {
		t.Fatal("Create must fill in ID")
	}
	if u.CreatedAt.IsZero() {
		t.Fatal("Create must fill in CreatedAt")
	}

	got, err := s.Users().GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Username != "alice" || got.Role != RoleOperator {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if got.Disabled {
		t.Error("new user must be enabled by default")
	}
	if got.LastLoginAt != nil {
		t.Errorf("LastLoginAt must be nil before first login, got %v", got.LastLoginAt)
	}
	if got.PasswordHash != u.PasswordHash {
		t.Errorf("PasswordHash = %q, want %q", got.PasswordHash, u.PasswordHash)
	}

	byName, err := s.Users().GetByUsername(ctx, "alice")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if byName.ID != u.ID {
		t.Errorf("GetByUsername id = %d, want %d", byName.ID, u.ID)
	}

	// Usernames resolve case-insensitively.
	if _, err := s.Users().GetByUsername(ctx, "ALICE"); err != nil {
		t.Errorf("GetByUsername must be case-insensitive: %v", err)
	}
}

func TestUserCreateDuplicateUsernameIsTyped(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	first := &User{Username: "bob", PasswordHash: "h", Role: RoleViewer}
	if err := s.Users().Create(ctx, first); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	dup := &User{Username: "bob", PasswordHash: "h2", Role: RoleViewer}
	err := s.Users().Create(ctx, dup)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate username error = %v, want ErrDuplicate", err)
	}
	if dup.ID != 0 {
		t.Errorf("failed insert must not set ID, got %d", dup.ID)
	}

	// The seeded admin collides too, and the message names the username.
	err = s.Users().Create(ctx, &User{Username: "admin", PasswordHash: "h", Role: RoleAdmin})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("creating admin again = %v, want ErrDuplicate", err)
	}
	if !strings.Contains(err.Error(), "admin") {
		t.Errorf("duplicate error should name the username, got %q", err)
	}
}

func TestUserCreateValidation(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	cases := map[string]*User{
		"nil":          nil,
		"empty name":   {Username: "  ", Role: RoleViewer},
		"unknown role": {Username: "carol", Role: "superuser"},
	}
	for name, u := range cases {
		if err := s.Users().Create(ctx, u); !errors.Is(err, ErrInvalid) {
			t.Errorf("Create(%s) = %v, want ErrInvalid", name, err)
		}
	}
}

func TestUserNotFoundIsTyped(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	if _, err := s.Users().GetByID(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetByID(missing) = %v, want ErrNotFound", err)
	}
	if _, err := s.Users().GetByUsername(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetByUsername(missing) = %v, want ErrNotFound", err)
	}
	if err := s.Users().UpdatePassword(ctx, 9999, "h"); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdatePassword(missing) = %v, want ErrNotFound", err)
	}
	if err := s.Users().SetRole(ctx, 9999, RoleViewer); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetRole(missing) = %v, want ErrNotFound", err)
	}
	if err := s.Users().SetDisabled(ctx, 9999, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetDisabled(missing) = %v, want ErrNotFound", err)
	}
	if err := s.Users().TouchLastLogin(ctx, 9999, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("TouchLastLogin(missing) = %v, want ErrNotFound", err)
	}
	if err := s.Users().Delete(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete(missing) = %v, want ErrNotFound", err)
	}
}

func TestUserList(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	for _, name := range []string{"u1", "u2", "u3"} {
		if err := s.Users().Create(ctx, &User{Username: name, PasswordHash: "h", Role: RoleViewer}); err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
	}

	users, err := s.Users().List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(users) != 4 { // seeded admin + 3
		t.Fatalf("List returned %d users, want 4", len(users))
	}
	if users[0].Username != "admin" {
		t.Errorf("List must be ordered by id, first = %q", users[0].Username)
	}
	for i := 1; i < len(users); i++ {
		if users[i].ID <= users[i-1].ID {
			t.Errorf("List not ordered by id: %d then %d", users[i-1].ID, users[i].ID)
		}
	}
}

func TestUserSetRoleAndDisabledRoundTrip(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	u := &User{Username: "dave", PasswordHash: "h", Role: RoleViewer}
	if err := s.Users().Create(ctx, u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.Users().SetRole(ctx, u.ID, RoleOperator); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	got, err := s.Users().GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Role != RoleOperator {
		t.Errorf("role = %q, want %q", got.Role, RoleOperator)
	}

	// Setting the same role twice is a no-op, not an error.
	if err := s.Users().SetRole(ctx, u.ID, RoleOperator); err != nil {
		t.Errorf("idempotent SetRole: %v", err)
	}

	if err := s.Users().SetDisabled(ctx, u.ID, true); err != nil {
		t.Fatalf("SetDisabled: %v", err)
	}
	got, err = s.Users().GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !got.Disabled {
		t.Error("user must be disabled")
	}
	if got.Enabled() {
		t.Error("Enabled() must invert Disabled")
	}
	if err := s.Users().SetDisabled(ctx, u.ID, false); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	got, _ = s.Users().GetByID(ctx, u.ID)
	if got.Disabled {
		t.Error("user must be re-enabled")
	}

	if err := s.Users().SetRole(ctx, u.ID, "wizard"); !errors.Is(err, ErrInvalid) {
		t.Errorf("SetRole(bogus) = %v, want ErrInvalid", err)
	}
}

func TestUserUpdatePasswordAndTouchLastLogin(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	// The seeded admin is the first-run case: marker hash in, real hash out.
	admin, err := s.Users().GetByID(ctx, 1)
	if err != nil {
		t.Fatalf("GetByID(1): %v", err)
	}
	if !admin.NeedsPasswordSetup() {
		t.Fatal("seeded admin must need password setup")
	}

	const hash = "$2a$10$X8sQ0m4Lh1Q0k9Wc0m3nOeYQ2b1r0u1s2t3v4w5x6y7z8A9B0C1D2"
	if err := s.Users().UpdatePassword(ctx, 1, hash); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}
	admin, err = s.Users().GetByID(ctx, 1)
	if err != nil {
		t.Fatalf("GetByID after: %v", err)
	}
	if admin.NeedsPasswordSetup() {
		t.Error("admin must not need password setup after UpdatePassword")
	}
	if admin.PasswordHash != hash {
		t.Errorf("hash = %q, want %q", admin.PasswordHash, hash)
	}
	if err := s.Users().UpdatePassword(ctx, 1, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("UpdatePassword(\"\") = %v, want ErrInvalid", err)
	}

	// TouchLastLogin with a fixed instant must round-trip as UTC text.
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	if err := s.Users().TouchLastLogin(ctx, 1, at); err != nil {
		t.Fatalf("TouchLastLogin: %v", err)
	}
	admin, err = s.Users().GetByID(ctx, 1)
	if err != nil {
		t.Fatalf("GetByID after login: %v", err)
	}
	if admin.LastLoginAt == nil {
		t.Fatal("LastLoginAt must be set")
	}
	if !admin.LastLoginAt.Equal(at) {
		t.Errorf("LastLoginAt = %v, want %v", admin.LastLoginAt, at)
	}
}

func TestUserCountAdmins(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	n, err := s.Users().CountAdmins(ctx)
	if err != nil {
		t.Fatalf("CountAdmins: %v", err)
	}
	if n != 1 {
		t.Fatalf("CountAdmins = %d, want 1 (the seeded admin)", n)
	}

	if err := s.Users().Create(ctx, &User{Username: "root2", PasswordHash: "h", Role: RoleAdmin}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if n, _ = s.Users().CountAdmins(ctx); n != 2 {
		t.Fatalf("CountAdmins = %d, want 2", n)
	}

	// Disabled admins do not count: they cannot log in.
	if err := s.Users().SetDisabled(ctx, 2, true); err != nil {
		t.Fatalf("SetDisabled: %v", err)
	}
	if n, _ = s.Users().CountAdmins(ctx); n != 1 {
		t.Fatalf("CountAdmins after disabling one = %d, want 1", n)
	}

	// Operators and viewers never count.
	op := &User{Username: "op", PasswordHash: "h", Role: RoleOperator}
	if err := s.Users().Create(ctx, op); err != nil {
		t.Fatalf("Create operator: %v", err)
	}
	if n, _ = s.Users().CountAdmins(ctx); n != 1 {
		t.Fatalf("CountAdmins = %d, want 1 (operators excluded)", n)
	}
}

func TestUserDeleteRemovesGrants(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "del-user")

	u := &User{Username: "temp", PasswordHash: "h", Role: RoleOperator}
	if err := s.Users().Create(ctx, u); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Users().Grant(ctx, u.ID, inst.ID, PermControl); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	if err := s.Users().Delete(ctx, u.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Users().GetByID(ctx, u.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetByID after delete = %v, want ErrNotFound", err)
	}

	grants, err := s.Users().ListForUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if len(grants) != 0 {
		t.Errorf("grants must be removed with the user, got %+v", grants)
	}
}

// TestUserLastAdminProtection is the D3 guard: the panel must never end up with
// nobody able to administer it.
func TestUserLastAdminProtection(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	// The seed leaves exactly one enabled admin.
	if n, _ := s.Users().CountAdmins(ctx); n != 1 {
		t.Fatalf("precondition: CountAdmins = %d, want 1", n)
	}

	t.Run("delete", func(t *testing.T) {
		err := s.Users().Delete(ctx, 1)
		if !errors.Is(err, ErrLastAdmin) {
			t.Fatalf("Delete(last admin) = %v, want ErrLastAdmin", err)
		}
		if !errors.Is(err, ErrLastAdmin) || !errors.Is(err, ErrLastAdmin) {
			t.Fatal("ErrLastAdmin must survive wrapping")
		}
		if _, gerr := s.Users().GetByID(ctx, 1); gerr != nil {
			t.Fatalf("admin must still exist after refused delete: %v", gerr)
		}
	})

	t.Run("disable", func(t *testing.T) {
		err := s.Users().SetDisabled(ctx, 1, true)
		if !errors.Is(err, ErrLastAdmin) {
			t.Fatalf("SetDisabled(last admin) = %v, want ErrLastAdmin", err)
		}
		u, _ := s.Users().GetByID(ctx, 1)
		if u.Disabled {
			t.Error("admin must remain enabled after refused disable")
		}
	})

	t.Run("demote", func(t *testing.T) {
		err := s.Users().SetRole(ctx, 1, RoleViewer)
		if !errors.Is(err, ErrLastAdmin) {
			t.Fatalf("SetRole(last admin -> viewer) = %v, want ErrLastAdmin", err)
		}
		u, _ := s.Users().GetByID(ctx, 1)
		if u.Role != RoleAdmin {
			t.Errorf("role = %q, want it unchanged as %q", u.Role, RoleAdmin)
		}
	})

	t.Run("allowed once a second admin exists", func(t *testing.T) {
		second := &User{Username: "admin2", PasswordHash: "h", Role: RoleAdmin}
		if err := s.Users().Create(ctx, second); err != nil {
			t.Fatalf("Create second admin: %v", err)
		}

		// Demoting the original is now fine.
		if err := s.Users().SetRole(ctx, 1, RoleOperator); err != nil {
			t.Fatalf("SetRole with a second admin present: %v", err)
		}
		// ...and the new one becomes the protected row.
		if err := s.Users().Delete(ctx, second.ID); !errors.Is(err, ErrLastAdmin) {
			t.Fatalf("Delete(remaining admin) = %v, want ErrLastAdmin", err)
		}
		if err := s.Users().SetDisabled(ctx, second.ID, true); !errors.Is(err, ErrLastAdmin) {
			t.Fatalf("SetDisabled(remaining admin) = %v, want ErrLastAdmin", err)
		}

		// Deleting the demoted ex-admin now succeeds: only enabled admins
		// are protected.
		if err := s.Users().Delete(ctx, 1); err != nil {
			t.Fatalf("Delete(demoted ex-admin): %v", err)
		}
	})

	t.Run("a disabled admin does not count as cover", func(t *testing.T) {
		// After the previous subtest only admin2 remains, disabled-able but
		// currently the sole admin. Make a viewer and confirm the guard holds.
		v := &User{Username: "v", PasswordHash: "h", Role: RoleViewer}
		if err := s.Users().Create(ctx, v); err != nil {
			t.Fatalf("Create viewer: %v", err)
		}
		if err := s.Users().SetDisabled(ctx, v.ID, false); err != nil {
			t.Fatalf("SetDisabled(viewer): %v", err)
		}
		// admin2 is the only admin.
		admins, _ := s.Users().List(ctx)
		var only int64
		for _, u := range admins {
			if u.Role == RoleAdmin && !u.Disabled {
				only = u.ID
			}
		}
		if only == 0 {
			t.Fatal("expected exactly one enabled admin")
		}
		if err := s.Users().Delete(ctx, only); !errors.Is(err, ErrLastAdmin) {
			t.Fatalf("Delete(sole admin) = %v, want ErrLastAdmin", err)
		}
	})
}

// --- instance-level grants (D3) -----------------------------------------

func TestGrantRevokeAndList(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst1 := mustCreateInstance(t, s, "grant-1")
	inst2 := mustCreateInstance(t, s, "grant-2")

	u := &User{Username: "gina", PasswordHash: "h", Role: RoleOperator}
	if err := s.Users().Create(ctx, u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	for _, g := range []struct {
		inst int64
		perm string
	}{
		{inst1.ID, PermRead},
		{inst1.ID, PermControl},
		{inst2.ID, PermConfig},
	} {
		if err := s.Users().Grant(ctx, u.ID, g.inst, g.perm); err != nil {
			t.Fatalf("Grant(%d, %s): %v", g.inst, g.perm, err)
		}
	}

	// Re-granting is idempotent.
	if err := s.Users().Grant(ctx, u.ID, inst1.ID, PermRead); err != nil {
		t.Fatalf("re-Grant must be idempotent: %v", err)
	}

	grants, err := s.Users().ListForUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if len(grants) != 3 {
		t.Fatalf("ListForUser returned %d grants, want 3: %+v", len(grants), grants)
	}
	// Ordered by instance then perm.
	if grants[0].InstanceID != inst1.ID || grants[0].Perm != PermControl {
		t.Errorf("first grant = %+v, want (inst1, control)", grants[0])
	}

	byInst, err := s.Users().ListForInstance(ctx, inst1.ID)
	if err != nil {
		t.Fatalf("ListForInstance: %v", err)
	}
	if len(byInst) != 2 {
		t.Errorf("ListForInstance = %d grants, want 2", len(byInst))
	}

	if err := s.Users().Grant(ctx, u.ID, inst1.ID, "sudo"); !errors.Is(err, ErrInvalid) {
		t.Errorf("Grant(bogus perm) = %v, want ErrInvalid", err)
	}

	if err := s.Users().Revoke(ctx, u.ID, inst2.ID, PermConfig); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := s.Users().Revoke(ctx, u.ID, inst2.ID, PermConfig); !errors.Is(err, ErrNotFound) {
		t.Errorf("Revoke twice = %v, want ErrNotFound", err)
	}
	grants, _ = s.Users().ListForUser(ctx, u.ID)
	if len(grants) != 2 {
		t.Errorf("after revoke = %d grants, want 2", len(grants))
	}

	n, err := s.Users().RevokeAllForInstance(ctx, inst1.ID)
	if err != nil {
		t.Fatalf("RevokeAllForInstance: %v", err)
	}
	if n != 2 {
		t.Errorf("RevokeAllForInstance removed %d, want 2", n)
	}
	if grants, _ = s.Users().ListForInstance(ctx, inst1.ID); len(grants) != 0 {
		t.Errorf("grants remain for instance: %+v", grants)
	}
}

func TestHasPermission(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "perm-inst")

	// The admin holds everything implicitly; that is what lets single-user
	// mode run with an empty grant table.
	ok, err := s.Users().HasPermission(ctx, 1, inst.ID, PermControl)
	if err != nil {
		t.Fatalf("HasPermission(admin): %v", err)
	}
	if !ok {
		t.Error("admin must implicitly hold every permission")
	}

	op := &User{Username: "pat", PasswordHash: "h", Role: RoleOperator}
	if err := s.Users().Create(ctx, op); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Nothing granted yet.
	ok, err = s.Users().HasPermission(ctx, op.ID, inst.ID, PermControl)
	if err != nil {
		t.Fatalf("HasPermission(op): %v", err)
	}
	if ok {
		t.Error("operator must not hold an ungranted permission")
	}

	if err := s.Users().Grant(ctx, op.ID, inst.ID, PermControl); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if ok, _ = s.Users().HasPermission(ctx, op.ID, inst.ID, PermControl); !ok {
		t.Error("granted permission must be held")
	}
	// A different permission on the same instance is still absent.
	if ok, _ = s.Users().HasPermission(ctx, op.ID, inst.ID, PermConfig); ok {
		t.Error("control must not imply config")
	}

	// Ownership is an implicit grant (D3's owner_id).
	owned := mustCreateInstance(t, s, "perm-owned")
	if err := s.Instances().SetOwner(ctx, owned.ID, op.ID); err != nil {
		t.Fatalf("SetOwner: %v", err)
	}
	if ok, _ = s.Users().HasPermission(ctx, op.ID, owned.ID, PermConfig); !ok {
		t.Error("owner must implicitly hold permissions on their instance")
	}

	// Disabled accounts hold nothing, even with a grant row.
	if err := s.Users().SetDisabled(ctx, op.ID, true); err != nil {
		t.Fatalf("SetDisabled: %v", err)
	}
	if ok, _ = s.Users().HasPermission(ctx, op.ID, inst.ID, PermControl); ok {
		t.Error("disabled user must hold no permission")
	}

	// Unknown users and unknown permissions are not errors-but-granted.
	if ok, err = s.Users().HasPermission(ctx, 9999, inst.ID, PermControl); err != nil || ok {
		t.Errorf("HasPermission(unknown user) = (%v, %v), want (false, nil)", ok, err)
	}
	if _, err = s.Users().HasPermission(ctx, 1, inst.ID, "sudo"); !errors.Is(err, ErrInvalid) {
		t.Errorf("HasPermission(bogus perm) = %v, want ErrInvalid", err)
	}
}
