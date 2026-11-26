package content

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

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

type sourceParts struct {
	texts map[string]string
	runes map[string][]rune
	order map[string]int
}

func newSourceParts(v Version) *sourceParts {
	p := &sourceParts{texts: map[string]string{}, runes: map[string][]rune{}, order: map[string]int{}}
	for i, part := range v.Manifest.Parts {
		if part.Content.Kind == "text" {
			p.texts[part.Key] = part.Content.Text
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
		raw, ok := p.texts[r.PartKey]
		text, decoded := p.runes[r.PartKey]
		if ok && !decoded {
			text = []rune(raw)
			p.runes[r.PartKey] = text
		}
		pos := p.order[r.PartKey]
		if !ok || r.Start < 0 || r.End <= r.Start || r.End > len(text) || r.End-r.Start > 4096 || pos < previous || pos == previous && r.Start < lastEnd {
			return nil, nil, ErrInvalid
		}
		out[i] = SourceExcerpt{SourceRange: r, Text: string(text[r.Start:r.End])}
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
	parts := map[string][]rune{}
	blobParts := map[string]string{}
	for _, p := range v.Manifest.Parts {
		if p.Content.Kind == "blob" {
			blobParts[p.Key] = p.Content.BlobSHA256
			continue
		}
		parts[p.Key] = []rune(p.Content.Text)
	}
	for i := range result.Segments {
		p := &result.Segments[i]
		if blobSHA, isBlob := blobParts[p.PartKey]; isBlob {
			if p.Start != 0 || p.End != 0 || p.Text != "" || len(p.Derivation.SourceRanges) > 0 || !p.Derivation.Region.Valid() {
				return ErrInvalid
			}
			if p.TitleKey != "" {
				title, ok := parts[p.TitleKey]
				if !ok || string(title) != p.Title {
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
		if !ok || p.Start < 0 || p.End < p.Start || p.End > len(text) {
			return ErrInvalid
		}
		expectedText := string(text[p.Start:p.End])
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

// DefaultHydrationBlobCacheBytes is the default in-memory budget for the
// content-addressed immutable text blob cache used during hydration (64 MiB).
const DefaultHydrationBlobCacheBytes int64 = 64 << 20

// MaxHydrationBlobCacheBytes bounds operator-configured hydration blob cache
// budgets (1 GiB).
const MaxHydrationBlobCacheBytes int64 = 1 << 30

// blobCacheEntryOverhead approximates the per-entry map bucket, doubly-linked
// list node, and string header footprint so tiny blobs cannot bypass maxBytes.
const blobCacheEntryOverhead int64 = 160

type blobCacheKey struct {
	organization string
	key          string
	sha256       string
	size         int64
}

func (k blobCacheKey) cacheable() bool {
	return k.organization != "" && k.key != "" && k.sha256 != "" && k.size >= 0
}

type blobCacheEntry struct {
	key        blobCacheKey
	text       string
	bytes      int64
	prev, next *blobCacheEntry
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

func blobEntryCost(key blobCacheKey, text string) int64 {
	return int64(len(text)) + int64(len(key.organization)+len(key.key)+len(key.sha256)) + blobCacheEntryOverhead
}

func (c *HydrationBlobCache) getBatch(keys []blobCacheKey) (map[blobCacheKey]string, int) {
	if c == nil || len(keys) == 0 {
		return nil, 0
	}
	var hits map[blobCacheKey]string
	hitCount, missCount := 0, 0
	c.mu.Lock()
	for _, key := range keys {
		if !key.cacheable() {
			continue
		}
		if e, ok := c.entries[key]; ok {
			c.moveToFront(e)
			if hits == nil {
				hits = make(map[blobCacheKey]string, len(keys))
			}
			hits[key] = e.text
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
	return hits, hitCount
}

func (c *HydrationBlobCache) put(key blobCacheKey, text string) {
	if c == nil || !key.cacheable() {
		return
	}
	cost := blobEntryCost(key, text)
	if cost > c.maxBytes {
		return
	}
	evicted := 0
	c.mu.Lock()
	if existing, ok := c.entries[key]; ok {
		c.moveToFront(existing)
		c.mu.Unlock()
		return
	}
	for c.usedBytes+cost > c.maxBytes && c.tail != nil {
		victim := c.tail
		c.unlink(victim)
		delete(c.entries, victim.key)
		c.usedBytes -= victim.bytes
		evicted++
	}
	e := &blobCacheEntry{key: key, text: text, bytes: cost}
	c.entries[key] = e
	c.pushFront(e)
	c.usedBytes += cost
	c.mu.Unlock()
	if evicted > 0 {
		c.evictions.Add(evicted)
	}
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

// blobSlicer decodes a blob once per hydration batch (avoiding []rune entirely
// for ASCII blobs) and returns independent excerpt strings so callers never pin
// an evicted cache entry's backing memory.
type blobSlicer struct {
	text  string
	ascii bool
	runes []rune
}

func newBlobSlicer(text string) blobSlicer {
	for i := 0; i < len(text); i++ {
		if text[i] >= utf8.RuneSelf {
			return blobSlicer{text: text, runes: []rune(text)}
		}
	}
	return blobSlicer{text: text, ascii: true}
}

func (b blobSlicer) slice(start, end int) (string, bool) {
	if b.ascii {
		if start < 0 || end < start || end > len(b.text) {
			return "", false
		}
		return strings.Clone(b.text[start:end]), true
	}
	if start < 0 || end < start || end > len(b.runes) {
		return "", false
	}
	return string(b.runes[start:end]), true
}

// blobReadParallelism bounds the concurrent canonical blob reads of one
// hydration batch, staying within the S3 transport's idle connection pool
// while letting a 50-hit page complete in a single round-trip wave.
const blobReadParallelism = 64

// Hydrate rechecks candidates and reads their excerpts from canonical storage
// as one batch: one lookup, one read per distinct uncached blob, then one recheck,
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
	excerpts, err := s.excerpts(ctx, scope.Organization, located)
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

// excerpts reads each distinct blob of a batch once, serving verified immutable
// blobs from BlobCache when present and reading only misses concurrently from
// canonical storage, then cuts and checksum-verifies each candidate's excerpt.
func (s Service) excerpts(ctx context.Context, org string, located map[int]Located) (map[int]string, error) {
	type request struct {
		candidate, index int
		source           LocatedSource
		ranges           []SourceRange
	}
	byBlob := map[blobCacheKey][]request{}
	seenBlob := map[string]Blob{}
	regionOnly := map[int]bool{}
	for i, l := range located {
		if len(l.Sources) == 0 && l.Blob.Key == "" && l.ManifestBlob.Key == "" && l.Segment.Derivation.Region != nil && l.Segment.Start == 0 && l.Segment.End == 0 {
			regionOnly[i] = true
			continue
		}
		if len(l.Sources) == 0 && l.ManifestBlob.Key != "" && len(l.Segment.Derivation.SourceRanges) > 0 {
			if prev, ok := seenBlob[l.ManifestBlob.Key]; ok && prev != l.ManifestBlob {
				return nil, ErrArtifactCorrupt
			}
			seenBlob[l.ManifestBlob.Key] = l.ManifestBlob
			key := blobCacheKey{organization: org, key: l.ManifestBlob.Key, sha256: l.ManifestBlob.SHA256, size: l.ManifestBlob.Size}
			byBlob[key] = append(byBlob[key], request{
				candidate: i,
				source:    LocatedSource{Blob: l.ManifestBlob},
				ranges:    l.Segment.Derivation.SourceRanges,
			})
			continue
		}
		sources := l.Sources
		if len(sources) == 0 {
			sources = []LocatedSource{{Range: SourceRange{PartKey: l.Segment.PartKey, Start: l.Segment.Start, End: l.Segment.End}, Blob: l.Blob, Pack: l.Pack}}
		}
		for k, source := range sources {
			if prev, ok := seenBlob[source.Blob.Key]; ok && prev != source.Blob {
				return nil, ErrArtifactCorrupt
			}
			seenBlob[source.Blob.Key] = source.Blob
			key := blobCacheKey{organization: org, key: source.Blob.Key, sha256: source.Blob.SHA256, size: source.Blob.Size}
			byBlob[key] = append(byBlob[key], request{candidate: i, index: k, source: source})
		}
	}
	slices := map[int][]SourceExcerpt{}
	for i, l := range located {
		if regionOnly[i] {
			continue
		}
		n := len(l.Sources)
		if n == 0 && l.ManifestBlob.Key != "" && len(l.Segment.Derivation.SourceRanges) > 0 {
			n = len(l.Segment.Derivation.SourceRanges)
		}
		if n == 0 {
			n = 1
		}
		slices[i] = make([]SourceExcerpt, n)
	}
	sliceBlob := func(blob string, requests []request) ([][]SourceExcerpt, error) {
		var manifestParts map[string]string
		var manifestSlicers map[string]*blobSlicer
		parts := map[[2]int64]*blobSlicer{}
		cuts := make([][]SourceExcerpt, len(requests))
		for idx, req := range requests {
			if len(req.ranges) > 0 {
				if manifestParts == nil {
					var manifest Manifest
					if err := json.Unmarshal([]byte(blob), &manifest); err != nil {
						return nil, ErrArtifactCorrupt
					}
					manifestParts = make(map[string]string, len(manifest.Parts))
					manifestSlicers = make(map[string]*blobSlicer, len(manifest.Parts))
					for _, part := range manifest.Parts {
						if part.Content.Kind == "text" {
							manifestParts[part.Key] = part.Content.Text
						}
					}
				}
				excerpts := make([]SourceExcerpt, len(req.ranges))
				for k, r := range req.ranges {
					slicer, ok := manifestSlicers[r.PartKey]
					if !ok {
						partText, exists := manifestParts[r.PartKey]
						if !exists || !ValidText(partText) {
							return nil, ErrArtifactCorrupt
						}
						s := newBlobSlicer(partText)
						slicer = &s
						manifestSlicers[r.PartKey] = slicer
					}
					cut, valid := slicer.slice(r.Start, r.End)
					if !valid {
						return nil, ErrArtifactCorrupt
					}
					excerpts[k] = SourceExcerpt{SourceRange: r, Text: cut}
				}
				cuts[idx] = excerpts
				continue
			}
			bounds, err := req.source.Pack.bounds(int64(len(blob)))
			if err != nil {
				return nil, err
			}
			slicer, ok := parts[bounds]
			if !ok {
				partText := blob[bounds[0]:bounds[1]]
				if !ValidText(partText) {
					return nil, ErrArtifactCorrupt
				}
				s := newBlobSlicer(partText)
				slicer = &s
				parts[bounds] = slicer
			}
			r := req.source.Range
			cut, valid := slicer.slice(r.Start, r.End)
			if !valid {
				return nil, ErrArtifactCorrupt
			}
			cuts[idx] = []SourceExcerpt{{SourceRange: r, Text: cut}}
		}
		return cuts, nil
	}
	applyCuts := func(requests []request, cuts [][]SourceExcerpt) {
		for idx, req := range requests {
			if len(req.ranges) > 0 {
				copy(slices[req.candidate], cuts[idx])
			} else {
				slices[req.candidate][req.index] = cuts[idx][0]
			}
		}
	}
	type miss struct {
		key      blobCacheKey
		requests []request
	}
	var misses []miss
	var cached map[blobCacheKey]string
	if s.BlobCache != nil {
		keys := make([]blobCacheKey, 0, len(byBlob))
		for key := range byBlob {
			keys = append(keys, key)
		}
		cached, _ = s.BlobCache.getBatch(keys)
	}
	for key, requests := range byBlob {
		if text, ok := cached[key]; ok {
			cuts, err := sliceBlob(text, requests)
			if err != nil {
				return nil, err
			}
			applyCuts(requests, cuts)
			continue
		}
		misses = append(misses, miss{key: key, requests: requests})
	}
	if len(misses) > 0 {
		var mu sync.Mutex
		var failure error
		var wg sync.WaitGroup
		slots := make(chan struct{}, blobReadParallelism)
	launch:
		for _, item := range misses {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				if failure == nil {
					failure = ctx.Err()
				}
				mu.Unlock()
				break launch
			}
			if ctx.Err() != nil {
				<-slots
				mu.Lock()
				if failure == nil {
					failure = ctx.Err()
				}
				mu.Unlock()
				break launch
			}
			wg.Add(1)
			go func(item miss) {
				defer func() { <-slots; wg.Done() }()
				// Verify the whole shared object once through Read before slicing Parts.
				data, err := s.Blobs.Read(ctx, item.requests[0].source.Blob)
				if err != nil {
					mu.Lock()
					if failure == nil {
						failure = err
					}
					mu.Unlock()
					return
				}
				text := string(data)
				cuts, err := sliceBlob(text, item.requests)
				if err != nil {
					mu.Lock()
					if failure == nil {
						failure = err
					}
					mu.Unlock()
					return
				}
				if s.BlobCache != nil && item.key.cacheable() && int64(len(data)) == item.key.size && Hash(data) == item.key.sha256 {
					s.BlobCache.put(item.key, text)
				}
				mu.Lock()
				applyCuts(item.requests, cuts)
				mu.Unlock()
			}(item)
		}
		wg.Wait()
		if failure != nil {
			return nil, failure
		}
	}
	out := map[int]string{}
	for i := range regionOnly {
		if Hash(nil) != located[i].TextSHA256 {
			return nil, fmt.Errorf("canonical excerpt mismatch: %w", ErrArtifactCorrupt)
		}
		out[i] = ""
	}
	for i, pieces := range slices {
		texts := make([]string, len(pieces))
		for k, p := range pieces {
			texts[k] = p.Text
		}
		text := strings.Join(texts, located[i].Segment.Derivation.SourceSeparator)
		if Hash([]byte(text)) != located[i].TextSHA256 {
			return nil, fmt.Errorf("canonical excerpt mismatch: %w", ErrArtifactCorrupt)
		}
		out[i] = text
		l := located[i]
		if len(l.Sources) > 0 || (l.ManifestBlob.Key != "" && len(l.Segment.Derivation.SourceRanges) > 0) {
			l.Segment.SourceExcerpts = pieces
			located[i] = l
		}
	}
	return out, nil
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
