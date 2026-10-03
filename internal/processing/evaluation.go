package processing

import (
	"context"
	"errors"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

// EvaluationStore owns durable dispatch and per-plugin evaluation outcomes.
type EvaluationStore interface {
	ClaimIngestionEvaluations(context.Context, int) ([]content.IngestionEvaluation, error)
	IngestionEvaluationDispatched(context.Context, content.IngestionEvaluation) error
	IngestionEvaluation(context.Context, string, string) (content.IngestionEvaluation, error)
	PrepareEvaluation(context.Context, string, string, []string) (content.Generation, error)
	CoverEvaluation(context.Context, string, content.Generation, content.Segmentation, []content.Embedding) error
	CompleteIngestionEvaluation(context.Context, string, string, string, *content.Diagnostic) error
}

type EvaluationProjection interface {
	Publish(context.Context, content.Generation, string, string, string, content.Version, content.Segmentation) error
	PublishEmbeddings(context.Context, content.Generation, string, []content.EmbeddingData) error
}

// Evaluator runs after served publication, on dedicated activity capacity.
// Plugin failures end only this job; dependency publication failures retry it.
type Evaluator struct {
	Store      EvaluationStore
	Serving    ServingProjectionStore
	Content    content.Service
	Plugin     *PluginDeriver
	Projection EvaluationProjection
}

func (e Evaluator) Run(ctx context.Context, org, id string) error {
	job, err := e.Store.IngestionEvaluation(ctx, org, id)
	if err != nil || job.State != "queued" {
		return err
	}
	record, err := e.Content.TrustedRecord(ctx, org, job.RecordID)
	if errors.Is(err, corpus.ErrNotFound) {
		return e.Store.CompleteIngestionEvaluation(ctx, org, id, "skipped", nil)
	}
	if err != nil {
		return err
	}
	v, err := e.Content.TrustedVersion(ctx, org, record.Source.CorpusID, job.RecordID, job.VersionID)
	if errors.Is(err, corpus.ErrNotFound) || errors.Is(err, content.ErrArtifactMissing) || errors.Is(err, content.ErrArtifactCorrupt) {
		return e.Store.CompleteIngestionEvaluation(ctx, org, id, "skipped", nil)
	}
	if err != nil {
		return err
	}
	if !v.Availability.Current || !v.Availability.Searchable || v.Steps.Enriched == nil {
		return e.Store.CompleteIngestionEvaluation(ctx, org, id, "skipped", nil)
	}
	g, err := e.Store.PrepareEvaluation(ctx, org, record.Source.CorpusID, job.Spaces)
	if err != nil {
		return err
	}
	if g.ID != job.GenerationID {
		return e.Store.CompleteIngestionEvaluation(ctx, org, id, "skipped", nil)
	}
	if e.Plugin == nil || len(job.Spaces) == 0 {
		return e.fail(ctx, job, ErrSpaceUnowned)
	}
	driver := e.Plugin.forSpace(ctx, job.Spaces[0])
	if driver.Plugin == nil {
		return e.fail(ctx, job, ErrSpaceUnowned)
	}
	if err = driver.bind(ctx); err != nil {
		return err
	}
	seg, data, err := driver.derive(ctx, org, record.Source.CorpusID, v, job.Spaces)
	if err != nil {
		return e.fail(ctx, job, err)
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
	if err = e.Store.CoverEvaluation(ctx, org, g, seg, artifacts); err != nil {
		if errors.Is(err, content.ErrConflict) || errors.Is(err, content.ErrInvalid) {
			return e.fail(ctx, job, err)
		}
		return err
	}
	return e.Store.CompleteIngestionEvaluation(ctx, org, id, "succeeded", nil)
}

func (e Evaluator) fail(ctx context.Context, job content.IngestionEvaluation, cause error) error {
	code, retryable := "ingestion_evaluation_unavailable", true
	if errors.Is(cause, content.ErrIngestionRefused) || errors.Is(cause, content.ErrConflict) || errors.Is(cause, content.ErrInvalid) {
		code, retryable = "ingestion_evaluation_refused", false
	}
	if errors.Is(cause, ErrPluginDeadline) {
		code = "ingestion_evaluation_deadline"
	}
	reason := content.Diagnostic{Code: code, Message: "The evaluation plugin could not fill its independent projection; served ingestion and search remain available.", Retryable: retryable, Plugin: job.PluginID, Contribution: "ingestion", Plan: job.PlanID}
	return e.Store.CompleteIngestionEvaluation(ctx, job.Organization, job.ID, "failed", &reason)
}
