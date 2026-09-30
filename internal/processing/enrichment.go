package processing

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

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
		if errors.Is(err, content.ErrConflict) || errors.Is(err, content.ErrIngestionRefused) {
			state, code = "blocked", "derivation_conflict"
			// Name why: a plugin whose segments with vectors differ from the
			// segments it returned alone at baseline, or another refusal.
			slog.Warn("enrichment blocked", "component", "worker", "version_id", v.ID, "error", err.Error())
		}
		s.outcome(org, "enrichment", state, receiptID, v, started, code)
		_ = s.Content.EnrichmentProgress(ctx, org, v.ID, state, code)
		if state == "blocked" {
			return nil
		}
		return errors.New("enrichment unavailable")
	}
	s.outcome(org, "enrichment", "succeeded", receiptID, v, started, "")
	return nil
}
func (s Service) enrich(ctx context.Context, org string, v content.Version) error {
	rt, err := s.route(ctx, org, v)
	if err == nil && rt.legacy {
		// The legacy E5 space has no pinned owner: the Corpus's rebuild onto
		// the plugin's spaces embeds this Version, then this retry attaches.
		err = ErrSpaceUnowned
	}
	if err != nil {
		return err
	}
	seg, data, err := s.Plugin.Derive(ctx, org, rt.corpusID, v, rt.generation)
	if err != nil {
		return err
	}
	return s.Enrichment.IndexEmbeddings(ctx, org, v, seg, data)
}
