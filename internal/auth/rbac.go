package auth

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Role is one of the three RBAC tiers from §5.5/§5.7.
//
// The panel ships in single-user mode (one admin row), but the roles, the
// instance-level grant model and the middleware are wired from day one so
// enabling multi-user later needs no schema migration and no handler changes.
type Role string

// The three roles, ordered from most to least privileged.
const (
	// RoleAdmin may do everything, including user management and panel
	// configuration.
	RoleAdmin Role = "admin"
	// RoleOperator may control instances (start/stop/restart/config/files)
	// but may not manage users or delete instances outright.
	RoleOperator Role = "operator"
	// RoleViewer is read-only.
	RoleViewer Role = "viewer"
)

// RoleRank orders roles for comparison. Higher wins.
var roleRank = map[Role]int{
	RoleViewer:   1,
	RoleOperator: 2,
	RoleAdmin:    3,
}

// AllRoles lists every valid role, most privileged first.
var AllRoles = []Role{RoleAdmin, RoleOperator, RoleViewer}

// Valid reports whether r is one of the three known roles.
func (r Role) Valid() bool {
	_, ok := roleRank[r]
	return ok
}

// Rank returns the numeric privilege level (0 for unknown roles).
func (r Role) Rank() int { return roleRank[r] }

// AtLeast reports whether r is at least as privileged as min.
func (r Role) AtLeast(min Role) bool {
	if !r.Valid() || !min.Valid() {
		return false
	}
	return r.Rank() >= min.Rank()
}

// ParseRole normalises a role string, returning an error for unknown values.
func ParseRole(s string) (Role, error) {
	r := Role(strings.ToLower(strings.TrimSpace(s)))
	if !r.Valid() {
		return "", fmt.Errorf("%w: %q (want one of %s)", ErrUnknownRole, s, strings.Join(roleNames(), ", "))
	}
	return r, nil
}

func roleNames() []string {
	out := make([]string, 0, len(AllRoles))
	for _, r := range AllRoles {
		out = append(out, string(r))
	}
	return out
}

// ErrUnknownRole is returned for a role string outside the three-tier model.
var ErrUnknownRole = errors.New("auth: unknown role")

// Permission is a fine-grained capability checked by the RBAC middleware.
//
// Permissions are grouped per resource area so that the instance-level grant
// table (user_instance_grant.perm = read/control/config) can be layered on top
// without changing handler code.
type Permission string

// Permission constants. The naming follows "<area>:<verb>" so that a grant can
// be matched by prefix when a coarse check is enough.
const (
	// PermPanelAdmin gates panel-wide administration (users, system, jobs).
	PermPanelAdmin Permission = "panel:admin"
	// PermSystemRead gates /system/info.
	PermSystemRead Permission = "system:read"

	// PermInstanceRead gates listing and reading instances.
	PermInstanceRead Permission = "instance:read"
	// PermInstanceWrite gates creating/deleting instances.
	PermInstanceWrite Permission = "instance:write"
	// PermInstanceControl gates start/stop/restart and console commands.
	PermInstanceControl Permission = "instance:control"
	// PermInstanceConfig gates config WRITES and world mutation.
	//
	// It is deliberately separate from PermInstanceConfigRead: an earlier
	// design reused one permission for both, which silently let a viewer write
	// configuration. Read and write are always distinct permissions here.
	PermInstanceConfig Permission = "instance:config"
	// PermInstanceConfigRead gates reading an instance's configuration.
	PermInstanceConfigRead Permission = "instance:config:read"
	// PermInstanceFile gates file WRITES (upload, mkdir, rename, delete, unzip).
	//
	// Separate from PermInstanceFileRead for the same reason as above.
	PermInstanceFile Permission = "instance:file"
	// PermInstanceFileRead gates listing and downloading files.
	PermInstanceFileRead Permission = "instance:file:read"
	// PermInstanceBackup gates backup create/restore/delete.
	PermInstanceBackup Permission = "instance:backup"
	// PermInstanceBackupRead gates listing backups.
	PermInstanceBackupRead Permission = "instance:backup:read"
	// PermInstanceLogRead gates reading and downloading logs.
	PermInstanceLogRead Permission = "instance:log:read"

	// PermUserManage gates /users CRUD.
	PermUserManage Permission = "user:manage"
	// PermAuditRead gates /audit.
	PermAuditRead Permission = "audit:read"
	// PermJobManage gates /jobs CRUD.
	PermJobManage Permission = "job:manage"
)

