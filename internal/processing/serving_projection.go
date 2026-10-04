package processing

import (
	"context"
	"errors"
	"slices"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

// ServingProjectionStore owns the independently pinned publication required
// after a route cutover. Optional evaluation cannot occupy this work's slots.
type ServingProjectionStore interface {
	ClaimServingProjections(context.Context, int) ([]content.IngestionEvaluation, error)
	ServingProjectionDispatched(context.Context, content.IngestionEvaluation) error
	ServingProjection(context.Context, string, string) (content.IngestionEvaluation, error)
	ServingProjectionEligible(context.Context, content.IngestionEvaluation) (bool, error)
	CoverServingProjection(context.Context, content.IngestionEvaluation, content.Generation, content.Segmentation, []content.Embedding) error
	CompleteServingProjection(context.Context, content.IngestionEvaluation, string, *content.Diagnostic) error
	CountServingProjectionTimeout(context.Context, content.IngestionEvaluation) (int, error)
}

func (e Evaluator) RunServing(ctx context.Context, org, id string) error {
	j, err := e.Serving.ServingProjection(ctx, org, id)
	if err != nil || j.State != "queued" {
		return err
	}
	eligible, err := e.Serving.ServingProjectionEligible(ctx, j)
	if err != nil {
		return err
	}
	if !eligible {
		return e.Serving.CompleteServingProjection(ctx, j, "skipped", nil)
	}
	record, err := e.Content.TrustedRecord(ctx, org, j.RecordID)
	if err != nil {
		return err
	}
	v, err := e.Content.TrustedVersion(ctx, org, record.Source.CorpusID, j.RecordID, j.VersionID)
	if err != nil {
		return err
	}
	if v.Steps.Enriched != nil && v.Availability.Current {
		return e.Serving.CompleteServingProjection(ctx, j, "succeeded", nil)
	}
	g, err := e.Store.PrepareEvaluation(ctx, org, record.Source.CorpusID, j.Spaces)
	if err != nil {
		return err
	}
	if g.ID != j.GenerationID {
		return e.Serving.CompleteServingProjection(ctx, j, "skipped", nil)
	}
	if e.Plugin == nil || len(j.Spaces) == 0 {
		return ErrSpaceUnowned
	}
	if !slices.Contains(j.Spaces, g.ServedFor(j.PluginID)) {
		return e.Serving.CompleteServingProjection(ctx, j, "skipped", nil)
	}
	seg, data, err := e.Plugin.FillIndependent(ctx, org, record.Source.CorpusID, v, j.Spaces)
	if err != nil {
		var reason *content.Diagnostic
		if errors.Is(err, content.ErrIngestionRefused) || errors.Is(err, content.ErrInvalid) || errors.Is(err, content.ErrConflict) {
			r := content.RefusalReason(err)
			reason = &r
		} else if errors.Is(err, ErrPluginDeadline) {
			count, countErr := e.Serving.CountServingProjectionTimeout(ctx, j)
			if countErr != nil {
				return countErr
			}
			if count >= EnrichmentTimeoutBudget {
				reason = &content.Diagnostic{Code: content.CodeEnrichmentTimeout, Message: "The served ingestion plugin exceeded its deadline three times for this Version."}
			}
		} else {
			var goneErr error
			reason, goneErr = e.Plugin.Gone(ctx, err)
			if goneErr != nil {
				return goneErr
			}
		}
		if reason == nil {
			return err
		}
		reason.Plugin, reason.Plan, reason.Contribution = j.PluginID, j.PlanID, "ingestion"
		return e.Serving.CompleteServingProjection(ctx, j, "failed", reason)
	}
	if err = e.Projection.Publish(ctx, g, org, record.Source.CorpusID, record.Source.Namespace, v, seg); err != nil {
		return err
	}
	if err = e.Projection.PublishEmbeddings(ctx, g, org, data); err != nil {
		return err
	}
	artifacts := make([]content.Embedding, len(data))
	for i, d := range data {
		artifacts[i] = d.Artifact
	}
	if err = e.Serving.CoverServingProjection(ctx, j, g, seg, artifacts); err != nil {
		return err
	}
	return e.Serving.CompleteServingProjection(ctx, j, "succeeded", nil)
}
