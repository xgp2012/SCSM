package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"scnetm/internal/auth"
)

// Error codes. These are the machine-readable half of the error envelope; the
// frontend switches on them, so they are part of the API contract and must stay
// stable even if messages are reworded.
const (
	// CodeBadRequest covers malformed JSON, missing parameters and other
	// client mistakes that are not field-validation failures.
	CodeBadRequest = "bad_request"
	// CodeValidationFailed is the 422 code for a well-formed request whose
	// values fail validation (bad port, illegal world directory name...).
	CodeValidationFailed = "validation_failed"
	// CodeUnauthorized covers a missing or invalid bearer token.
	CodeUnauthorized = "unauthorized"
	// CodeInvalidCredentials is a 401 with a *distinct* code so the login form
	// can say "wrong username or password" without leaking which was wrong.
	CodeInvalidCredentials = "invalid_credentials"
	// CodeAccountLocked is the 429 code returned while an account is in
	// lockout after repeated failed logins.
	CodeAccountLocked = "account_locked"
	// CodeSetupRequired is the first-run code: the seeded admin has no
	// password yet, so login is impossible until POST /auth/setup succeeds.
	CodeSetupRequired = "setup_required"
	// CodeForbidden is the 403 code for an authenticated principal without
	// the required permission.
	CodeForbidden = "forbidden"
	// CodeNotFound is the 404 code.
	CodeNotFound = "not_found"
	// CodeConflict is the 409 code for state conflicts: starting an
	// already-running instance, duplicate instance name, duplicate username.
	CodeConflict = "conflict"
	// CodeRateLimited is the 429 code for a limiter rejection.
	CodeRateLimited = "rate_limited"
	// CodePayloadTooLarge is the 413 code for an oversize upload or body.
	CodePayloadTooLarge = "payload_too_large"
	// CodeTimeout is the 504 code for an operation that exceeded its deadline
	// (a stop that never completed).
	CodeTimeout = "operation_timeout"
	// CodeNotImplemented is the 501 code returned when the backend that owns
	// a feature is a nop in this build.
	CodeNotImplemented = "not_implemented"
	// CodeUnavailable is the 503 code for a temporarily unusable dependency.
	CodeUnavailable = "unavailable"
	// CodeInternal is the 500 code.
	CodeInternal = "internal_error"
)

// ErrorBody is the "error" object of the envelope.
type ErrorBody struct {
	// Code is the stable machine-readable identifier (see the constants
	// above).
	Code string `json:"code"`
	// Message is a human-readable, safe-to-display explanation.
	Message string `json:"message"`
	// Details carries structured extras: per-field validation issues, the
	// current instance state on a conflict, the retry-after on a rate limit.
	Details any `json:"details,omitempty"`
}

// ErrorEnvelope is the single error shape used by every endpoint (§5.6/§5.7):
//
//	{"error":{"code":"...","message":"...","details":...}}
type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
	// RequestID echoes the X-Request-Id header so a user can quote it in a bug
	// report and an operator can find the matching log line.
	RequestID string `json:"request_id,omitempty"`
}

// APIError is a typed error that carries an HTTP status and an error code.
//
// Handlers either return one of the sentinel-bearing errors below (which
// Fail maps automatically) or construct an APIError directly for a bespoke
// case.
type APIError struct {
	// Status is the HTTP status code to send.
	Status int
	// Code is the machine-readable code.
	Code string
	// Message is the user-facing message.
	Message string
	// Details is optional structured context.
	Details any
	// Err is the wrapped cause, used for logging (never rendered to clients,
	// so it may contain filesystem paths).
	Err error
}

// Error implements error.
func (e *APIError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s (%d): %s: %v", e.Code, e.Status, e.Message, e.Err)
	}
	return fmt.Sprintf("%s (%d): %s", e.Code, e.Status, e.Message)
}

// Unwrap exposes the wrapped cause to errors.Is/As.
func (e *APIError) Unwrap() error { return e.Err }

// WithDetail returns a copy of e carrying details.
func (e *APIError) WithDetail(d any) *APIError {
	cp := *e
	cp.Details = d
	return &cp
}

// WithCause returns a copy of e carrying a wrapped cause.
func (e *APIError) WithCause(err error) *APIError {
	cp := *e
	cp.Err = err
	return &cp
}

