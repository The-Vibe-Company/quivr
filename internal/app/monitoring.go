package app

import (
	"context"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
)

// versionParts reads a Record Version's canonical text Parts for evaluation,
// with a worker scope limited to the Version's own Corpus.
type versionParts struct{ content content.Service }

func (v versionParts) Parts(ctx context.Context, org, corpusID, recordID, versionID string) ([]monitoring.Part, error) {
	version, err := v.content.Version(ctx, corpus.Scope{Organization: org, Actions: []string{"content:read"}, Corpora: []string{corpusID}}, recordID, versionID)
	if err != nil {
		return nil, err
	}
	parts := make([]monitoring.Part, 0, len(version.Manifest.Parts))
	for _, p := range version.Manifest.Parts {
		if p.Content.Kind == "text" {
			parts = append(parts, monitoring.Part{Key: p.Key, Role: p.Role, Text: p.Content.Text})
		}
	}
	return parts, nil
}
