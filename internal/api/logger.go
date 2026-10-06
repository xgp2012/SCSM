package api

import (
	"context"
	"io"
	"log/slog"
	"os"
	"time"
)

// Logger is the structured logging seam used by the middleware chain and the
// WebSocket hubs.
//
// It is deliberately narrower than *slog.Logger: the API layer only needs
// levelled, key/value logging, and a narrow interface keeps tests silent and
// lets the host decide on formatting. SlogLogger adapts *slog.Logger to it.
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
	// With returns a logger with the given attributes bound.
	With(args ...any) Logger
}

// SlogLogger adapts a *slog.Logger to the Logger interface.
type SlogLogger struct{ l *slog.Logger }

// NewSlogLogger wraps l. A nil l means "JSON to stderr at Info level", which is
// the panel's default in production.
func NewSlogLogger(l *slog.Logger) *SlogLogger {
	if l == nil {
		l = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	return &SlogLogger{l: l}
}

// Debug logs at debug level.
func (s *SlogLogger) Debug(msg string, args ...any) { s.l.Debug(msg, args...) }

// Info logs at info level.
func (s *SlogLogger) Info(msg string, args ...any) { s.l.Info(msg, args...) }

// Warn logs at warn level.
func (s *SlogLogger) Warn(msg string, args ...any) { s.l.Warn(msg, args...) }

// Error logs at error level.
func (s *SlogLogger) Error(msg string, args ...any) { s.l.Error(msg, args...) }

// With binds attributes and returns the derived logger.
func (s *SlogLogger) With(args ...any) Logger { return &SlogLogger{l: s.l.With(args...)} }

// DiscardLogger drops every record. Used by tests that do not assert on logs.
type DiscardLogger struct{}

// Debug implements Logger.
func (DiscardLogger) Debug(string, ...any) {}

// Info implements Logger.
func (DiscardLogger) Info(string, ...any) {}

// Warn implements Logger.
func (DiscardLogger) Warn(string, ...any) {}

// Error implements Logger.
func (DiscardLogger) Error(string, ...any) {}

// With implements Logger.
func (d DiscardLogger) With(...any) Logger { return d }

// NewDiscardLogger returns a logger that writes nothing.
func NewDiscardLogger() Logger { return DiscardLogger{} }

// NopWriter is an io.Writer that discards everything, for test harnesses that
// need to satisfy an io.Writer without pulling in io.Discard semantics.
type NopWriter struct{}

// Write implements io.Writer.
func (NopWriter) Write(p []byte) (int, error) { return len(p), nil }

var (
	_ Logger    = (*SlogLogger)(nil)
	_ Logger    = DiscardLogger{}
	_ io.Writer = NopWriter{}
	_           = time.Now
	_           = context.Background
)