// Constructors for the common cases. They are functions (not vars) so that
// callers can safely append details without mutating shared state.

// BadRequest returns a 400.
func BadRequest(msg string, args ...any) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: CodeBadRequest, Message: sprintf(msg, args...)}
}

// ValidationFailed returns a 422 with optional per-field details.
func ValidationFailed(msg string, args ...any) *APIError {
	return &APIError{Status: http.StatusUnprocessableEntity, Code: CodeValidationFailed, Message: sprintf(msg, args...)}
}

// Unauthorized returns a 401.
func Unauthorized(msg string, args ...any) *APIError {
	return &APIError{Status: http.StatusUnauthorized, Code: CodeUnauthorized, Message: sprintf(msg, args...)}
}

// InvalidCredentials returns a 401 with the login-specific code.
func InvalidCredentials() *APIError {
	return &APIError{
		Status:  http.StatusUnauthorized,
		Code:    CodeInvalidCredentials,
		Message: "invalid username or password",
	}
}

// SetupRequired returns a 409 with the first-run code. 409 (not 401) is used
// because the request is well-formed and the credentials may even be correct —
// the panel simply is not initialised yet.
func SetupRequired() *APIError {
	return &APIError{
		Status:  http.StatusConflict,
		Code:    CodeSetupRequired,
		Message: "panel setup is not complete: set the administrator password via POST /api/v1/auth/setup",
	}
}

// AccountLocked returns a 429 for a locked-out account.
func AccountLocked(retryAfterSeconds int) *APIError {
	return &APIError{
		Status:  http.StatusTooManyRequests,
		Code:    CodeAccountLocked,
		Message: "too many failed login attempts; the account is temporarily locked",
		Details: gin.H{"retry_after_seconds": retryAfterSeconds},
	}
}

// Forbidden returns a 403.
func Forbidden(msg string, args ...any) *APIError {
	return &APIError{Status: http.StatusForbidden, Code: CodeForbidden, Message: sprintf(msg, args...)}
}

// NotFound returns a 404.
func NotFound(msg string, args ...any) *APIError {
	return &APIError{Status: http.StatusNotFound, Code: CodeNotFound, Message: sprintf(msg, args...)}
}

// Conflict returns a 409. This is the code the plan mandates for state
// conflicts such as "start an instance that is already running".
func Conflict(msg string, args ...any) *APIError {
	return &APIError{Status: http.StatusConflict, Code: CodeConflict, Message: sprintf(msg, args...)}
}

// RateLimited returns a 429 for a limiter rejection.
func RateLimited(retryAfterSeconds int) *APIError {
	return &APIError{
		Status:  http.StatusTooManyRequests,
		Code:    CodeRateLimited,
		Message: "rate limit exceeded",
		Details: gin.H{"retry_after_seconds": retryAfterSeconds},
	}
}

// PayloadTooLarge returns a 413.
func PayloadTooLarge(msg string, args ...any) *APIError {
	return &APIError{Status: http.StatusRequestEntityTooLarge, Code: CodePayloadTooLarge, Message: sprintf(msg, args...)}
}

// Timeout returns a 504. This is the code the plan mandates for an operation
// that exceeded its deadline (e.g. a graceful stop that never finished).
func Timeout(msg string, args ...any) *APIError {
	return &APIError{Status: http.StatusGatewayTimeout, Code: CodeTimeout, Message: sprintf(msg, args...)}
}

// NotImplemented returns a 501 for a feature whose backend is a nop in this
// build.
func NotImplemented(what string) *APIError {
	return &APIError{
		Status:  http.StatusNotImplemented,
		Code:    CodeNotImplemented,
		Message: sprintf("%s is not implemented in this build", what),
	}
}

// Unavailable returns a 503.
func Unavailable(msg string, args ...any) *APIError {
	return &APIError{Status: http.StatusServiceUnavailable, Code: CodeUnavailable, Message: sprintf(msg, args...)}
}

// Internal returns a 500. The cause is logged but never sent to the client.
func Internal(err error) *APIError {
	return &APIError{
		Status:  http.StatusInternalServerError,
		Code:    CodeInternal,
		Message: "internal server error",
		Err:     err,
	}
}

