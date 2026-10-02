package processing

import (
	"context"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

type RecordReader interface {
	TrustedRecord(context.Context, string, string) (content.Record, error)
}

// VersionRoute resolves the Record source and current Corpus generation at
// the engine boundary. Publication and derivation use this same lookup.
func VersionRoute(ctx context.Context, org string, v content.Version, records RecordReader, routing GenerationRouter) (content.Record, content.Generation, error) {
	record, err := records.TrustedRecord(ctx, org, v.RecordID)
	if err != nil {
		return record, content.Generation{}, err
	}
	generation, err := routing.Generation(ctx, org, record.Source.CorpusID)
	return record, generation, err
}
