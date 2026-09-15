// Package processing executes validated local contributions under engine ownership.
package processing

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

const ShortRecipe = "quivr.normalized-text.short-whole-part.v1"
const MaxShortBytes = 256

var ErrUnsupported = errors.New("short_text_limit")

type Input struct {
	Organization string
	Version      content.Version
}

// Processor is the shared contribution interface. Local execution needs no network.
type Processor interface {
	Process(context.Context, Input) (content.Segmentation, error)
}
type ShortText struct{}

func (ShortText) Process(ctx context.Context, in Input) (content.Segmentation, error) {
	result := content.Segmentation{ID: content.StableID("segmentation", in.Organization, in.Version.ID, ShortRecipe), VersionID: in.Version.ID, Recipe: ShortRecipe}
	if len(in.Version.Manifest.Parts) != 1 {
		return result, ErrUnsupported
	}
	p := in.Version.Manifest.Parts[0]
	text := p.Content.Text
	if len(text) > MaxShortBytes || strings.ContainsRune(text, 0) || !utf8.ValidString(text) || text == "" {
		return result, ErrUnsupported
	}
	end := utf8.RuneCountInString(text)
	result.Segments = []content.Segment{{ID: content.StableID("segment", in.Organization, result.ID, p.Key, "0", strconv.Itoa(end), content.Hash([]byte(text))), PartKey: p.Key, Text: text, Start: 0, End: end}}
	return result, ctx.Err()
}

type Indexer interface {
	Index(context.Context, string, content.Version, content.Segmentation) error
}
type Service struct {
	Content   content.Service
	Processor Processor
	Retrieval Indexer
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
		return s.Content.BaselineProgress(ctx, org, v.ID, "blocked", "short_text_limit", true)
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
