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
}
