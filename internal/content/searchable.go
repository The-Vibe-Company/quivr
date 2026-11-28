package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
	"unsafe"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
)

// Segmentation is an immutable derivation; offsets address canonical Part text.
type Segment struct {
	ID, PartKey, Text string
	Start, End        int
	TitleKey, Title   string
	Derivation        SegmentDerivation
	SourceExcerpts    []SourceExcerpt `json:"-"`
}

// SourceRange addresses an exact canonical Part slice using Unicode code points.
type SourceRange struct {
	PartKey string `json:"part_key"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
}
type SourceExcerpt struct {
	SourceRange
	Text string `json:"text"`
}

func (p Segment) AnchorText() string {
	if len(p.SourceExcerpts) > 0 {
		return p.SourceExcerpts[0].Text
	}
	return p.Text
}
func validSourceSeparator(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && utf8.RuneCountInString(s) <= 16
}

// SourceSlices validates reading order and resolves ranges from immutable Parts.
func SourceSlices(v Version, ranges []SourceRange) ([]SourceRange, []SourceExcerpt, error) {
	return newSourceParts(v).resolve(ranges)
}

// partText indexes one Part's text by code point once, so validating many
// segments of a Part costs one pass over it rather than one per segment.
//
// Coordinates address the code points of []rune(raw): each invalid UTF-8 byte
// decodes to its own U+FFFD. text is that decoding re-encoded, which is raw
// itself whenever raw is valid UTF-8, and offsets[i] is the byte offset of
// code point i in text, with offsets[len] == len(text).
type partText struct {
	raw     string
	text    string
	decoded bool
	offsets []int
	sha256  string
}

func (t *partText) canonical() string {
	if !t.decoded {
		t.text, t.decoded = t.raw, true
		if !utf8.ValidString(t.raw) {
			t.text = string([]rune(t.raw))
		}
	}
	return t.text
}

func (t *partText) index() []int {
	if t.offsets == nil {
		text := t.canonical()
		offsets := make([]int, 0, utf8.RuneCountInString(text)+1)
		for i := range text {
			offsets = append(offsets, i)
		}
		t.offsets = append(offsets, len(text))
	}
	return t.offsets
}

// runes is the Part's length in code points.
func (t *partText) runes() int { return len(t.index()) - 1 }

// slice is string([]rune(raw)[start:end]) for 0 <= start <= end <= runes().
// It shares the Part's text; callers that retain it beyond the Version clone it.
func (t *partText) slice(start, end int) string {
	offsets := t.index()
	return t.text[offsets[start]:offsets[end]]
}

// utf8Offset is len(string([]rune(raw)[:n])) for 0 <= n <= runes().
func (t *partText) utf8Offset(n int) int { return t.index()[n] }

// hash is Hash([]byte(string([]rune(raw)))).
func (t *partText) hash() string {
	if t.sha256 == "" {
		t.sha256 = Hash([]byte(t.canonical()))
	}
	return t.sha256
}

type sourceParts struct {
	texts map[string]*partText
	order map[string]int
}

func newSourceParts(v Version) *sourceParts {
	p := &sourceParts{texts: map[string]*partText{}, order: map[string]int{}}
	for i, part := range v.Manifest.Parts {
		if part.Content.Kind == "text" {
			p.texts[part.Key] = &partText{raw: part.Content.Text}
			p.order[part.Key] = i
		}
	}
	return p
}
func (p *sourceParts) resolve(ranges []SourceRange) ([]SourceRange, []SourceExcerpt, error) {
	if len(ranges) == 0 || len(ranges) > 256 {
		return nil, nil, ErrInvalid
	}
	out := make([]SourceExcerpt, len(ranges))
	previous := -1
	lastEnd := 0
	for i, r := range ranges {
		text, ok := p.texts[r.PartKey]
		pos := p.order[r.PartKey]
		if !ok || r.Start < 0 || r.End <= r.Start || r.End > text.runes() || r.End-r.Start > 4096 || pos < previous || pos == previous && r.Start < lastEnd {
			return nil, nil, ErrInvalid
		}
		// Excerpts outlive the Version on plugin segments: do not pin its Part.
		out[i] = SourceExcerpt{SourceRange: r, Text: strings.Clone(text.slice(r.Start, r.End))}
		previous = pos
		lastEnd = r.End
	}
	return append([]SourceRange(nil), ranges...), out, nil
}

// ImageRegion is normalized bounding-box provenance for a segment derived
// from a Blob Part (for example an image or crop).
type ImageRegion struct {
	CoordinateSystem string  `json:"coordinate_system"`
	X                float64 `json:"x"`
	Y                float64 `json:"y"`
	Width            float64 `json:"width"`
	Height           float64 `json:"height"`
	ImageWidth       int     `json:"image_width,omitempty"`
	ImageHeight      int     `json:"image_height,omitempty"`
	Label            string  `json:"label,omitempty"`
}

// Valid reports whether the region has valid normalized [0, 1] bounds.
func (r *ImageRegion) Valid() bool {
	if r == nil || r.CoordinateSystem != "normalized_bbox" {
		return false
	}
	for _, v := range []float64{r.X, r.Y, r.Width, r.Height} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	if r.X < 0 || r.X > 1 || r.Y < 0 || r.Y > 1 || r.Width <= 0 || r.Width > 1 || r.Height <= 0 || r.Height > 1 {
		return false
	}
	if r.X+r.Width > 1 || r.Y+r.Height > 1 {
		return false
	}
	if r.ImageWidth < 0 || r.ImageHeight < 0 {
		return false
	}
	if r.Label != "" && (!utf8.ValidString(r.Label) || strings.ContainsRune(r.Label, 0) || utf8.RuneCountInString(r.Label) > 128) {
		return false
	}
	return true
}

type SegmentDerivation struct {
	SourceRanges     []SourceRange `json:"source_ranges,omitempty"`
	SourceSeparator  string        `json:"source_separator,omitempty"`
	Region           *ImageRegion  `json:"region,omitempty"`
	Ordinal          int           `json:"ordinal"`
	UTF8Start        int           `json:"utf8_start"`
	UTF8End          int           `json:"utf8_end"`
	TokenStart       int           `json:"token_start"`
	TokenEnd         int           `json:"token_end"`
	Overlap          int           `json:"overlap_tokens"`
	HardStart        bool          `json:"hard_start"`
	HardEnd          bool          `json:"hard_end"`
	NormalizedSHA256 string        `json:"normalized_content_sha256"`
	TitleFullSHA256  string        `json:"full_title_sha256,omitempty"`
	TitleUsedSHA256  string        `json:"title_used_sha256,omitempty"`
	TitleTokens      int           `json:"title_tokens"`
	TitleTruncated   bool          `json:"title_truncated"`
	ModelInput       string        `json:"model_input"`
	ModelInputSHA256 string        `json:"model_input_sha256"`
	ModelTokens      int           `json:"model_tokens"`
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
	MetadataProjected        bool
	// ItemKeywordsProjected pins item BM25 independently of legacy passage indexes.
	ItemKeywordsProjected bool
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

// GenerationSpace is one vector space of a generation, the distance its
// index uses and, for a generation built since index settings, how the
// index stores its vectors.
type GenerationSpace struct {
	ID            string       `json:"id"`
	Metric        string       `json:"metric"`
	Role          string       `json:"role,omitempty"`
	OwnerPluginID string       `json:"owner_plugin_id,omitempty"`
	Index         *VectorIndex `json:"index,omitempty"`
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
	VersionID               string
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
	Title                               string
	CommonMetadata                      json.RawMessage
	TotalPassages                       int
	Segment                             Segment
	Availability                        Availability
}

// LeadSegmentRef identifies a Version's segmentation whose opening text
// segment should be located when a retrieval plugin opts in to lead passages.
type LeadSegmentRef struct {
	VersionID      string
	SegmentationID string
}

// LeadSegmentLocator resolves the opening (lowest-ordinal) text segment ID for
// each requested segmentation.
type LeadSegmentLocator interface {
	LeadSegments(ctx context.Context, org string, refs []LeadSegmentRef) (map[string]string, error)
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

// HydrationRechecker is an optional narrow serving fence. Repositories that
// provide it avoid loading immutable metadata again after blob reads.
type HydrationRechecker interface {
	RecheckHydration(context.Context, corpus.Scope, []Candidate) (map[int]bool, error)
}

// Located is a candidate's segment as canonical storage records it, with the
// immutable blob holding its Part text. For legacy unpacked multi-part
// segments, ManifestBlob carries the Version's canonical Manifest so hydration
// reads one object per Version instead of one per Part.
type Located struct {
	Hydrated
	Blob         Blob
	Pack         PackRange
	ManifestBlob Blob
	Sources      []LocatedSource
}
type LocatedSource struct {
	Range SourceRange
	Blob  Blob
	Pack  PackRange
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
	sources := newSourceParts(v)
	if result.VersionID != v.ID || result.Recipe == "" || len(result.Segments) == 0 {
		return ErrInvalid
	}
	// Index each Part once; per-segment checks below are proportional to the
	// segment, not to its Part.
	parts := map[string]*partText{}
	blobParts := map[string]string{}
	for i, p := range v.Manifest.Parts {
		if p.Content.Kind == "blob" {
			blobParts[p.Key] = p.Content.BlobSHA256
			continue
		}
		if shared, ok := sources.texts[p.Key]; ok && sources.order[p.Key] == i {
			parts[p.Key] = shared
			continue
		}
		parts[p.Key] = &partText{raw: p.Content.Text}
	}
	for i := range result.Segments {
		p := &result.Segments[i]
		if blobSHA, isBlob := blobParts[p.PartKey]; isBlob {
			if p.Start != 0 || p.End != 0 || p.Text != "" || len(p.Derivation.SourceRanges) > 0 || !p.Derivation.Region.Valid() {
				return ErrInvalid
			}
			if p.TitleKey != "" {
				title, ok := parts[p.TitleKey]
				if !ok || title.canonical() != p.Title {
					return ErrInvalid
				}
			}
			d := p.Derivation
			if d.UTF8Start != 0 || d.UTF8End != 0 || d.NormalizedSHA256 != blobSHA || d.ModelInputSHA256 != Hash([]byte(d.ModelInput)) || d.ModelTokens > 512 {
				return ErrInvalid
			}
			expected := SegmentID(org, result.ID, *p)
			if p.ID != expected {
				return ErrInvalid
			}
			continue
		}
		if p.Derivation.Region != nil {
			return ErrInvalid
		}
		text, ok := parts[p.PartKey]
		if !ok || p.Start < 0 || p.End < p.Start || p.End > text.runes() {
			return ErrInvalid
		}
		expectedText := text.slice(p.Start, p.End)
		if len(p.Derivation.SourceRanges) > 0 {
			ranges, slices, err := sources.resolve(p.Derivation.SourceRanges)
			if err != nil || ranges[0].PartKey != p.PartKey || ranges[0].Start != p.Start || ranges[0].End != p.End || !validSourceSeparator(p.Derivation.SourceSeparator) {
				return ErrInvalid
			}
			texts := make([]string, len(slices))
			for k, slice := range slices {
				texts[k] = slice.Text
			}
			expectedText = strings.Join(texts, p.Derivation.SourceSeparator)
		}
		if p.Text != expectedText {
			return ErrInvalid
		}
		if p.Start == p.End && p.Title == "" {
			return ErrInvalid
		}
		if p.TitleKey != "" {
			title, ok := parts[p.TitleKey]
			if !ok || title.canonical() != p.Title {
				return ErrInvalid
			}
		}
		d := p.Derivation
		if d.UTF8Start != text.utf8Offset(p.Start) || d.UTF8End != text.utf8Offset(p.End) || d.NormalizedSHA256 != text.hash() || d.ModelInputSHA256 != Hash([]byte(d.ModelInput)) || d.ModelTokens > 512 {
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

// DefaultHydrationBlobCacheBytes is the default in-memory budget for the
// content-addressed immutable text blob cache used during hydration (64 MiB).
const DefaultHydrationBlobCacheBytes int64 = 64 << 20

// MaxHydrationBlobCacheBytes bounds operator-configured hydration blob cache
// budgets (1 GiB).
const MaxHydrationBlobCacheBytes int64 = 1 << 30

// blobCacheEntryOverhead approximates the per-entry map bucket, doubly-linked
// list node, and string header footprint so tiny blobs cannot bypass maxBytes.
const blobCacheEntryOverhead int64 = 160

// textIndexOverhead approximates a rune index's header and pointer.
const textIndexOverhead int64 = 64

type blobCacheKey struct {
	organization string
	key          string
	sha256       string
	size         int64
}

func (k blobCacheKey) cacheable() bool {
	return k.organization != "" && k.key != "" && k.sha256 != "" && k.size >= 0
}

// blobCacheEntry is one verified immutable blob, resident in a cache or held
// only by the hydrations that read it.
type blobCacheEntry struct {
	key  blobCacheKey
	text string
	// valid and ascii describe the whole text once: when valid, every Part
	// cut at code point boundaries is valid canonical text.
	valid, ascii bool
	// index locates the code points of valid non-ASCII text. It is built on
	// the first cut and, while the entry is resident, charged to its bytes.
	index      atomic.Pointer[textIndex]
	bytes      int64
	prev, next *blobCacheEntry
}

func newBlobEntry(key blobCacheKey, text string) *blobCacheEntry {
	e := &blobCacheEntry{key: key, text: text, valid: ValidText(text)}
	e.ascii = e.valid && utf8.RuneCountInString(text) == len(text)
	e.bytes = blobEntryCost(key, text, nil)
	return e
}

// HydrationBlobCacheStats is a point-in-time snapshot of hydration cache usage.
type HydrationBlobCacheStats struct {
	Bytes    int64
	MaxBytes int64
	Entries  int
}

// HydrationBlobCache is a concurrency-safe, memory-budgeted LRU cache of
// immutable canonical text blobs keyed by (organization, blob_key, sha256, size).
// It stores verified immutable bytes only; Postgres authorization,
// current-version, quarantine, ACL, and Tombstone checks always run per request.
type HydrationBlobCache struct {
	maxBytes  int64
	mu        sync.Mutex
	entries   map[blobCacheKey]*blobCacheEntry
	head      *blobCacheEntry
	tail      *blobCacheEntry
	usedBytes int64

	lookups   *telemetry.Counter
	evictions *telemetry.Counter

	// flights shares concurrent cold reads of one blob across hydrations.
	flights blobFlights
}

var hydrationBlobCacheGauges = []telemetry.GaugeDefinition{
	{Name: "quivr_hydration_blob_cache_bytes", Help: "Current estimated bytes held in the hydration immutable text blob LRU cache.", Unit: "By"},
	{Name: "quivr_hydration_blob_cache_max_bytes", Help: "Configured maximum byte budget of the hydration immutable text blob LRU cache.", Unit: "By"},
	{Name: "quivr_hydration_blob_cache_entries", Help: "Current number of immutable text blobs held in the hydration LRU cache.", Unit: "{entry}"},
}

var (
	hydrationBlobCacheMetricsMu  sync.Mutex
	hydrationBlobCacheRegistered bool
	activeHydrationBlobCache     atomic.Pointer[HydrationBlobCache]
)

func registerHydrationBlobCacheGauges(c *HydrationBlobCache) {
	activeHydrationBlobCache.Store(c)
	if !telemetry.Enabled() {
		return
	}
	hydrationBlobCacheMetricsMu.Lock()
	defer hydrationBlobCacheMetricsMu.Unlock()
	if hydrationBlobCacheRegistered {
		return
	}
	telemetry.RegisterGauges(hydrationBlobCacheGauges, func(context.Context) ([]float64, error) {
		if active := activeHydrationBlobCache.Load(); active != nil {
			return active.gaugeValues(), nil
		}
		return []float64{0, 0, 0}, nil
	})
	hydrationBlobCacheRegistered = true
}

// NewHydrationBlobCache constructs a bounded content-addressed LRU cache for
// canonical text blobs and binds its Prometheus/OTel metrics. A non-positive
// maxBytes disables caching and returns nil.
func NewHydrationBlobCache(maxBytes int64) *HydrationBlobCache {
	if maxBytes <= 0 {
		return nil
	}
	c := &HydrationBlobCache{
		maxBytes:  maxBytes,
		entries:   map[blobCacheKey]*blobCacheEntry{},
		lookups:   telemetry.NewCounter("quivr_hydration_blob_cache_lookups_total", "Hydration immutable text blob cache lookups by result.", []string{"result"}, []string{"hit"}, []string{"miss"}),
		evictions: telemetry.NewCounter("quivr_hydration_blob_cache_evictions_total", "Hydration immutable text blob cache entries evicted to enforce the memory budget.", nil, nil),
	}
	registerHydrationBlobCacheGauges(c)
	return c
}

// Stats returns the current estimated byte usage, configured cap, and entry count.
func (c *HydrationBlobCache) Stats() HydrationBlobCacheStats {
	if c == nil {
		return HydrationBlobCacheStats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return HydrationBlobCacheStats{
		Bytes:    c.usedBytes,
		MaxBytes: c.maxBytes,
		Entries:  len(c.entries),
	}
}

func (c *HydrationBlobCache) gaugeValues() []float64 {
	s := c.Stats()
	return []float64{float64(s.Bytes), float64(s.MaxBytes), float64(s.Entries)}
}

// WriteMetrics renders the cache's Prometheus text metrics. A nil cache writes nothing.
func (c *HydrationBlobCache) WriteMetrics(w io.Writer) {
	if c == nil {
		return
	}
	c.lookups.Write(w)
	c.evictions.Write(w)
	values := c.gaugeValues()
	for i, g := range hydrationBlobCacheGauges {
		telemetry.Gauge(w, g.Name, g.Help, values[i])
	}
}

// blobEntryCost is the estimated memory of an entry holding text and, once
// built, its rune index.
func blobEntryCost(key blobCacheKey, text string, index *textIndex) int64 {
	cost := int64(len(text)) + int64(len(key.organization)+len(key.key)+len(key.sha256)) + blobCacheEntryOverhead
	if index != nil {
		cost += index.cost()
	}
	return cost
}

// blobGroup is one distinct blob a hydration batch cuts: its cache key, its
// resident entry when cached, and the span of its requests in batch order.
type blobGroup struct {
	key        blobCacheKey
	entry      *blobCacheEntry
	start, end int
}

// getBatch sets the resident entry of every cached group and reports hits.
func (c *HydrationBlobCache) getBatch(groups []blobGroup) int {
	if c == nil || len(groups) == 0 {
		return 0
	}
	hitCount, missCount := 0, 0
	c.mu.Lock()
	for i := range groups {
		if !groups[i].key.cacheable() {
			continue
		}
		if e, ok := c.entries[groups[i].key]; ok {
			c.moveToFront(e)
			groups[i].entry = e
			hitCount++
		} else {
			missCount++
		}
	}
	c.mu.Unlock()
	if hitCount > 0 {
		c.lookups.Add(hitCount, "hit")
	}
	if missCount > 0 {
		c.lookups.Add(missCount, "miss")
	}
	return hitCount
}

// put admits a new, unshared entry and returns the resident entry for its
// key: e, or the one already cached. An entry over the budget stays transient.
func (c *HydrationBlobCache) put(e *blobCacheEntry) *blobCacheEntry {
	if c == nil || !e.key.cacheable() {
		return e
	}
	e.bytes = blobEntryCost(e.key, e.text, e.index.Load())
	if e.bytes > c.maxBytes {
		return e
	}
	c.mu.Lock()
	if existing, ok := c.entries[e.key]; ok {
		c.moveToFront(existing)
		c.mu.Unlock()
		return existing
	}
	evicted := c.evictLocked(e.bytes)
	c.entries[e.key] = e
	c.pushFront(e)
	c.usedBytes += e.bytes
	c.mu.Unlock()
	if evicted > 0 {
		c.evictions.Add(evicted)
	}
	return e
}

// runeIndex returns the code point index of e's valid text, building it on
// first use. A resident entry is charged for the index, which may evict
// least recently used entries, e included, to stay within the budget.
func (c *HydrationBlobCache) runeIndex(e *blobCacheEntry) textIndex {
	if e.ascii {
		return textIndex{text: e.text, runes: len(e.text)}
	}
	if ix := e.index.Load(); ix != nil {
		return *ix
	}
	ix := newTextIndex(e.text)
	if !e.index.CompareAndSwap(nil, ix) {
		return *e.index.Load()
	}
	if c == nil {
		return *ix
	}
	evicted := 0
	c.mu.Lock()
	if c.entries[e.key] == e {
		cost := blobEntryCost(e.key, e.text, ix)
		c.usedBytes += cost - e.bytes
		e.bytes = cost
		evicted = c.evictLocked(0)
	}
	c.mu.Unlock()
	if evicted > 0 {
		c.evictions.Add(evicted)
	}
	return *ix
}

// evictLocked evicts least recently used entries until incoming more bytes
// fit the budget, and returns how many it evicted.
func (c *HydrationBlobCache) evictLocked(incoming int64) int {
	evicted := 0
	for c.usedBytes+incoming > c.maxBytes && c.tail != nil {
		victim := c.tail
		c.unlink(victim)
		delete(c.entries, victim.key)
		c.usedBytes -= victim.bytes
		evicted++
	}
	return evicted
}

func (c *HydrationBlobCache) moveToFront(e *blobCacheEntry) {
	if c.head == e {
		return
	}
	c.unlink(e)
	c.pushFront(e)
}

func (c *HydrationBlobCache) pushFront(e *blobCacheEntry) {
	e.prev = nil
	e.next = c.head
	if c.head != nil {
		c.head.prev = e
	} else {
		c.tail = e
	}
	c.head = e
}

func (c *HydrationBlobCache) unlink(e *blobCacheEntry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		c.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		c.tail = e.prev
	}
	e.prev = nil
	e.next = nil
}

// runeIndexStride is the code point spacing of a textIndex's byte offset
// marks: a lookup steps over at most runeIndexStride-1 code points, and the
// marks of non-ASCII text cost at most 1/16 of its bytes.
const runeIndexStride = 128

// textIndex locates code points in valid canonical text without decoding it
// to []rune. ASCII text addresses bytes directly; other text keeps the byte
// offset of every runeIndexStride-th code point and steps forward from the
// nearest one. Coordinates are those of []rune(text).
type textIndex struct {
	text  string
	runes int
	marks []int // nil for ASCII text
}

// newTextIndex indexes valid non-ASCII text.
func newTextIndex(text string) *textIndex {
	n := utf8.RuneCountInString(text)
	marks := make([]int, 0, (n+runeIndexStride-1)/runeIndexStride)
	for i, off := 0, 0; off < len(text); i++ {
		if i%runeIndexStride == 0 {
			marks = append(marks, off)
		}
		off += utf8Len(text[off])
	}
	return &textIndex{text: text, runes: n, marks: marks}
}

// indexText indexes one Part's text, or reports that it is not valid
// canonical text.
func indexText(text string) (textIndex, bool) {
	if !ValidText(text) {
		return textIndex{}, false
	}
	if utf8.RuneCountInString(text) == len(text) {
		return textIndex{text: text, runes: len(text)}, true
	}
	return *newTextIndex(text), true
}

func (x *textIndex) cost() int64 { return textIndexOverhead + int64(cap(x.marks))*8 }

// utf8Len is the byte length of the code point that lead byte b starts in
// valid UTF-8.
func utf8Len(b byte) int {
	switch {
	case b < 0x80:
		return 1
	case b < 0xE0:
		return 2
	case b < 0xF0:
		return 3
	}
	return 4
}

// step is the byte offset k code points after byte offset off.
func (x *textIndex) step(off, k int) int {
	if x.marks == nil {
		return off + k
	}
	for ; k > 0; k-- {
		off += utf8Len(x.text[off])
	}
	return off
}

// offset is the byte offset of code point n, for 0 <= n <= runes.
func (x *textIndex) offset(n int) int {
	if x.marks == nil {
		return n
	}
	if n == x.runes {
		return len(x.text)
	}
	return x.step(x.marks[n/runeIndexStride], n%runeIndexStride)
}

// runeAt is the number of code points before byte offset b, a code point
// boundary of text.
func (x *textIndex) runeAt(b int) int {
	if x.marks == nil {
		return b
	}
	i := sort.SearchInts(x.marks, b+1) - 1
	n, off := i*runeIndexStride, x.marks[i]
	for off < b {
		off += utf8Len(x.text[off])
		n++
	}
	return n
}

// cut is an independent copy of code points [start, end) of the Part at
// bytes [b0, b1) of text, whose ends are code point boundaries, so callers
// never pin a cache entry. It is string([]rune(text[b0:b1])[start:end]) when
// that slice is in bounds.
func (x *textIndex) cut(b0, b1, start, end int) (string, bool) {
	if start < 0 || end < start {
		return "", false
	}
	base := x.runeAt(b0)
	if end > x.runes-base {
		return "", false
	}
	from := x.offset(base + start)
	var to int
	if end-start < runeIndexStride {
		to = x.step(from, end-start)
	} else {
		to = x.offset(base + end)
	}
	if to > b1 {
		return "", false
	}
	return strings.Clone(x.text[from:to]), true
}

// runeBoundary reports whether byte offset i of s starts a code point or ends s.
func runeBoundary(s string, i int) bool { return i == len(s) || utf8.RuneStart(s[i]) }

// blobReadParallelism bounds the concurrent canonical blob reads of one
// hydration batch, staying within the S3 transport's idle connection pool
// while letting a 50-hit page complete in a single round-trip wave.
const blobReadParallelism = 64

// HydrationAtomicityChecker reports whether Baseline.Hydrate executes under a
// single consistent visibility fence for a batch of n candidates. Backends that
// shard large batches across independent connections return false so Service.Hydrate
// preserves the post-lookup RecheckHydration fence even when BlobCache serves
// every excerpt locally.
type HydrationAtomicityChecker interface {
	HydrateIsAtomic(ctx context.Context, candidates int) bool
}

// Hydrate rechecks candidates and reads their excerpts from canonical storage
// as one batch: one lookup, one read per distinct uncached blob, then one recheck
// when any uncached blob was read from canonical storage or the initial lookup
// spanned multiple independent shards, since access and currentness may change
// while bytes are fetched or between shards. When every blob is served
// synchronously from BlobCache under a single atomic lookup, the initial
// Baseline.Hydrate already enforces every visibility and currentness fence.
// The result holds, at each candidate's index, its hydration, or nothing when
// the caller may not read it or it is no longer current.
func (s Service) Hydrate(ctx context.Context, scope corpus.Scope, cs []Candidate) (map[int]Hydrated, error) {
	if err := scope.Require(corpus.ActionContentHydrate); err != nil {
		return nil, err
	}
	if len(cs) == 0 {
		return map[int]Hydrated{}, nil
	}
	located, err := s.Baseline.Hydrate(ctx, scope, cs)
	if err != nil {
		return nil, err
	}
	excerpts, readBlobs, err := s.excerpts(ctx, scope.Organization, located)
	if err != nil {
		return nil, err
	}
	out := make(map[int]Hydrated, len(located))
	found := make([]int, 0, len(located))
	for i := range cs {
		l, ok := located[i]
		if !ok {
			continue
		}
		l.Hydrated.Segment.Text = excerpts[i]
		out[i] = l.Hydrated
		found = append(found, i)
	}
	atomicLookup := true
	if checker, ok := s.Baseline.(HydrationAtomicityChecker); ok {
		atomicLookup = checker.HydrateIsAtomic(ctx, len(cs))
	}
	if len(found) == 0 || (!readBlobs && atomicLookup) {
		return out, nil
	}
	recheck := make([]Candidate, len(found))
	for j, i := range found {
		recheck[j] = cs[i]
	}
	still, err := s.RecheckHydration(ctx, scope, recheck)
	if err != nil {
		return nil, err
	}
	for j, i := range found {
		if !still[j] {
			delete(out, i)
		}
	}
	return out, nil
}

// RecheckHydration verifies currentness and access without rereading Parts.
func (s Service) RecheckHydration(ctx context.Context, scope corpus.Scope, candidates []Candidate) (map[int]bool, error) {
	if err := scope.Require(corpus.ActionContentHydrate); err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	if guard, ok := s.Baseline.(HydrationRechecker); ok {
		return guard.RecheckHydration(ctx, scope, candidates)
	}
	hydrated, err := s.Baseline.Hydrate(ctx, scope, candidates)
	if err != nil {
		return nil, err
	}
	still := make(map[int]bool, len(hydrated))
	for i := range hydrated {
		still[i] = true
	}
	return still, nil
}

// LeadSegments resolves the opening (lowest-ordinal) text segment ID per
// segmentation when the underlying BaselineRepository supports it.
func (s Service) LeadSegments(ctx context.Context, scope corpus.Scope, refs []LeadSegmentRef) (map[string]string, error) {
	if err := scope.Require(corpus.ActionContentHydrate); err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return map[string]string{}, nil
	}
	locator, ok := s.Baseline.(LeadSegmentLocator)
	if !ok {
		return map[string]string{}, nil
	}
	return locator.LeadSegments(ctx, scope.Organization, refs)
}

// excerpts reads each distinct blob of a batch once, serving verified immutable
// blobs from BlobCache when present and reading only misses concurrently from
// canonical storage, then cuts and checksum-verifies each candidate's excerpt.
// Concurrent batches missing the same cacheable blob share one read. The first
// failed read fails the batch at once and cancels its other reads. It also
// reports whether any uncached blob had to be read from canonical storage.
func (s Service) excerpts(ctx context.Context, org string, located map[int]Located) (map[int]string, bool, error) {
	// A request cuts one source excerpt of a candidate from its blob, into
	// pieces[at], or, with ranges, all of a candidate's excerpts from its
	// legacy Manifest blob, into pieces[at:at+len(ranges)].
	type request struct {
		group, at int
		source    SourceRange
		pack      PackRange
		ranges    []SourceRange
	}
	var regionOnly map[int]bool
	total := 0
	for i, l := range located {
		switch {
		case len(l.Sources) == 0 && l.Blob.Key == "" && l.ManifestBlob.Key == "" && l.Segment.Derivation.Region != nil && l.Segment.Start == 0 && l.Segment.End == 0:
			if regionOnly == nil {
				regionOnly = map[int]bool{}
			}
			regionOnly[i] = true
		case len(l.Sources) == 0 && l.ManifestBlob.Key != "" && len(l.Segment.Derivation.SourceRanges) > 0:
			total += len(l.Segment.Derivation.SourceRanges)
		case len(l.Sources) > 0:
			total += len(l.Sources)
		default:
			total++
		}
	}
	// pieces holds every candidate's source excerpts; slices views each
	// candidate's own, capped so a caller appending cannot reach another's.
	pieces := make([]SourceExcerpt, total)
	slices := make(map[int][]SourceExcerpt, len(located)-len(regionOnly))
	requests := make([]request, 0, total)
	groups := make([]blobGroup, 0, total)
	groupOf := make(map[string]int, total)
	addRequest := func(blob Blob, r request) error {
		g, ok := groupOf[blob.Key]
		if !ok {
			g = len(groups)
			groupOf[blob.Key] = g
			groups = append(groups, blobGroup{key: blobCacheKey{organization: org, key: blob.Key, sha256: blob.SHA256, size: blob.Size}})
		} else if k := groups[g].key; k.sha256 != blob.SHA256 || k.size != blob.Size {
			return ErrArtifactCorrupt
		}
		r.group = g
		groups[g].end++
		requests = append(requests, r)
		return nil
	}
	at := 0
	for i, l := range located {
		if regionOnly[i] {
			continue
		}
		var err error
		n := 1
		switch {
		case len(l.Sources) == 0 && l.ManifestBlob.Key != "" && len(l.Segment.Derivation.SourceRanges) > 0:
			n = len(l.Segment.Derivation.SourceRanges)
			err = addRequest(l.ManifestBlob, request{at: at, ranges: l.Segment.Derivation.SourceRanges})
		case len(l.Sources) > 0:
			n = len(l.Sources)
			for k, source := range l.Sources {
				if err = addRequest(source.Blob, request{at: at + k, source: source.Range, pack: source.Pack}); err != nil {
					break
				}
			}
		default:
			err = addRequest(l.Blob, request{at: at, source: SourceRange{PartKey: l.Segment.PartKey, Start: l.Segment.Start, End: l.Segment.End}, pack: l.Pack})
		}
		if err != nil {
			return nil, false, err
		}
		slices[i] = pieces[at : at+n : at+n]
		at += n
	}
	// Order requests by group: groups[g] owns order[groups[g].start:groups[g].end].
	next := 0
	for g := range groups {
		count := groups[g].end
		groups[g].start, groups[g].end = next, next
		next += count
	}
	order := make([]int, len(requests))
	for j, r := range requests {
		order[groups[r.group].end] = j
		groups[r.group].end++
	}
	// cutBlob writes the excerpts of one group's requests into their pieces;
	// requests of distinct groups write distinct pieces.
	cutBlob := func(e *blobCacheEntry, g blobGroup) error {
		blob := e.text
		var manifestParts map[string]string
		var manifestIndexes map[string]textIndex
		var whole textIndex
		indexed := false
		var parts map[[2]int64]textIndex
		for _, j := range order[g.start:g.end] {
			req := &requests[j]
			if len(req.ranges) > 0 {
				if manifestParts == nil {
					var manifest Manifest
					if err := json.Unmarshal([]byte(blob), &manifest); err != nil {
						return ErrArtifactCorrupt
					}
					manifestParts = make(map[string]string, len(manifest.Parts))
					manifestIndexes = make(map[string]textIndex, len(manifest.Parts))
					for _, part := range manifest.Parts {
						if part.Content.Kind == "text" {
							manifestParts[part.Key] = part.Content.Text
						}
					}
				}
				for k, r := range req.ranges {
					ix, ok := manifestIndexes[r.PartKey]
					if !ok {
						partText, exists := manifestParts[r.PartKey]
						if !exists {
							return ErrArtifactCorrupt
						}
						if ix, ok = indexText(partText); !ok {
							return ErrArtifactCorrupt
						}
						manifestIndexes[r.PartKey] = ix
					}
					cut, valid := ix.cut(0, len(ix.text), r.Start, r.End)
					if !valid {
						return ErrArtifactCorrupt
					}
					pieces[req.at+k] = SourceExcerpt{SourceRange: r, Text: cut}
				}
				continue
			}
			bounds, err := req.pack.bounds(int64(len(blob)))
			if err != nil {
				return err
			}
			b0, b1 := int(bounds[0]), int(bounds[1])
			var ix textIndex
			if e.valid {
				// Within valid text, a Part is valid exactly when it is cut at
				// code point boundaries, so the blob's index serves every Part.
				if !runeBoundary(blob, b0) || !runeBoundary(blob, b1) {
					return ErrArtifactCorrupt
				}
				if !indexed {
					whole, indexed = s.BlobCache.runeIndex(e), true
				}
				ix = whole
			} else {
				var ok bool
				if ix, ok = parts[bounds]; !ok {
					if ix, ok = indexText(blob[b0:b1]); !ok {
						return ErrArtifactCorrupt
					}
					if parts == nil {
						parts = map[[2]int64]textIndex{}
					}
					parts[bounds] = ix
				}
				b0, b1 = 0, len(ix.text)
			}
			r := req.source
			cut, valid := ix.cut(b0, b1, r.Start, r.End)
			if !valid {
				return ErrArtifactCorrupt
			}
			pieces[req.at] = SourceExcerpt{SourceRange: r, Text: cut}
		}
		return nil
	}
	s.BlobCache.getBatch(groups)
	var misses []int
	for g := range groups {
		if e := groups[g].entry; e != nil {
			if err := cutBlob(e, groups[g]); err != nil {
				return nil, false, err
			}
			continue
		}
		misses = append(misses, g)
	}
	readBlobs := len(misses) > 0
	if readBlobs {
		batch, cancel := context.WithCancel(ctx)
		defer cancel()
		var mu sync.Mutex
		var failure error
		fail := func(err error) {
			mu.Lock()
			if failure == nil {
				failure = err
			}
			mu.Unlock()
			cancel()
		}
		var wg sync.WaitGroup
		slots := make(chan struct{}, min(blobReadParallelism, len(misses)))
		for _, g := range misses {
			select {
			case slots <- struct{}{}:
			case <-batch.Done():
			}
			if batch.Err() != nil {
				fail(ctx.Err())
				break
			}
			wg.Add(1)
			go func(g blobGroup) {
				defer wg.Done()
				defer func() { <-slots }()
				defer func() {
					if r := recover(); r != nil {
						fail(recoveredBlobPanic(r))
					}
				}()
				// Verify the whole shared object once through Read before slicing Parts.
				e, err := s.readBlob(batch, g.key)
				if err == nil {
					err = cutBlob(e, g)
				}
				if err != nil {
					fail(err)
				}
			}(groups[g])
		}
		wg.Wait()
		if failure != nil {
			return nil, readBlobs, failure
		}
	}
	out := make(map[int]string, len(located))
	for i := range regionOnly {
		if !hashMatches("", located[i].TextSHA256) {
			return nil, readBlobs, fmt.Errorf("canonical excerpt mismatch: %w", ErrArtifactCorrupt)
		}
		out[i] = ""
	}
	for i, pieces := range slices {
		text := pieces[0].Text
		if len(pieces) > 1 {
			separator := located[i].Segment.Derivation.SourceSeparator
			n := len(separator) * (len(pieces) - 1)
			for _, p := range pieces {
				n += len(p.Text)
			}
			var b strings.Builder
			b.Grow(n)
			for k, p := range pieces {
				if k > 0 {
					b.WriteString(separator)
				}
				b.WriteString(p.Text)
			}
			text = b.String()
		}
		if !hashMatches(text, located[i].TextSHA256) {
			return nil, readBlobs, fmt.Errorf("canonical excerpt mismatch: %w", ErrArtifactCorrupt)
		}
		out[i] = text
		l := located[i]
		if len(l.Sources) > 0 || (l.ManifestBlob.Key != "" && len(l.Segment.Derivation.SourceRanges) > 0) {
			l.Segment.SourceExcerpts = pieces
			located[i] = l
		}
	}
	return out, readBlobs, nil
}

// readBlob reads a missed blob, through one shared flight per cacheable key
// when BlobCache is set, and admits it to BlobCache only when its bytes match
// the size and SHA-256 that canonical storage recorded.
func (s Service) readBlob(ctx context.Context, key blobCacheKey) (*blobCacheEntry, error) {
	read := func(ctx context.Context) (*blobCacheEntry, error) {
		data, err := s.Blobs.Read(ctx, Blob{Key: key.key, SHA256: key.sha256, Size: key.size})
		if err != nil {
			return nil, err
		}
		e := newBlobEntry(key, string(data))
		if s.BlobCache != nil && key.cacheable() && int64(len(data)) == key.size && Hash(data) == key.sha256 {
			e = s.BlobCache.put(e)
		}
		return e, nil
	}
	if s.BlobCache == nil || !key.cacheable() {
		return read(ctx)
	}
	return s.BlobCache.flights.do(ctx, key, read)
}

// hashMatches reports whether Hash([]byte(s)) == want without copying s or
// allocating the digest: SHA-256 only reads its input.
func hashMatches(s, want string) bool {
	sum := sha256.Sum256(unsafe.Slice(unsafe.StringData(s), len(s)))
	var digest [2 * sha256.Size]byte
	hex.Encode(digest[:], sum[:])
	return string(digest[:]) == want
}

// bounds returns a half-open byte interval. Check the length with subtraction
// first so corrupt BIGINT offsets cannot overflow during addition.
func (p PackRange) bounds(size int64) ([2]int64, error) {
	if p.Offset == nil && p.Length == nil {
		return [2]int64{0, size}, nil
	}
	if p.Offset == nil || p.Length == nil {
		return [2]int64{}, ErrArtifactCorrupt
	}
	offset, length := *p.Offset, *p.Length
	if offset < 0 || offset > size || length <= 0 || length > size-offset {
		return [2]int64{}, ErrArtifactCorrupt
	}
	return [2]int64{offset, offset + length}, nil
}

func SegmentID(org, segmentation string, p Segment) string {
	base := StableID("segment", org, segmentation, p.PartKey, strconv.Itoa(p.Derivation.Ordinal), strconv.Itoa(p.Start), strconv.Itoa(p.End), strconv.Itoa(p.Derivation.UTF8Start), strconv.Itoa(p.Derivation.UTF8End), p.Derivation.NormalizedSHA256, Hash([]byte(p.Text)))
	if p.Derivation.Region != nil {
		raw, _ := json.Marshal(p.Derivation.Region)
		return StableID("region-segment", base, string(raw))
	}
	if len(p.Derivation.SourceRanges) == 0 {
		return base
	}
	raw, _ := json.Marshal(p.Derivation.SourceRanges)
	return StableID("packed-segment", base, string(raw), p.Derivation.SourceSeparator)
}
