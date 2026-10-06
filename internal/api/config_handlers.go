package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// This file implements the configuration endpoints (§6.2).
//
// The PUT semantics required by the task are strict and ordered:
//
//	back up first → validate → reject on error with 422 → otherwise write
//	→ report whether a restart is needed
//
// The "reject on error means the original file is unchanged" property is
// guaranteed by validating *before* delegating the write, so a rejected document
// never reaches the ConfigService's writer at all. The service additionally
// takes its own backup before overwriting, which is what makes the operation
// reversible if a later step fails.

// resolveConfigKind maps the request's kind/file pair onto a ConfigKind,
// defaulting to ServerSetting.json.
func resolveConfigKind(kind, file string) (ConfigKind, error) {
	k := strings.TrimSpace(kind)
	if k == "" {
		k = string(ConfigKindServerSetting)
	}
	switch ConfigKind(k) {
	case ConfigKindServerSetting, ConfigKindSettings:
		return ConfigKind(k), nil
	case ConfigKindConfigs:
		if strings.TrimSpace(file) != "" {
			// A specific plugin config, addressed by name. The name is
			// validated as a bare filename: no separators, no "..", because it
			// will be joined to Configs/.
			if err := validateConfigFilename(file); err != nil {
				return "", err
			}
		}
		return ConfigKindConfigs, nil
	default:
		return "", fmt.Errorf("%w: unknown config kind %q (want ServerSetting.json, Settings.xml or Configs)", ErrInvalid, kind)
	}
}

// validateConfigFilename rejects anything that is not a bare .json filename.
func validateConfigFilename(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("%w: config file name is required", ErrInvalid)
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return fmt.Errorf("%w: config file name must not contain path separators", ErrInvalid)
	}
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		return fmt.Errorf("%w: config file must be a .json file", ErrInvalid)
	}
	if len(name) > 128 {
		return fmt.Errorf("%w: config file name is too long", ErrInvalid)
	}
	return nil
}

// handleGetConfig reads one configuration document.
func (s *Server) handleGetConfig(c *gin.Context) {
	inst := MustInstance(c)

	kind, err := resolveConfigKind(c.Query("kind"), c.Query("file"))
	if err != nil {
		Fail(c, ValidationFailed("%v", err))
		return
	}

	doc, err := s.deps.Config.Get(c.Request.Context(), inst, kind)
	if err != nil {
		s.failConfig(c, err, inst, "read")
		return
	}

	c.JSON(http.StatusOK, DataResponse{Data: configResponse(doc, nil, false)})
}

// handleValidateConfig validates a proposed document without writing it.
func (s *Server) handleValidateConfig(c *gin.Context) {
	inst := MustInstance(c)

	var req ValidateConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, BadRequest("invalid validate request: %v", err))
		return
	}

	kind, err := resolveConfigKind(req.Kind, req.File)
	if err != nil {
		Fail(c, ValidationFailed("%v", err))
		return
	}

	content, err := encodeConfigContent(req.JSONContent, req.Text)
	if err != nil {
		Fail(c, ValidationFailed("%v", err))
		return
	}

	result, err := s.deps.Config.Validate(c.Request.Context(), inst, kind, content)
	if err != nil {
		s.failConfig(c, err, inst, "validate")
		return
	}

	// A validate endpoint always answers 200 with the verdict in the body:
	// "invalid" is a successful validation, not a failed request. Callers that
	// want a status code use PUT.
	c.JSON(http.StatusOK, DataResponse{Data: result})
}

