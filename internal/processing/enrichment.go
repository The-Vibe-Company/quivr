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
	if v.Steps.Enriched != nil {
		// Already enriched: a retry after a worker died before reporting the
		// success ends here, whatever plan it now follows, so its pin is
		// released instead of deriving again and failing forever (THE-861).
		return nil
	}
	if err = s.Content.EnrichmentProgress(ctx, org, v.ID, "running", ""); err != nil {
		return err
	}
	started := time.Now()
	out := s.enrich(ctx, org, v)
	err = out.Retry
	if out.Terminal != nil {
		s.outcome(org, "enrichment", "blocked", receiptID, v, started, out.Terminal.Code)
		if out.Terminal.Code == content.CodeEnrichmentTimeout {
			return s.Content.EnrichmentProgress(ctx, org, v.ID, "blocked", out.Terminal.Code)
		}
		if errors.Is(out.Cause, content.ErrConflict) || errors.Is(out.Cause, content.ErrIngestionRefused) {
			// Keep the public enrichment state for incompatible derived artifacts.
			_ = s.Content.EnrichmentProgress(ctx, org, v.ID, "blocked", "derivation_conflict")
			return nil
		}
		return s.Content.BlockEnrichment(ctx, org, v.ID, *out.Terminal)
	}
	if err != nil {
		state, code := "retrying", "enrichment_unavailable"
		if errors.Is(err, content.ErrConflict) || errors.Is(err, content.ErrIngestionRefused) {
			state, code = "blocked", "derivation_conflict"
			// Name why: a plugin whose segments with vectors differ from the
			// segments it returned alone at baseline, or another refusal.
			slog.Warn("enrichment blocked", "component", "worker", "version_id", v.ID, "error", err.Error())
		} else {
			// Name what is retried: a deadline, an outage, a plugin error.
			slog.Warn("enrichment retrying", "component", "worker", "version_id", v.ID, "error", err.Error())
		}
		s.outcome(org, "enrichment", state, receiptID, v, started, code)
		_ = s.Content.EnrichmentProgress(ctx, org, v.ID, state, code)
		if state == "blocked" {
			return nil
		}
		return errors.New("enrichment unavailable")
	}
	s.outcome(org, "enrichment", "succeeded", receiptID, v, started, "")
	if s.Observer != nil {
		s.Observer.Enriched(ctx, org, receiptID, time.Since(started))
	}
	return nil
}
func (s Service) enrich(ctx context.Context, org string, v content.Version) DerivationResult {
	rt, err := s.route(ctx, org, v)
	if err == nil && rt.legacy {
		err = ErrSpaceUnowned
	}
	if err != nil {
		return DerivationResult{Retry: err}
	}
	out := Derive(ctx, org, s.Plugin, s.Content, DerivationRequest{CorpusID: rt.corpusID, Version: v, Target: rt.generation, Kind: Vectors})
	if out.Terminal == nil && out.Retry == nil {
		out.Retry = s.Enrichment.IndexEmbeddings(ctx, org, v, out.Segmentation, out.Data)
	}
	return out
}