// rolePermissions is the default permission set granted by each role.
//
// viewer  : read-only everywhere it is allowed to look.
// operator: everything a viewer can do, plus control/config/file/backup on
//
//	instances it can see.
//
// admin   : everything.
var rolePermissions = map[Role]map[Permission]bool{
	// viewer: read-only. It holds ONLY *:read permissions; every mutating
	// permission is absent, so the role check alone denies writes regardless of
	// what the instance-grant table contains.
	RoleViewer: {
		PermSystemRead:         true,
		PermInstanceRead:       true,
		PermInstanceConfigRead: true,
		PermInstanceFileRead:   true,
		PermInstanceBackupRead: true,
		PermInstanceLogRead:    true,
	},
	RoleOperator: {
		PermSystemRead:         true,
		PermInstanceRead:       true,
		PermInstanceWrite:      true,
		PermInstanceControl:    true,
		PermInstanceConfig:     true,
		PermInstanceConfigRead: true,
		PermInstanceFile:       true,
		PermInstanceFileRead:   true,
		PermInstanceBackup:     true,
		PermInstanceBackupRead: true,
		PermInstanceLogRead:    true,
		PermAuditRead:          true,
		PermJobManage:          true,
	},
	RoleAdmin: {
		PermSystemRead:         true,
		PermInstanceRead:       true,
		PermInstanceWrite:      true,
		PermInstanceControl:    true,
		PermInstanceConfig:     true,
		PermInstanceConfigRead: true,
		PermInstanceFile:       true,
		PermInstanceFileRead:   true,
		PermInstanceBackup:     true,
		PermInstanceBackupRead: true,
		PermInstanceLogRead:    true,
		PermAuditRead:          true,
		PermJobManage:          true,
		PermUserManage:         true,
		PermPanelAdmin:         true,
	},
}

// readOnlyPermissions lists every permission that does NOT mutate state. The
// viewer role may hold only these, and IsReadOnly uses the table to assert that
// invariant in tests.
var readOnlyPermissions = map[Permission]bool{
	PermSystemRead:         true,
	PermInstanceRead:       true,
	PermInstanceConfigRead: true,
	PermInstanceFileRead:   true,
	PermInstanceBackupRead: true,
	PermInstanceLogRead:    true,
	PermAuditRead:          true,
}

// Grant is a single instance-level permission row
// (user_instance_grant in §5.5). In single-user mode the table is always empty
// and the owner implicitly holds every grant.
type Grant struct {
	UserID     int64
	InstanceID int64
	Perm       GrantPerm
}

// GrantPerm is the coarse permission stored in user_instance_grant.perm.
type GrantPerm string

// The three grant levels from the schema comment: read/control/config.
const (
	// GrantRead allows viewing the instance.
	GrantRead GrantPerm = "read"
	// GrantControl allows start/stop/restart/console.
	GrantControl GrantPerm = "control"
	// GrantConfig allows config and file mutation.
	GrantConfig GrantPerm = "config"
)

// Valid reports whether g is one of read/control/config.
func (g GrantPerm) Valid() bool {
	switch g {
	case GrantRead, GrantControl, GrantConfig:
		return true
	}
	return false
}

// PermissionSet is an immutable set of granted permissions.
type PermissionSet struct {
	perms map[Permission]bool
	role  Role
}