func sprintf(msg string, args ...any) string {
	if len(args) == 0 {
		return msg
	}
	return fmt.Sprintf(msg, args...)
}

// Classify maps a plain error from a store or service onto an APIError using
// the shared sentinels. Handlers call this for every dependency error so the
// status-code contract (409 conflict / 422 validation / 504 timeout / 501 not
// implemented) is applied in exactly one place.
//
// notFoundMsg is used when err is ErrNotFound.
func Classify(err error, notFoundMsg string) *APIError {
	if err == nil {
		return nil
	}

	// An APIError already carries its status.
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}

	switch {
	case errors.Is(err, ErrNotFound):
		return NotFound("%s", notFoundMsg)
	case errors.Is(err, ErrConflict):
		return Conflict("%s", err.Error())
	case errors.Is(err, ErrInvalid):
		return ValidationFailed("%s", err.Error())
	case errors.Is(err, ErrNotImplemented):
		return &APIError{
			Status:  http.StatusNotImplemented,
			Code:    CodeNotImplemented,
			Message: err.Error(),
		}
	case errors.Is(err, ErrUnavailable):
		return Unavailable("%s", err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return Timeout("operation timed out")
	case errors.Is(err, context.Canceled):
		// The client went away; 499 is the nginx convention and is not a real
		// status code, so use 408 for a request-scoped timeout instead.
		return &APIError{
			Status:  http.StatusRequestTimeout,
			Code:    "request_canceled",
			Message: "the request was canceled",
			Err:     err,
		}
	case errors.Is(err, auth.ErrForbidden):
		return Forbidden("%s", err.Error())
	case errors.Is(err, auth.ErrTokenInvalid), errors.Is(err, auth.ErrTokenAlgorithm):
		return Unauthorized("%s", err.Error())
	case errors.Is(err, auth.ErrTokenRevoked):
		return Unauthorized("session has been logged out")
	}
	return Internal(err)
}

// IsNotImplemented reports whether err is (or wraps) the not-implemented
// sentinel or an APIError with the 501 code. Handlers use it to decide whether
// to emit the machine-readable "not implemented in this build" hint.
func IsNotImplemented(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNotImplemented) {
		return true
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == http.StatusNotImplemented || apiErr.Code == CodeNotImplemented
	}
	return false
}

// Abort writes the unified error envelope and stops the handler chain.
//
// Every handler should funnel its failures through Fail/Abort so the response
// shape is uniform; the frontend then never has to guess between a Gin default
// 400 body and a panel error body.
func Abort(c *gin.Context, err error) {
	apiErr := Classify(err, "resource not found")
	Fail(c, apiErr)
}

// Fail writes apiErr as the JSON envelope with its status code and aborts.
//
// A nil apiErr is a programming error and is treated as a 500 rather than
// silently producing an empty body.
func Fail(c *gin.Context, apiErr *APIError) {
	if apiErr == nil {
		apiErr = Internal(errors.New("nil *APIError passed to Fail"))
	}

	// 5xx causes are logged with the request id; they are never echoed.
	if apiErr.Status >= http.StatusInternalServerError {
		args := []any{
			"status", apiErr.Status,
			"code", apiErr.Code,
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"request_id", RequestIDFrom(c),
		}
		if apiErr.Err != nil {
			args = append(args, "err", apiErr.Err.Error())
		} else {
			args = append(args, "message", apiErr.Message)
		}
		loggerFrom(c).Error("request failed", args...)
	}

	body := ErrorEnvelope{
		Error: ErrorBody{
			Code:    apiErr.Code,
			Message: apiErr.Message,
			Details: apiErr.Details,
		},
		RequestID: RequestIDFrom(c),
	}
	c.AbortWithStatusJSON(apiErr.Status, body)
}

// FailNotImplemented is the standard 501 response used by handlers whose
// backend is a nop. It always names the feature so the message is actionable
// even when the frontend ignores the details object.
func FailNotImplemented(c *gin.Context, feature string, cause error) {
	apiErr := NotImplemented(feature)
	if cause != nil {
		apiErr = apiErr.WithCause(cause)
	}
	apiErr.Details = gin.H{
		"feature": feature,
		"reason":  "the backend package that owns this feature is not wired in this build",
	}
	Fail(c, apiErr)
}
