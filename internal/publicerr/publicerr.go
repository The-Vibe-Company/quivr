// Package publicerr gives domain errors a stable public code. Transports
// resolve the code with Code, never from Error text, so wrapping an error with
// explanatory detail can never change what an API client sees.
package publicerr

import (
	"errors"
	"fmt"
)

// Error is a sentinel carrying a stable public code. Each New value is a
// distinct sentinel for errors.Is, even when two share the same code.
type Error struct{ code string }

// New declares a sentinel whose public code is code.
func New(code string) *Error { return &Error{code: code} }

func (e *Error) Error() string { return e.code }

// Code returns the public code.
func (e *Error) Code() string { return e.code }

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
