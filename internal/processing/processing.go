// Package processing executes validated local contributions under engine ownership.
package processing

import (
	"context"
	"errors"
	"log/slog"
	"time"

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
	// Observer receives processing outcomes for metrics; nil disables them.
	Observer Observer
}

// Observer is told each processing outcome (bounded stage and outcome names)
// and when a Receipt's Version became searchable.
type Observer interface {
	Outcome(stage, outcome string)
	Searchable(ctx context.Context, org, receiptID string)
}

// outcome reports one stage outcome to the Observer and a correlated log line.
func (s Service) outcome(stage, outcome, receiptID string, v content.Version, started time.Time, code string) {
	if s.Observer != nil {
		s.Observer.Outcome(stage, outcome)
	}
	level := slog.LevelInfo
	if outcome != "succeeded" {
		level = slog.LevelWarn
	}
	slog.Log(context.Background(), level, "processing outcome", "component", "worker", "stage", stage, "outcome", outcome, "code", code,
		"receipt_id", receiptID, "record_id", v.RecordID, "version_id", v.ID, "duration_ms", time.Since(started).Milliseconds())
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
	started := time.Now()
	result, err := s.Processor.Process(ctx, Input{Organization: org, Version: v})
	if errors.Is(err, ErrUnsupported) {
		s.outcome("baseline", "blocked", receiptID, v, started, "segmentation_limit")
		return s.Content.BaselineProgress(ctx, org, v.ID, "blocked", "segmentation_limit", true)
	}
	if err == nil {
		err = s.Content.SaveSegmentation(ctx, org, v, result)
	}
	if err == nil {
		err = s.Retrieval.Index(ctx, org, v, result)
	}
	if err != nil {
		s.outcome("baseline", "retrying", receiptID, v, started, "baseline_unavailable")
		_ = s.Content.BaselineProgress(ctx, org, v.ID, "retrying", "baseline_unavailable", false)
		return errors.New("baseline processing unavailable")
	}
	s.outcome("baseline", "succeeded", receiptID, v, started, "")
	if s.Observer != nil {
		s.Observer.Searchable(ctx, org, receiptID)
	}
	return nil
}
