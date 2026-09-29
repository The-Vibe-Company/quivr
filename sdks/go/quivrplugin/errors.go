package quivrplugin

import (
	"errors"
	"fmt"
	"time"
)

// Class is a connector error class. The core maps it to Connector Health:
// access to access_error, transient to a retry with backoff, source to a
// source error.
type Class string

const (
	// ClassAccess: the source refuses the credential or the access. Terminal
	// until the operator deposits a new credential or changes the access.
	ClassAccess Class = "access"
	// ClassTransient: an outage, a timeout or a rate limit. Retried.
	ClassTransient Class = "transient"
	// ClassSource: the source returned data the plugin cannot use. Terminal
	// for this run.
	ClassSource Class = "source"
)

// Error is a classified connector failure. Code is a stable diagnostic
// (lowercase, digits and underscores); Message is for humans and must not
// contain source data or credentials (the SDK still scrubs credential values
// from it). RetryAfter, for a transient error, defers the next run, such as a
// rate-limit reset.
type Error struct {
	Class      Class
	Code       string
	Message    string
	RetryAfter time.Duration
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s: %s", e.Class, e.Code, e.Message) }

// WithRetryAfter sets the delay after which the source accepts requests again.
func (e *Error) WithRetryAfter(d time.Duration) *Error {
	e.RetryAfter = d
	return e
}

// AccessError reports that the source refuses the credential or the access.
func AccessError(code, message string) *Error {
	return &Error{Class: ClassAccess, Code: code, Message: message}
}

// TransientError reports an outage, a timeout or a rate limit.
func TransientError(code, message string) *Error {
	return &Error{Class: ClassTransient, Code: code, Message: message}
}

// SourceError reports that the source returned data the plugin cannot use.
func SourceError(code, message string) *Error {
	return &Error{Class: ClassSource, Code: code, Message: message}
}

// ErrNotDue, returned by Fetch, reports that the source asked not to be
// polled yet (for example an RSS ttl). The SDK answers not_due with the
// request's checkpoint unchanged, and the core skips the run.
var ErrNotDue = errors.New("not_due")

// envelope is the Plugin Protocol error envelope.
type envelope struct {
	Code              string `json:"code"`
	Message           string `json:"message"`
	Retryable         bool   `json:"retryable"`
	Class             Class  `json:"error_class,omitempty"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
}

func (e *Error) envelope() (int, envelope) {
	env := envelope{Code: e.Code, Message: e.Message, Class: e.Class}
	if env.Message == "" {
		env.Message = e.Code
	}
	status := 422
	switch e.Class {
	case ClassAccess:
		status = 403
	case ClassTransient:
		status, env.Retryable = 503, true
		if e.RetryAfter > 0 {
			env.RetryAfterSeconds = min(max(int((e.RetryAfter+time.Second-1)/time.Second), 1), 86400)
		}
	}
	return status, env
}