// NewPermissionSet builds the permission set for a role.
func NewPermissionSet(role Role) PermissionSet {
	src := rolePermissions[role]
	dst := make(map[Permission]bool, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return PermissionSet{perms: dst, role: role}
}

// Role returns the role the set was built from.
func (p PermissionSet) Role() Role { return p.role }

// Has reports whether the set contains perm.
func (p PermissionSet) Has(perm Permission) bool { return p.perms[perm] }

// List returns the granted permissions in a stable, sorted order.
func (p PermissionSet) List() []Permission {
	out := make([]Permission, 0, len(p.perms))
	for k, v := range p.perms {
		if v {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// IsReadOnly reports whether the set holds no mutating permission.
func (p PermissionSet) IsReadOnly() bool {
	for k, v := range p.perms {
		if v && !readOnlyPermissions[k] {
			return false
		}
	}
	return true
}

// Can answers "may this role perform perm?".
//
// Viewers are additionally blocked from every mutating permission even if the
// table above were to grow a mistake: the read-only check is applied last and
// cannot be overridden by data.
func (r Role) Can(perm Permission) bool {
	set := rolePermissions[r]
	if set == nil || !set[perm] {
		return false
	}
	if r == RoleViewer && !readOnlyPermissions[perm] {
		return false
	}
	return true
}

// Authorizer combines role-based permissions with instance-level grants.
//
// The zero value is not usable; construct one with NewAuthorizer.
type Authorizer struct {
	// singleUser mirrors the panel's D3 single-user mode. When true, a user
	// implicitly owns (and may act on) every instance, which is what makes
	// the grant middleware a no-op in the shipped configuration while still
	// exercising the same code path.
	singleUser bool
	// grants maps userID -> instanceID -> grant level. In single-user mode
	// this map is empty and never consulted.
	grants map[int64]map[int64]GrantPerm
}

// NewAuthorizer builds an Authorizer. singleUser should be true for the
// shipped single-admin configuration.
func NewAuthorizer(singleUser bool) *Authorizer {
	return &Authorizer{singleUser: singleUser, grants: map[int64]map[int64]GrantPerm{}}
}

// SingleUser reports whether the authorizer is in single-user mode.
func (a *Authorizer) SingleUser() bool { return a.singleUser }

// SetGrant records an instance-level grant. It is a no-op in single-user mode,
// matching the "table stays empty" rule from §5.5.
func (a *Authorizer) SetGrant(userID, instanceID int64, perm GrantPerm) {
	if a.singleUser {
		return
	}
	if a.grants[userID] == nil {
		a.grants[userID] = map[int64]GrantPerm{}
	}
	// Grant levels are cumulative: control implies read, config implies read.
	existing := a.grants[userID][instanceID]
	if grantRank(perm) > grantRank(existing) || existing == "" {
		a.grants[userID][instanceID] = perm
	}
}

// GrantsFor returns the grant level a user holds on an instance, if any.
func (a *Authorizer) GrantsFor(userID, instanceID int64) (GrantPerm, bool) {
	if a.singleUser {
		return GrantConfig, true
	}
	m := a.grants[userID]
	if m == nil {
		return "", false
	}
	g, ok := m[instanceID]
	return g, ok
}

func grantRank(g GrantPerm) int {
	switch g {
	case GrantRead:
		return 1
	case GrantControl:
		return 2
	case GrantConfig:
		return 3
	}
	return 0
}

// ErrForbidden is returned when an authenticated principal lacks the required
// permission. Handlers map it to HTTP 403.
var ErrForbidden = errors.New("auth: permission denied")

// Authorize checks a panel-level permission.
func (a *Authorizer) Authorize(role Role, perm Permission) error {
	if !role.Valid() {
		return fmt.Errorf("%w: %v", ErrForbidden, ErrUnknownRole)
	}
	if !role.Can(perm) {
		return fmt.Errorf("%w: role %q lacks %q", ErrForbidden, role, perm)
	}
	return nil
}

// AuthorizeInstance checks a permission scoped to a single instance, applying
// the instance-level grant table on top of the role check.
//
// Admins bypass grant checks (they administer the panel). Everyone else needs
// an explicit grant when not in single-user mode. ownerID is the instance's
// owner (instances.owner_id); the owner always has full access to their own
// instance, which is what makes single-user mode transparent.
func (a *Authorizer) AuthorizeInstance(role Role, userID, ownerID, instanceID int64, perm Permission) error {
	if err := a.Authorize(role, perm); err != nil {
		return err
	}
	if a.singleUser || role == RoleAdmin {
		return nil
	}
	if ownerID != 0 && ownerID == userID {
		return nil
	}

	g, ok := a.GrantsFor(userID, instanceID)
	if !ok {
		return fmt.Errorf("%w: no grant on instance %d", ErrForbidden, instanceID)
	}
	if err := grantSatisfies(g, perm); err != nil {
		return fmt.Errorf("%w: grant %q on instance %d insufficient: %v", ErrForbidden, g, instanceID, err)
	}
	return nil
}

// grantSatisfies maps a fine-grained permission onto the minimum grant level.
func grantSatisfies(g GrantPerm, perm Permission) error {
	required := GrantRead
	switch perm {
	case PermInstanceControl:
		required = GrantControl
	case PermInstanceConfig, PermInstanceFile, PermInstanceBackup, PermInstanceWrite:
		required = GrantConfig
	case PermInstanceConfigRead, PermInstanceFileRead, PermInstanceBackupRead,
		PermInstanceLogRead, PermInstanceRead, PermSystemRead:
		required = GrantRead
	default:
		// Panel-level permissions are never satisfiable by an instance grant.
		return fmt.Errorf("permission %q is not instance-scoped", perm)
	}
	if grantRank(g) < grantRank(required) {
		return fmt.Errorf("need %q", required)
	}
	return nil
}
