package app

import "fmt"

type configCode string

const (
	configMissing  configCode = "config_missing"
	configInvalid  configCode = "config_invalid"
	configConflict configCode = "config_conflict"
)

// configError separates safe, engine-owned diagnostics from causes that may
// contain secret values. Fields and problems must be static configuration
// names and explanations, never interpolated input or dependency errors.
type configError struct {
	code    configCode
	field   string
	problem string
	cause   error
}

func (e *configError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %v", e.problem, e.cause)
	}
	return e.problem
}

func (e *configError) Unwrap() error { return e.cause }

func badConfig(code configCode, field, problem string) error {
	return &configError{code: code, field: field, problem: problem}
}

func invalidConfig(field, problem string, cause error) error {
	return &configError{code: configInvalid, field: field, problem: problem, cause: cause}
}
