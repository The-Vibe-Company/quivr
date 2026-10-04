package content

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"

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
	// IngestionRouting selects the served projection by source media type.
	// Nil retains the routing of generations recorded before evaluations.
	IngestionRouting *IngestionRouting
}

type IngestionRouting struct {
	Default string            `json:"default"`
	Routes  map[string]string `json:"routes,omitempty"`
}

func (r IngestionRouting) For(mediaType string) string {
	if mediaType == "" {
		mediaType = "text/plain"
	}
	if owner := r.Routes[mediaType]; owner != "" {
		return owner
	}
	return r.Default
}

// GenerationSpace is one vector space of a generation and the distance its
// index uses.
type GenerationSpace struct {
	ID            string `json:"id"`
	Metric        string `json:"metric"`
	Role          string `json:"role,omitempty"`
	OwnerPluginID string `json:"owner_plugin_id,omitempty"`
}

// ServedFor returns the served space owned by a plugin in this generation.
// Generations recorded before owner roles keep their single served SpaceID.
func (g Generation) ServedFor(pluginID string) string {
	for _, sp := range g.Spaces {
		if sp.OwnerPluginID == pluginID && sp.Role == SpaceServed {
			return sp.ID
		}
	}
	for _, sp := range g.Spaces {
		if sp.OwnerPluginID != "" {
			return ""
		}
	}
	return g.SpaceID
}

// Serves reports whether this generation serves a space, including legacy ones.
func (g Generation) Serves(space string) bool {
	for _, sp := range g.Spaces {
		if sp.ID == space && sp.Role != "" {
			return sp.Role == SpaceServed
		}
	}
	return space == g.SpaceID
}

