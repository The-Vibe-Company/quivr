package registry

import "fmt"

// CoverageGap identifies an ingestion owner's incomplete returning projection.
// Versions count documents, independently of how many segments they contain.
type CoverageGap struct {
	Owner, Space       string
	MissingVersions    int64
	MissingGenerations int64
}

// CoverageError refuses an owner switch without exposing storage error text.
type CoverageError struct {
	Gaps []CoverageGap
}

func (e *CoverageError) Error() string {
	return fmt.Sprintf("%s: ingestion coverage gaps %+v", ErrConflict, e.Gaps)
}
func (e *CoverageError) Unwrap() error { return ErrConflict }
