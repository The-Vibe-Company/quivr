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
	TitleKey, Title   string
	Derivation        SegmentDerivation
}
type SegmentDerivation struct {
	Ordinal          int    `json:"ordinal"`
	UTF8Start        int    `json:"utf8_start"`
	UTF8End          int    `json:"utf8_end"`
	TokenStart       int    `json:"token_start"`
	TokenEnd         int    `json:"token_end"`
	Overlap          int    `json:"overlap_tokens"`
	HardStart        bool   `json:"hard_start"`
	HardEnd          bool   `json:"hard_end"`
	NormalizedSHA256 string `json:"normalized_content_sha256"`
	TitleFullSHA256  string `json:"full_title_sha256,omitempty"`
	TitleUsedSHA256  string `json:"title_used_sha256,omitempty"`
	TitleTokens      int    `json:"title_tokens"`
	TitleTruncated   bool   `json:"title_truncated"`
	ModelInput       string `json:"model_input"`
	ModelInputSHA256 string `json:"model_input_sha256"`
	ModelTokens      int    `json:"model_tokens"`
	// LexicalText is the keyword-search text an ingestion plugin returned
	// for the segment, indexed apart from the source text.
	LexicalText string `json:"lexical_text,omitempty"`
	// Provenance is how an ingestion plugin made the segment, stored as is.
	Provenance json.RawMessage `json:"provenance,omitempty"`
}
type Segmentation struct {
	ID, VersionID, Recipe string
	Segments              []Segment
	Provenance            json.RawMessage
}

// Generation is a logical Projection Generation. Fields are the retrieval
// mappings it pins; its projected text is built from them.
type Generation struct {
	ID, Collection, ProfileVersion, SpaceID string
	Fields                                  []corpus.Field
	// SourceNamespaceProjected reports that every object of the generation
	// carries its Record's Source Namespace, so search can filter on it.
	SourceNamespaceProjected bool
	// Spaces are the vector spaces whose named vectors the generation's
	// objects carry, the served one first. SpacesProjected is false for a
	// generation built before named spaces: it serves SpaceID only and
	// refuses a request for a named space until it is rebuilt.
	Spaces          []GenerationSpace
	SpacesProjected bool
}

// GenerationSpace is one vector space of a generation and the distance its
// index uses.
type GenerationSpace struct {
	ID     string `json:"id"`
	Metric string `json:"metric"`
}

// VectorSpaces lists the ids of the spaces the generation carries vectors
// for: its projected spaces, or the served space alone for a generation built
// before named spaces.
func (g Generation) VectorSpaces() []string {
	if !g.SpacesProjected || len(g.Spaces) == 0 {
		return []string{g.SpaceID}
	}
	out := make([]string, len(g.Spaces))
	for i, s := range g.Spaces {
		out[i] = s.ID
	}
	return out
}

// Carries reports whether the generation's objects carry vectors of a space.
func (g Generation) Carries(space string) bool {
	for _, id := range g.VectorSpaces() {
		if id == space {
			return true
		}
	}
	return false
}

type Candidate struct{ SegmentID, GenerationID string }
type Hydrated struct {
	RecordID, VersionID, SegmentationID string
	GenerationID                        string
	TextSHA256                          string
	EmbeddingID, SpaceID                string
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
		if !ok || p.Start < 0 || p.End < p.Start || p.End > len(text) || string(text[p.Start:p.End]) != p.Text {
			return ErrInvalid
		}
		if p.Start == p.End && p.Title == "" {
			return ErrInvalid
		}
		if p.TitleKey != "" {
			title, ok := parts[p.TitleKey]
			if !ok || string(title) != p.Title {
				return ErrInvalid
			}
		}
		d := p.Derivation
		if d.UTF8Start != len(string(text[:p.Start])) || d.UTF8End != len(string(text[:p.End])) || d.NormalizedSHA256 != Hash([]byte(string(text))) || d.ModelInputSHA256 != Hash([]byte(d.ModelInput)) || d.ModelTokens > 512 {
			return ErrInvalid
		}
		expected := SegmentID(org, result.ID, *p)
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

func SegmentID(org, segmentation string, p Segment) string {
	return StableID("segment", org, segmentation, p.PartKey, strconv.Itoa(p.Derivation.Ordinal), strconv.Itoa(p.Start), strconv.Itoa(p.End), strconv.Itoa(p.Derivation.UTF8Start), strconv.Itoa(p.Derivation.UTF8End), p.Derivation.NormalizedSHA256, Hash([]byte(p.Text)))
}