// PluginOfRecipe is the producer plugin id of a plugin segmentation recipe.
func PluginOfRecipe(recipe string) string {
	if !strings.HasPrefix(recipe, "plugin:") {
		return ""
	}
	id, _, _ := strings.Cut(strings.TrimPrefix(recipe, "plugin:"), "@")
	return id
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

// Candidate is one projected object a search found, with the index's score
// for that query (higher is better).
type Candidate struct {
	SegmentID, GenerationID string
	Score                   float64
	// EvaluationPlugin explicitly selects a separate owner's projection.
	EvaluationPlugin string
	EvaluationSpace  string
}
type Hydrated struct {
	RecordID, VersionID, SegmentationID string
	GenerationID                        string
	TextSHA256                          string
	EmbeddingID, SpaceID                string
	Segment                             Segment
	Availability                        Availability
}

// Quarantine stages: the step a quarantined Version failed at, which a
// reprocess reruns. A Version is quarantined at normalization when it is
// published quarantined (its normalizer failed or its route was removed), and
// at ingestion when its baseline was refused or stopped.
const (
	QuarantineNormalization = "normalization"
	QuarantineIngestion     = "ingestion"
)

type BaselineRepository interface {
	SaveSegmentation(context.Context, string, Segmentation) error
	BaselineProgress(context.Context, string, string, string, string, bool) error
	// QuarantineVersion quarantines a Version that is not searchable yet,
	// with its structured reason, like BaselineProgress with quarantined.
	QuarantineVersion(ctx context.Context, org, versionID string, reason Diagnostic) error
	Promote(context.Context, string, Segmentation, Generation) error
	// Hydrate looks candidates up in one batch: at each candidate's index,
	// its segment and the blob holding its Part text, or nothing when the
	// caller may not read it or it is no longer current and routed.
	Hydrate(context.Context, corpus.Scope, []Candidate) (map[int]Located, error)
}

// Located is a candidate's segment as canonical storage records it, with the
// immutable blob holding its Part text.
type Located struct {
	Hydrated
	Blob Blob
}

func (s Service) ProcessingVersion(ctx context.Context, org, receiptID string) (Version, error) {
	r, err := s.Receipts.Receipt(ctx, org, receiptID)
	if err != nil {
		return Version{}, err
	}
	if r.VersionID == "" {
		return Version{}, nil
	}
	return s.TrustedVersion(ctx, org, r.Source.CorpusID, r.RecordID, r.VersionID)
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

// QuarantineVersion quarantines a Version that is not searchable yet with a
// structured reason, listed in its diagnostics.
func (s Service) QuarantineVersion(ctx context.Context, org, versionID string, reason Diagnostic) error {
	return s.Baseline.QuarantineVersion(ctx, org, versionID, reason)
}
func (s Service) Promote(ctx context.Context, org string, seg Segmentation, g Generation) error {
	return s.Baseline.Promote(ctx, org, seg, g)
}

// blobReadParallelism bounds the concurrent canonical blob reads of one
// hydration batch.
const blobReadParallelism = 16

// Hydrate rechecks candidates and reads their excerpts from canonical storage
// as one batch: one lookup, one read per distinct blob, then one recheck,
// since access and currentness may change while the immutable bytes are read.
// The result holds, at each candidate's index, its hydration, or nothing when
// the caller may not read it or it is no longer current.
func (s Service) Hydrate(ctx context.Context, scope corpus.Scope, cs []Candidate) (map[int]Hydrated, error) {
	if err := scope.Require(corpus.ActionContentHydrate); err != nil {
		return nil, err
	}
	out := map[int]Hydrated{}
	if len(cs) == 0 {
		return out, nil
	}
	located, err := s.Baseline.Hydrate(ctx, scope, cs)
	if err != nil {
		return nil, err
	}
	excerpts, err := s.excerpts(ctx, located)
	if err != nil {
		return nil, err
	}
	var found []int
	for i := range cs {
		l, ok := located[i]
		if !ok {
			continue
		}
		l.Hydrated.Segment.Text = excerpts[i]
		out[i] = l.Hydrated
		found = append(found, i)
	}
	if len(found) == 0 {
		return out, nil
	}
	recheck := make([]Candidate, len(found))
	for j, i := range found {
		recheck[j] = cs[i]
	}
	still, err := s.Baseline.Hydrate(ctx, scope, recheck)
	if err != nil {
		return nil, err
	}
	for j, i := range found {
		if _, ok := still[j]; !ok {
			delete(out, i)
		}
	}
	return out, nil
}

// excerpts reads each distinct blob of a batch once, a few at a time, and
// cuts from it the excerpts of its candidates, each checked against the
// segment's checksum. Only the excerpts outlive a read.
func (s Service) excerpts(ctx context.Context, located map[int]Located) (map[int]string, error) {
	byBlob := map[string][]int{}
	for i, l := range located {
		byBlob[l.Blob.Key] = append(byBlob[l.Blob.Key], i)
	}
	out := make(map[int]string, len(located))
	var mu sync.Mutex
	var failure error
	var wg sync.WaitGroup
	slots := make(chan struct{}, blobReadParallelism)
	for _, at := range byBlob {
		wg.Add(1)
		slots <- struct{}{}
		go func(at []int) {
			defer func() { <-slots; wg.Done() }()
			cut, err := s.cut(ctx, located, at)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failure = err
				return
			}
			for i, excerpt := range cut {
				out[i] = excerpt
			}
		}(at)
	}
	wg.Wait()
	return out, failure
}

// cut reads the blob the candidates at the given indexes share and returns
// their excerpts.
func (s Service) cut(ctx context.Context, located map[int]Located, at []int) (map[int]string, error) {
	data, err := s.Blobs.Read(ctx, located[at[0]].Blob)
	if err != nil {
		return nil, err
	}
	runes := []rune(string(data))
	out := make(map[int]string, len(at))
	for _, i := range at {
		seg := located[i].Segment
		if seg.Start < 0 || seg.End > len(runes) || seg.End < seg.Start {
			return nil, errors.New("invalid canonical excerpt")
		}
		excerpt := string(runes[seg.Start:seg.End])
		// Repository supplies the checksum of the segment, not a projection excerpt.
		if Hash([]byte(excerpt)) != located[i].TextSHA256 {
			return nil, errors.New("canonical excerpt mismatch")
		}
		out[i] = excerpt
	}
	return out, nil
}

func SegmentID(org, segmentation string, p Segment) string {
	return StableID("segment", org, segmentation, p.PartKey, strconv.Itoa(p.Derivation.Ordinal), strconv.Itoa(p.Start), strconv.Itoa(p.End), strconv.Itoa(p.Derivation.UTF8Start), strconv.Itoa(p.Derivation.UTF8End), p.Derivation.NormalizedSHA256, Hash([]byte(p.Text)))
}
