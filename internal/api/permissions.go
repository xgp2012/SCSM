package api

import "scnetm/internal/auth"

// This file gives the route table short, readable names for the RBAC
// permissions, so that registerRoutes reads as a security policy rather than a
// wall of package-qualified identifiers.
//
// They are aliases (not copies) of the auth constants: there is exactly one
// definition of each permission string, and a typo is a compile error.

// permissionAlias is the type of the permission constants exposed here.
type permissionAlias = auth.Permission

const (
	// permPanelAdmin gates panel-wide administration.
	permPanelAdmin = auth.PermPanelAdmin
	// permSystemRead gates /system/info.
	permSystemRead = auth.PermSystemRead
	// permInstanceRead gates listing and reading instances.
	permInstanceRead = auth.PermInstanceRead
	// permInstanceWrite gates creating, updating and deleting instances.
	permInstanceWrite = auth.PermInstanceWrite
	// permInstanceControl gates start/stop/restart and console commands.
	permInstanceControl = auth.PermInstanceControl
	// permInstanceConfig gates configuration and world mutation.
	permInstanceConfig = auth.PermInstanceConfig
	// permInstanceConfigRead gates reading configuration (viewer-safe).
	permInstanceConfigRead = auth.PermInstanceConfigRead
	// permInstanceFile gates file mutation.
	permInstanceFile = auth.PermInstanceFile
	// permInstanceFileRead gates listing and downloading files (viewer-safe).
	permInstanceFileRead = auth.PermInstanceFileRead
	// permInstanceBackup gates backup create/restore/delete.
	permInstanceBackup = auth.PermInstanceBackup
	// permInstanceBackupRead gates listing backups (viewer-safe).
	permInstanceBackupRead = auth.PermInstanceBackupRead
	// permInstanceLogRead gates reading and downloading logs (viewer-safe).
	permInstanceLogRead = auth.PermInstanceLogRead
	// permUserManage gates /users CRUD.
	permUserManage = auth.PermUserManage
	// permAuditRead gates /audit.
	permAuditRead = auth.PermAuditRead
	// permJobManage gates /jobs CRUD.
	permJobManage = auth.PermJobManage
)
