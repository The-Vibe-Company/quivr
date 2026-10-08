package content

import (
	"errors"
	"fmt"
)

// Registry refusals at startup.
var (
	// ErrSpaceOwner is a registered space another owner declares: a space has
	// exactly one owner, for as long as vectors exist in it.
	ErrSpaceOwner = errors.New("space_owner_conflict")
	// ErrSpaceChanged is a registered space whose model, dimensions or metric
	// changed under the same id and version; a new version is a new space.
	ErrSpaceChanged = errors.New("space_changed")
	// ErrIngestionRefused is an ingestion plugin's terminal refusal of a
	// Version, or an answer the engine refuses: retrying cannot help.
	ErrIngestionRefused = errors.New("ingestion_refused")
)

// Refusal is ErrIngestionRefused with its reason: the diagnostic a Version
// quarantined for it shows.
type Refusal struct{ Reason Diagnostic }

func (r *Refusal) Error() string { return ErrIngestionRefused.Error() + ": " + r.Reason.Message }
func (r *Refusal) Unwrap() error { return ErrIngestionRefused }

// Refused is ErrIngestionRefused for the reason message states.
func Refused(format string, args ...any) error {
	return &Refusal{Reason: Diagnostic{Code: ErrIngestionRefused.Error(), Message: fmt.Sprintf(format, args...)}}
}

// RefusalReason is the diagnostic of an ingestion refusal: its reason, or a
// generic one when err carries none.
func RefusalReason(err error) Diagnostic {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Reason
	}
	return Diagnostic{Code: ErrIngestionRefused.Error(), Message: "the ingestion plugin refused the Version; it is withheld from search"}
}

// SpaceError names the space a registry refusal is about.
type SpaceError struct {
	Kind   error
	Space  string
	Detail string
}

func (e *SpaceError) Error() string {
	return fmt.Sprintf("%s: vector space %s: %s", e.Kind, e.Space, e.Detail)
}

func (e *SpaceError) Unwrap() error { return e.Kind }

// SpaceCoverage is one vector space of a Corpus's routed generation, as the
// registry describes it, with how many current segments hold a vector in it.
type SpaceCoverage struct {
	RegisteredSpace
	// GenerationRole is served for the generation's served space and
	// evaluation for its other spaces.
	GenerationRole string
	// Segments counts current segments with a vector in the space.
	Segments int64
	// TotalSegments counts this owner's independent segmentation. Nil retains legacy totals.
	TotalSegments *int64
	// VersionsCovered counts current Versions fully covered in this space.
	VersionsCovered int64
	// ServingSegments counts this owner's current served projection, when known.
	ServingSegments *int64
	// CorpusEmpty is true only when current canonical metadata confirms there
	// are no eligible Versions. It is independent of asynchronous coverage.
	CorpusEmpty bool
	// CoverageUnknown means no background snapshot has completed yet. Counts
	// are informational and must not gate candidate retrieval.
	CoverageUnknown bool
	CoverageAgeMS   *int64
}
