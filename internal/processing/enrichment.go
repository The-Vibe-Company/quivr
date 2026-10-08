package processing

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
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
		s.outcome(ctx, org, "enrichment", "blocked", receiptID, v, started, out.Terminal.Code)
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
			slog.WarnContext(ctx, "enrichment blocked", "component", "worker", "version_id", v.ID, "error", err.Error())
		} else {
			// Name what is retried: a deadline, an outage, a plugin error.
			slog.WarnContext(ctx, "enrichment retrying", "component", "worker", "version_id", v.ID, "error", err.Error())
		}
		s.outcome(ctx, org, "enrichment", state, receiptID, v, started, code)
		_ = s.Content.EnrichmentProgress(ctx, org, v.ID, state, code)
		if state == "blocked" {
			return nil
		}
		return errors.New("enrichment unavailable")
	}
	s.outcome(ctx, org, "enrichment", "succeeded", receiptID, v, started, "")
	if s.Observer != nil {
		s.Observer.Enriched(ctx, org, receiptID, time.Since(started))
	}
	return nil
}
func (s Service) enrich(ctx context.Context, org string, v content.Version) DerivationResult {
	rt, err := s.route(ctx, org, v)
	if err == nil && (rt.recipeMismatch || rt.legacy) {
		err = ErrSpaceUnowned
	}
	out := DerivationResult{Retry: err, Cause: err}
	if err == nil {
		out = Derive(ctx, org, s.Plugin, s.Content, DerivationRequest{CorpusID: rt.corpusID, Version: v, Target: rt.generation, Kind: Vectors})
	}
	if errors.Is(out.Retry, ErrSpaceUnowned) {
		// This plan cannot produce the old route's model. Finish the receipt
		// instead of occupying an activity slot until a rebuild switches it.
		var driver PluginDeriver
		if s.Plugin != nil {
			driver = s.Plugin.forVersion(ctx, v)
		}
		var reason *content.Diagnostic
		if err != nil {
			// Derive already checked Gone for derivation failures. Only route
			// failures need that check here, so its attempt budget is spent once.
			var goneErr error
			reason, goneErr = driver.Gone(ctx, err)
			if goneErr != nil {
				return DerivationResult{Retry: goneErr}
			}
		}
		if reason == nil {
			reason = &content.Diagnostic{Code: content.CodeRebuildRequired, Message: "The pinned ingestion plugin cannot serve the routed generation's embedding space; rebuild the Corpus to serve this Version's vectors.", Contribution: "ingestion"}
			if driver.descriptor != nil {
				reason.Plugin, reason.PluginVersion = driver.descriptor.PluginID, driver.descriptor.PluginVersion
			}
		}
		if work, ok := plugins.WorkOf(ctx); ok {
			reason.Plan = work.Plan
		}
		return DerivationResult{Terminal: reason, Cause: out.Cause}
	}
	if out.Terminal == nil && out.Retry == nil {
		out.Retry = s.Enrichment.IndexEmbeddings(ctx, org, v, out.Segmentation, out.Data)
	}
	return out
}
