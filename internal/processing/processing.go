// Package processing executes validated local contributions under engine ownership.
package processing

import (
	"context"
	"errors"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

var ErrUnsupported = errors.New("segmentation_limit")

type Input struct {
	Organization string
	Version      content.Version
}

// Processor is the shared contribution interface. Local execution needs no network.
type Processor interface {
	Process(context.Context, Input) (content.Segmentation, error)
}

type Indexer interface {
	Index(context.Context, string, content.Version, content.Segmentation) error
}
type Service struct {
	Content    content.Service
	Processor  Processor
	Retrieval  Indexer
	Embedder   Embedder
	Enrichment EnrichmentIndexer
}

func (s Service) Run(ctx context.Context, org, receiptID string) error {
	if err := s.Content.Materialize(ctx, org, receiptID); err != nil {
		return err
	}
	v, err := s.Content.ProcessingVersion(ctx, org, receiptID)
	if err != nil || v.ID == "" {
		return err
	}
	if v.Availability.State == "retrieval_ready" || v.Availability.State == "quarantined" {
		return nil
	}
	if err = s.Content.BaselineProgress(ctx, org, v.ID, "running", "", false); err != nil {
		return err
	}
	result, err := s.Processor.Process(ctx, Input{Organization: org, Version: v})
	if errors.Is(err, ErrUnsupported) {
		return s.Content.BaselineProgress(ctx, org, v.ID, "blocked", "segmentation_limit", true)
	}
	if err == nil {
		err = s.Content.SaveSegmentation(ctx, org, v, result)
	}
	if err == nil {
		err = s.Retrieval.Index(ctx, org, v, result)
	}
	if err != nil {
		_ = s.Content.BaselineProgress(ctx, org, v.ID, "retrying", "baseline_unavailable", false)
		return errors.New("baseline processing unavailable")
	}
	return nil
}