// handlePutConfig validates then writes a configuration document.
//
// Ordering is the contract:
//  1. build the candidate bytes,
//  2. validate them (a validation *error* → 422, file untouched),
//  3. hand off to Write, which takes a backup before overwriting,
//  4. report RequiresRestart so the UI can offer "restart now".
func (s *Server) handlePutConfig(c *gin.Context) {
	inst := MustInstance(c)
	ctx := c.Request.Context()

	var req UpdateConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, BadRequest("invalid config update request: %v", err))
		return
	}

	kind, err := resolveConfigKind(req.Kind, req.File)
	if err != nil {
		Fail(c, ValidationFailed("%v", err))
		return
	}

	content, err := encodeConfigContent(req.JSONContent, req.Text)
	if err != nil {
		Fail(c, ValidationFailed("%v", err))
		return
	}

	// A JSON document must be well-formed JSON *before* it reaches the
	// validator or the store.
	//
	// The validator is allowed to be lenient (and a build with no validator at
	// all must still be safe), so this is enforced here rather than delegated:
	// writing a malformed ServerSetting.json would leave the game server unable
	// to start, which is the worst possible outcome for a config editor. The
	// check is syntactic only — every semantic rule stays with the validator.
	if isJSONKind(kind) && !json.Valid(stripBOM(content)) {
		s.audit(c, "config.write_rejected", fmt.Sprintf("instance:%d", inst.ID), gin.H{
			"kind":   string(kind),
			"reason": "content is not valid JSON",
		})
		Fail(c, ValidationFailed("the body is not valid JSON and was not written").
			WithDetail(gin.H{
				"kind":    string(kind),
				"written": false,
				"issues": []ValidationIssue{{
					Field:    "content",
					Code:     "invalid_json",
					Message:  "the document must be a single well-formed JSON object",
					Severity: "error",
				}},
			}))
		return
	}

	// Fetch the current document first: it is what the backup preserves, and it
	// lets the audit row record a real before/after summary.
	before, beforeErr := s.deps.Config.Get(ctx, inst, kind)
	if beforeErr != nil && !IsNotImplemented(beforeErr) && !errors.Is(beforeErr, ErrNotFound) {
		s.failConfig(c, beforeErr, inst, "read")
		return
	}

	// --- step 2: validate before writing ---
	result, verr := s.deps.Config.Validate(ctx, inst, kind, content)
	if verr != nil {
		if IsNotImplemented(verr) {
			// No validator in this build: proceed, but say so in the audit.
			result = &ValidationResult{Valid: true}
		} else {
			s.failConfig(c, verr, inst, "validate")
			return
		}
	}

	if result != nil && hasValidationErrors(result) {
		// The file must not have been touched: we have not called Write.
		s.audit(c, "config.write_rejected", fmt.Sprintf("instance:%d", inst.ID), gin.H{
			"kind":   string(kind),
			"reason": "validation failed",
			"issues": result.Issues,
		})
		Fail(c, ValidationFailed("the configuration is invalid and was not written").
			WithDetail(gin.H{
				"kind":             string(kind),
				"issues":           result.Issues,
				"requires_restart": result.RequiresRestart,
				"written":          false,
			}))
		return
	}

	// --- step 3: write (the service backs up before overwriting) ---
	doc, werr := s.deps.Config.Write(ctx, inst, ConfigUpdate{Kind: kind, Content: content})
	if werr != nil {
		s.failConfig(c, werr, inst, "write")
		return
	}

	// --- step 4: does this need a restart? ---
	restartNeeded := false
	if result != nil {
		restartNeeded = result.RequiresRestart
	}
	if doc != nil && doc.RequiresRestart {
		restartNeeded = true
	}
	// A write while the instance is running always needs a restart to take
	// effect (§6.2 "运行时写 → 标注需重启生效"), regardless of what the
	// validator said.
	if live, ok := s.deps.Process.State(inst.ID); ok && live.State == StateRunning {
		restartNeeded = true
	}

	detail := gin.H{
		"kind":             string(kind),
		"requires_restart": restartNeeded,
		"bytes_written":    len(content),
	}
	if before != nil {
		detail["before_sha256"] = shortHash(before.Content)
		detail["before_bytes"] = len(before.Content)
	}
	detail["after_sha256"] = shortHash(content)
	s.audit(c, "config.write", fmt.Sprintf("instance:%d", inst.ID), detail)

	s.deps.Events.Publish(Event{
		Type:       EventInstance,
		InstanceID: inst.ID,
		Timestamp:  s.now().UTC(),
		Data: gin.H{
			"action":           "config_write",
			"kind":             string(kind),
			"requires_restart": restartNeeded,
		},
	})

	resp := configResponse(doc, result, restartNeeded)

	// Optional immediate restart.
	if req.Restart && restartNeeded {
		if err := s.deps.Process.Restart(ctx, inst.ID); err != nil {
			resp.RestartNeeded = true
			loggerFrom(c).Warn("restart after config write failed", "instance_id", inst.ID, "err", err.Error())
			c.JSON(http.StatusOK, DataResponse{Data: gin.H{
				"config":        resp,
				"restart":       false,
				"restart_error": err.Error(),
			}})
			return
		}
		resp.RestartNeeded = false
		s.audit(c, "instance.restart", fmt.Sprintf("instance:%d", inst.ID), gin.H{"trigger": "config_write"})
	}

	c.JSON(http.StatusOK, DataResponse{Data: resp})
}

// failConfig maps a ConfigService error onto the right status, substituting the
// 501 envelope when the service is a nop in this build.
func (s *Server) failConfig(c *gin.Context, err error, inst *Instance, op string) {
	if IsNotImplemented(err) {
		FailNotImplemented(c, "configuration service", err)
		return
	}
	Fail(c, Classify(err, fmt.Sprintf("configuration for instance %d not found", inst.ID)))
}

