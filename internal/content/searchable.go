package content

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

// Segmentation is an immutable derivation; offsets address canonical Part text.
type Segment struct {
	ID, PartKey, Text string
	Start, End        int
}
type Segmentation struct {
	ID, VersionID, Recipe string
	Segments              []Segment
}
type Generation struct{ ID, Collection, ProfileVersion string }
type Candidate struct{ SegmentID, GenerationID string }
type Hydrated struct {
	RecordID, VersionID, SegmentationID string
	TextSHA256                          string
	Segment                             Segment
	Availability                        Availability
}

type BaselineRepository interface {
	SaveSegmentation(context.Context, string, Segmentation) error
	BaselineProgress(context.Context, string, string, string, string, bool) error
	Promote(context.Context, string, Segmentation, Generation) error
	Hydrate(context.Context, corpus.Scope, Candidate) (Hydrated, Blob, error)
}

func (s Service) ProcessingVersion(ctx context.Context, org, receiptID string) (Version, error) {
	r, err := s.Repository.Receipt(ctx, org, receiptID)
	if err != nil {
		return Version{}, err
	}
	if r.VersionID == "" {
		return Version{}, nil
	}
	return s.Version(ctx, corpus.Scope{Organization: org, Actions: []string{"content:read"}, Corpora: []string{r.Source.CorpusID}}, r.RecordID, r.VersionID)
}

// Validate outputs at the engine boundary even when the contribution runs locally.
func (s Service) SaveSegmentation(ctx context.Context, org string, v Version, result Segmentation) error {
	if result.VersionID != v.ID || result.Recipe == "" || len(result.Segments) == 0 {
		return ErrInvalid
	}
	parts := map[string][]rune{}
	for _, p := range v.Manifest.Parts {
		parts[p.Key] = []rune(p.Content.Text)
	}
	for i := range result.Segments {
		p := &result.Segments[i]
		text, ok := parts[p.PartKey]
		if !ok || p.Start < 0 || p.End <= p.Start || p.End > len(text) || string(text[p.Start:p.End]) != p.Text {
			return ErrInvalid
		}
		expected := StableID("segment", org, result.ID, p.PartKey, strconv.Itoa(p.Start), strconv.Itoa(p.End), Hash([]byte(p.Text)))
		if p.ID != expected {
			return ErrInvalid
		}
	}
	expected := StableID("segmentation", org, v.ID, result.Recipe)
	if result.ID != expected {
		return ErrInvalid
	}
	return s.Baseline.SaveSegmentation(ctx, org, result)
}
func SegmentationDigest(result Segmentation) string { b, _ := json.Marshal(result); return Hash(b) }
func (s Service) BaselineProgress(ctx context.Context, org, versionID, state, code string, quarantined bool) error {
	return s.Baseline.BaselineProgress(ctx, org, versionID, state, code, quarantined)
}
func (s Service) Promote(ctx context.Context, org string, seg Segmentation, g Generation) error {
	return s.Baseline.Promote(ctx, org, seg, g)
}
func (s Service) Hydrate(ctx context.Context, scope corpus.Scope, c Candidate) (Hydrated, error) {
	if !scope.Allows("content:read") || !scope.Allows("search:query") {
		return Hydrated{}, corpus.ErrForbidden
	}
	h, blob, err := s.Baseline.Hydrate(ctx, scope, c)
	if err != nil {
		return h, err
	}
	text, err := s.Blobs.Read(ctx, blob)
	if err != nil {
		return h, err
	}
	runes := []rune(string(text))
	if h.Segment.Start < 0 || h.Segment.End > len(runes) || h.Segment.End < h.Segment.Start {
		return h, errors.New("invalid canonical excerpt")
	}
	excerpt := string(runes[h.Segment.Start:h.Segment.End])
	// Repository supplies the checksum of the segment, not a projection excerpt.
	if Hash([]byte(excerpt)) != h.TextSHA256 {
		return h, errors.New("canonical excerpt mismatch")
	}
	// Access and currentness may have changed while fetching immutable bytes.
	if _, _, err = s.Baseline.Hydrate(ctx, scope, c); err != nil {
		return Hydrated{}, err
	}
	h.Segment.Text = excerpt
	return h, nil
}
