package content

import (
	"context"
	"encoding/json"
	"slices"
)

// IngestionPage is a durable bounded result. Negotiated cuts and vectors are
// retained together so an interrupted derivation resumes without renegotiation.
type IngestionPage struct {
	Segments json.RawMessage `json:"segments"`
	Next     json.RawMessage `json:"next,omitempty"`
}

type IngestionPageStore interface {
	IngestionPage(context.Context, string, string, string, string, int) (IngestionPage, bool, error)
	// SaveIngestionPage returns the first committed result if calls raced.
	SaveIngestionPage(context.Context, string, string, string, string, int, IngestionPage) (IngestionPage, error)
	// DeleteIngestionPages retires one exact input namespace after its cuts and
	// all requested vectors are durable. Repeating a completed deletion is safe.
	DeleteIngestionPages(context.Context, string, string, string, string) error
}

// IngestionPageNamespace identifies durable work discovered by the page sweep.
// ManifestID fences source changes while canonical artifacts are read outside
// the database transaction. Settled is a hint; retirement rechecks it.
type IngestionPageNamespace struct {
	Organization, VersionID, Recipe, InputKey string
	RecordID, CorpusID, ManifestID            string
	Spaces                                    []string
	Settled                                   bool
}

// IngestionPageSweepStore scans and retires pages in bounded batches.
type IngestionPageSweepStore interface {
	IngestionPageCandidates(context.Context, int) ([]IngestionPageNamespace, error)
	RetireIngestionPages(context.Context, IngestionPageNamespace, string, int) (int, error)
}

// IngestionPageInputKey binds provider checkpoints to every requested space and
// the exact source Manifest, including normalization changes under one Version.
func IngestionPageInputKey(v Version, spaces []string) (string, error) {
	keys := slices.Clone(spaces)
	slices.Sort(keys)
	raw, err := json.Marshal(struct {
		Spaces []string `json:"spaces"`
		Source Manifest `json:"source"`
	}{keys, v.Manifest})
	if err != nil {
		return "", err
	}
	return Hash(raw), nil
}
