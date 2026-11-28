package weaviate

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"golang.org/x/sync/errgroup"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

// lexicalPatchConcurrency bounds parallel property-only PATCH calls to Weaviate.
const (
	lexicalPatchConcurrency  = 16
	lexicalProjectionBatch   = 25
	lexicalInventoryMaxBytes = 16 << 20
)

// UpdateLexicalText updates only the searchable lexicalText property on each
// segment's projected Weaviate object(s) using merge PATCH. It never modifies
// dense vectors, never adds HNSW entries, and never alters source text
// properties. Already-converged objects are skipped so retries are idempotent.
func (s *Store) UpdateLexicalText(ctx context.Context, g content.Generation, org, corpusID string, updates []content.SegmentLexicalUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	if !className.MatchString(g.Collection) {
		return errors.New("invalid lexical projection route")
	}
	ctx, err := tenantContext(ctx, g.Collection, org, corpusID, g.ID)
	if err != nil {
		return err
	}
	if err := s.ensureProperties(ctx, g.Collection, []map[string]any{searchable(lexicalProperty)}); err != nil {
		return err
	}
	target := make(map[string]string, len(updates))
	order := make([]string, 0, len(updates))
	for _, u := range updates {
		if u.SegmentID == "" || !content.ValidLexicalText(u.LexicalText, content.MaxLexicalTextRunes) {
			return content.ErrInvalid
		}
		if _, dup := target[u.SegmentID]; dup {
			return content.ErrInvalid
		}
		target[u.SegmentID] = u.LexicalText
		order = append(order, u.SegmentID)
	}
	for segments := range slices.Chunk(order, lexicalProjectionBatch) {
		if err := s.updateLexicalBatch(ctx, g, org, segments, target); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) updateLexicalBatch(ctx context.Context, g content.Generation, org string, segments []string, target map[string]string) error {
	scope := generationFilter(ctx, org, g.ID)
	rows, err := s.projectionRowsLimit(ctx, g.Collection, and(scope, containsAny("segmentId", segments)),
		embeddingInventoryLimit, searchOrder{}, []string{"segmentId", lexicalProperty}, false, lexicalInventoryMaxBytes)
	if err != nil {
		return err
	}
	full := len(rows) >= embeddingInventoryLimit
	rowsBySegment := make(map[string][]projectionRow, len(segments))
	for _, row := range rows {
		rowsBySegment[row.SegmentID] = append(rowsBySegment[row.SegmentID], row)
	}

	type patchSpec struct {
		objectID    string
		lexicalText string
	}
	var patches []patchSpec
	for _, segmentID := range segments {
		segRows := rowsBySegment[segmentID]
		if full {
			segRows, err = s.projectionRowsLimit(ctx, g.Collection, and(scope, equal("segmentId", segmentID)),
				embeddingInventoryLimit, searchOrder{}, []string{"segmentId", lexicalProperty}, false, lexicalInventoryMaxBytes)
			if err != nil {
				return err
			}
			if len(segRows) >= embeddingInventoryLimit {
				return errors.New("lexical projection inventory truncated")
			}
		}
		if len(segRows) == 0 {
			return retrieval.ErrProjectionMissing
		}
		want := target[segmentID]
		for _, row := range segRows {
			if row.LexicalText == want {
				continue
			}
			patches = append(patches, patchSpec{objectID: row.Additional.ID, lexicalText: want})
		}
	}
	if len(patches) == 0 {
		return nil
	}
	group, gctx := errgroup.WithContext(ctx)
	group.SetLimit(lexicalPatchConcurrency)
	tenant := tenantFrom(ctx)
	for _, spec := range patches {
		goRecover(gctx, group, "update_lexical_batch", func() error {
			body := map[string]any{
				"class":      g.Collection,
				"id":         spec.objectID,
				"properties": map[string]any{lexicalProperty: spec.lexicalText},
			}
			if tenant != "" {
				body["tenant"] = tenant
			}
			status, err := s.call(gctx, http.MethodPatch, tenantPath(gctx, "/v1/objects/"+g.Collection+"/"+spec.objectID), body, nil)
			if status == http.StatusNotFound {
				return retrieval.ErrProjectionMissing
			}
			return err
		})
	}
	return group.Wait()
}

var _ retrieval.LexicalProjection = (*Store)(nil)
