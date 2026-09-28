package processing

import (
	"context"
	"errors"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

type Embedder interface {
	Embed(context.Context, string) ([]float32, error)
	Space() content.VectorSpace
	Producer() string
}
type EnrichmentIndexer interface {
	IndexEmbeddings(context.Context, string, content.Version, content.Segmentation, []content.EmbeddingData) error
}

func (s Service) Enrich(ctx context.Context, org, receiptID string) error {
	v, err := s.Content.ProcessingVersion(ctx, org, receiptID)
	if err != nil || v.ID == "" {
		return err
	}
	if v.Availability.State != "retrieval_ready" || (v.Processing.State == "blocked" && v.Processing.Phase == "enrichment") {
		return nil
	}
	if err = s.Content.EnrichmentProgress(ctx, org, v.ID, "running", ""); err != nil {
		return err
	}
	started := time.Now()
	err = s.enrich(ctx, org, v)
	if err != nil {
		state, code := "retrying", "enrichment_unavailable"
		if errors.Is(err, content.ErrConflict) {
			state, code = "blocked", "derivation_conflict"
		}
		s.outcome("enrichment", state, receiptID, v, started, code)
		_ = s.Content.EnrichmentProgress(ctx, org, v.ID, state, code)
		if state == "blocked" {
			return nil
		}
		return errors.New("enrichment unavailable")
	}
	s.outcome("enrichment", "succeeded", receiptID, v, started, "")
	return nil
}
func (s Service) enrich(ctx context.Context, org string, v content.Version) error {
	seg, err := s.Processor.Process(ctx, Input{Organization: org, Version: v})
	if err != nil {
		return err
	}
	r, err := s.Content.Record(ctx, corpus.Scope{Organization: org, Actions: []string{"content:read"}, Corpora: []string{"*"}}, v.RecordID)
	if err != nil {
		return err
	}
	data := make([]content.EmbeddingData, 0, len(seg.Segments))
	for _, p := range seg.Segments {
		input := content.EmbeddingInput(org, r.Source.CorpusID, v, seg, p, s.Embedder.Space(), s.Embedder.Producer())
		artifact, vector, err := s.Content.LoadEmbedding(ctx, org, input.DerivationID)
		if errors.Is(err, corpus.ErrNotFound) {
			vector, err = s.Embedder.Embed(ctx, p.Derivation.ModelInput)
			if err == nil {
				artifact, err = s.Content.SaveEmbedding(ctx, input, s.Embedder.Space(), vector)
			}
		}
		if err != nil {
			return err
		}
		data = append(data, content.EmbeddingData{Artifact: artifact, Vector: vector})
	}
	return s.Enrichment.IndexEmbeddings(ctx, org, v, seg, data)
}
