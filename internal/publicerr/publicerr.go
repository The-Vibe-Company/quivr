// Package publicerr gives domain errors a stable public code. Transports
// resolve the code with Code, never from Error text, so wrapping an error with
// explanatory detail can never change what an API client sees.
package publicerr

import (
	"errors"
	"fmt"
)

// Error is the canonical sentinel for a public code and its response class.
// Request-specific fields are carried by wrappers, leaving sentinels immutable.
type Error struct {
	code         string
	responseCode string
	class        Class
	retryable    bool
}

// New resolves a declared public code. Unknown codes are programming errors;
// add their response class to the catalog before using them.
func New(code string) *Error {
	e, ok := catalog[code]
	if !ok {
		panic("undeclared public error code: " + code)
	}
	return e
}

func (e *Error) Error() string { return e.code }

// Code returns the public code.
func (e *Error) Code() string { return e.code }

// ResponseCode is the established HTTP envelope code. A domain condition can
// retain its identity while sharing a transport's unavailable response.
func (e *Error) ResponseCode() string {
	if e.responseCode != "" {
		return e.responseCode
	}
	return e.code
}

// Class identifies the HTTP response class of this public error.
func (e *Error) Class() Class { return e.class }

// Retryable says whether repeating the request can recover from this error.
func (e *Error) Retryable() bool { return e.retryable }

// Resolve finds the first public error in the chain, or the caller's fallback.
func Resolve(err error, fallback *Error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return fallback
}

type fieldError struct {
	err   error
	field string
}

func (e *fieldError) Error() string       { return e.err.Error() + " at " + e.field }
func (e *fieldError) Unwrap() error       { return e.err }
func (e *fieldError) PublicField() string { return e.field }

// WithField attaches a JSON Pointer to a request error without changing its
// public identity or class. A nil error stays nil.
func WithField(err error, field string) error {
	if err == nil {
		return nil
	}
	return &fieldError{err: err, field: field}
}

// Field reads the outermost public field pointer. Domain validation wrappers
// can implement PublicField to retain their own structured diagnostics.
func Field(err error) string {
	var f interface{ PublicField() string }
	if errors.As(err, &f) {
		return f.PublicField()
	}
	return ""
}

// Code returns the public code of the first *Error in err's chain. ok is false
// when err carries none, so callers pick an explicit fallback.
func Code(err error) (code string, ok bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.code, true
	}
	return "", false
}

// detailed explains a coded error without changing its public code.
type detailed struct {
	err    error
	detail string
}

func (d *detailed) Error() string { return d.err.Error() + ": " + d.detail }
func (d *detailed) Unwrap() error { return d.err }

// WithDetail wraps err with an explanation for logs and tooling. Code and
// errors.Is still resolve through it. A nil err stays nil.
func WithDetail(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return &detailed{err: err, detail: fmt.Sprintf(format, args...)}
}

// Detail returns the explanation attached by the outermost WithDetail in err's
// chain, or "" when there is none.
func Detail(err error) string {
	var d *detailed
	if errors.As(err, &d) {
		return d.detail
	}
	return ""
}

// responseCode preserves an explicitly public provider code while its catalog
// condition owns the response class. Provider codes are defined by their plugin.
type responseCode struct {
	err  error
	code string
}

func (e *responseCode) Error() string              { return e.code }
func (e *responseCode) Unwrap() error              { return e.err }
func (e *responseCode) PublicResponseCode() string { return e.code }

// WithResponseCode carries a provider's public code through a catalog condition.
// It must only receive codes already approved for public plugin responses.
func WithResponseCode(err error, code string) error {
	if err == nil {
		return nil
	}
	return &responseCode{err: err, code: code}
}

// ResponseCode reads a provider code, or the catalog condition's wire code.
func ResponseCode(err error, fallback *Error) string {
	var e interface{ PublicResponseCode() string }
	if errors.As(err, &e) {
		return e.PublicResponseCode()
	}
	return fallback.ResponseCode()
}
