package content

import (
	"context"
	"encoding/json"
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
}