// encodeConfigContent turns the request's structured or textual body into the
// exact bytes to write.
//
// Exactly one of the two forms must be supplied: accepting both would leave the
// precedence ambiguous, and accepting neither would silently truncate the file
// to empty.
func encodeConfigContent(obj map[string]any, text *string) ([]byte, error) {
	hasObj := obj != nil
	hasText := text != nil

	switch {
	case hasObj && hasText:
		return nil, fmt.Errorf("%w: supply either \"content\" (JSON) or \"text\", not both", ErrInvalid)
	case hasObj:
		b, err := json.MarshalIndent(obj, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("%w: content is not serialisable: %v", ErrInvalid, err)
		}
		return append(b, '\n'), nil
	case hasText:
		if len(*text) == 0 {
			return nil, fmt.Errorf("%w: text must not be empty", ErrInvalid)
		}
		return []byte(*text), nil
	default:
		return nil, fmt.Errorf("%w: a config body is required (\"content\" or \"text\")", ErrInvalid)
	}
}

// configResponse projects a ConfigDoc onto the response DTO.
func configResponse(doc *ConfigDoc, validated *ValidationResult, restartNeeded bool) ConfigResponse {
	if doc == nil {
		return ConfigResponse{Validated: validated, RestartNeeded: restartNeeded}
	}
	resp := ConfigResponse{
		Kind:            string(doc.Kind),
		Filename:        doc.Filename,
		Content:         doc.RawJSON,
		Text:            doc.Text,
		ModTime:         doc.ModTime,
		RequiresRestart: doc.RequiresRestart,
		Validated:       validated,
		RestartNeeded:   restartNeeded || doc.RequiresRestart,
	}
	// If the service returned only raw bytes (no parsed view), parse JSON here
	// so the UI always gets a structured document when one is possible.
	if resp.Content == nil && resp.Text == "" && len(doc.Content) > 0 {
		if isJSONContentType(doc.Filename, doc.Content) {
			var obj map[string]any
			if err := json.Unmarshal(stripBOM(doc.Content), &obj); err == nil {
				resp.Content = obj
			} else {
				resp.Text = string(doc.Content)
			}
		} else {
			resp.Text = string(doc.Content)
		}
	}
	// A Configs/ document is not a single filename; expose the kind plainly.
	if doc.Kind == ConfigKindConfigs && resp.Filename == "" {
		resp.Filename = "Configs"
	}
	return resp
}

// hasValidationErrors reports whether a result contains at least one
// "error"-severity issue. Warnings do not block a write (they are informational
// — e.g. a GameMode value that is still marked "inferred" per §6.2.1).
func hasValidationErrors(r *ValidationResult) bool {
	if r == nil {
		return false
	}
	if !r.Valid && len(r.Issues) == 0 {
		return true
	}
	for _, issue := range r.Issues {
		if !strings.EqualFold(issue.Severity, "warning") {
			return true
		}
	}
	return false
}

// isJSONKind reports whether a config kind is stored as JSON, and therefore
// must be well-formed JSON on write.
func isJSONKind(kind ConfigKind) bool {
	switch kind {
	case ConfigKindServerSetting, ConfigKindConfigs:
		return true
	default:
		// Settings.xml and any future XML/INI document are text.
		return false
	}
}

func isJSONContentType(name string, content []byte) bool {
	if strings.HasSuffix(strings.ToLower(name), ".json") {
		return true
	}
	trimmed := stripBOM(content)
	trimmed = []byte(strings.TrimSpace(string(trimmed)))
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
}

// stripBOM removes a UTF-8 byte order mark.
//
// §6.3 requires this for Project.json ("读各目录的 Project.json（去 BOM）"), and
// the same applies to any file the user may have edited on Windows.
func stripBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

// shortHash is a stable, short content fingerprint for audit rows.
//
// It is intentionally NOT a cryptographic digest of the whole file (that would
// bloat the audit log); it exists so an operator can answer "did this write
// actually change anything?" without diffing.
func shortHash(content []byte) string {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	var h uint64 = offset64
	for _, b := range stripBOM(content) {
		h ^= uint64(b)
		h *= prime64
	}
	return fmt.Sprintf("fnv64:%016x:len%d", h, len(content))
}

// loadConfigContentOrEmpty is a small helper used by world activation, which
// needs to read ServerSetting.json through the same safe path as the config
// endpoints.
func (s *Server) loadConfigContentOrEmpty(ctx context.Context, inst *Instance, kind ConfigKind) ([]byte, error) {
	doc, err := s.deps.Config.Get(ctx, inst, kind)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, ErrNotFound
	}
	return doc.Content, nil
}
