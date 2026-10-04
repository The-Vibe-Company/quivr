package app

import (
	"context"
	"sort"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/monitoring"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

type savedQueryEncoder struct {
	search     retrieval.Service
	evaluators *monitoring.LiveEvaluators
}

func (e savedQueryEncoder) EncodeSavedQuery(ctx context.Context, org string, definition monitoring.Definition) ([]monitoring.QueryVector, error) {
	texts := map[string]bool{}
	for _, evaluator := range e.evaluators.Load().Served {
		if port, ok := evaluator.(interface{ QueryText(map[string]any) string }); ok {
			if text := port.QueryText(definition.Expression); text != "" {
				texts[text] = true
			}
		}
	}
	if len(texts) == 0 {
		return nil, nil
	}
	if len(texts) != 1 {
		return nil, monitoring.ErrInvalidExpression
	}
	var text string
	for value := range texts {
		text = value
	}
	spaces := map[string]bool{}
	for _, corpusID := range definition.CorpusIDs {
		generation, err := e.search.Routing.Generation(ctx, org, corpusID)
		if err != nil {
			return nil, err
		}
		for _, space := range generation.VectorSpaces() {
			if generation.Serves(space) {
				spaces[space] = true
			}
		}
	}
	keys := make([]string, 0, len(spaces))
	for space := range spaces {
		keys = append(keys, space)
	}
	sort.Strings(keys)
	vectors := make([]monitoring.QueryVector, 0, len(keys))
	for _, space := range keys {
		vector, err := e.search.EncodeQuery(ctx, org, space, text)
		if err != nil {
			return nil, err
		}
		if _, err := content.VectorBytes(vector); err != nil {
			return nil, err
		}
		vectors = append(vectors, monitoring.QueryVector{SpaceID: space, Vector: vector})
	}
	return vectors, nil
}

type subscriptionEmbeddingReader interface {
	SubscriptionEmbeddings(context.Context, string, string, string) (content.Generation, []string, int, error)
}

func (v versionParts) ArticleVectors(ctx context.Context, org, corpusID, versionID string) (*monitoring.ArticleVectors, error) {
	generation, derivations, total, err := v.vectors.SubscriptionEmbeddings(ctx, org, corpusID, versionID)
	if err != nil {
		return nil, err
	}
	result := &monitoring.ArticleVectors{SpaceID: generation.SpaceID, Ready: total > 0 && total == len(derivations), Parts: map[string][]monitoring.SegmentVector{}}
	for _, derivation := range derivations {
		artifact, vector, err := v.content.LoadEmbedding(ctx, org, derivation)
		if err != nil {
			return nil, err
		}
		if artifact.SpaceID != generation.SpaceID || artifact.VersionID != versionID || artifact.CorpusID != corpusID {
			return nil, content.ErrConflict
		}
		result.Parts[artifact.PartKey] = append(result.Parts[artifact.PartKey], monitoring.SegmentVector{SegmentID: artifact.SegmentID, Vector: vector})
	}
	return result, nil
}
