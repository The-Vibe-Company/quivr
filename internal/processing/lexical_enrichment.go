package processing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/outbound"
)

// Defaults and bounds for opt-in async Phase-2 lexical enrichment.
const (
	DefaultMaxLexicalTextRunes      = content.MaxLexicalTextRunes
	DefaultMinCosineSalience        = 0.55
	DefaultMaxSyntheticQueries      = 8
	MaxSyntheticQueriesCap          = 64
	DefaultLexicalEnrichmentTimeout = 15 * time.Second
	MaxLexicalEnrichmentTimeout     = 60 * time.Second
	maxLexicalHTTPResponseBytes     = 4 << 20
)

// LexicalEnrichmentIndexer is an optional EnrichmentIndexer capability that
// projects Phase-2 Segment.LexicalText updates without modifying vectors.
type LexicalEnrichmentIndexer interface {
	IndexEmbeddingsWithLexical(ctx context.Context, org string, v content.Version, seg content.Segmentation, data []content.EmbeddingData, lexical []content.SegmentLexicalUpdate) error
}

// LexicalQueryEncoder encodes candidate synthetic queries into a generation's
// served vector space so cosine-salience gating can filter hallucinations.
type LexicalQueryEncoder interface {
	EncodeQuery(ctx context.Context, org, space, query string) ([]float32, error)
}

// LexicalEnrichmentOptions controls bounds and cosine-salience gating for
// opt-in Phase-2 lexical enrichment.
type LexicalEnrichmentOptions struct {
	// MaxLexicalTextRunes caps the final Segment.LexicalText length in Unicode
	// code points (0 defaults to DefaultMaxLexicalTextRunes).
	MaxLexicalTextRunes int
	// MinCosineSalience is the minimum cosine similarity between a candidate
	// synthetic query embedding and the segment embedding required to keep the
	// query (nil defaults to DefaultMinCosineSalience).
	MinCosineSalience *float64
	// MaxSyntheticQueries bounds how many cosine-gated synthetic queries are
	// appended per segment (0 defaults to DefaultMaxSyntheticQueries; < 0 is
	// unbounded up to MaxLexicalTextRunes).
	MaxSyntheticQueries int
	// QueryEncoder encodes synthetic queries that do not already carry vectors
	// into the generation's served vector space before cosine gating.
	QueryEncoder LexicalQueryEncoder
}

func (o LexicalEnrichmentOptions) maxRunes() int {
	if o.MaxLexicalTextRunes <= 0 || o.MaxLexicalTextRunes > content.MaxLexicalTextRunes {
		return DefaultMaxLexicalTextRunes
	}
	return o.MaxLexicalTextRunes
}

func (o LexicalEnrichmentOptions) minCosine() float64 {
	if o.MinCosineSalience == nil {
		return DefaultMinCosineSalience
	}
	return *o.MinCosineSalience
}

func (o LexicalEnrichmentOptions) maxQueries() int {
	if o.MaxSyntheticQueries == 0 {
		return DefaultMaxSyntheticQueries
	}
	if o.MaxSyntheticQueries < 0 {
		return 0
	}
	return o.MaxSyntheticQueries
}

// ScoredLexicalCandidate pairs a generated or expanded query/phrase (such as
// Doc2Query--) with an optional embedding in the segment's served vector space.
type ScoredLexicalCandidate struct {
	Text   string
	Vector []float32
}

// LexicalEnrichmentRequest passes a Version's already-derived segments and
// dense embeddings to a Phase-2 LexicalEnricher. All fields are read-only.
type LexicalEnrichmentRequest struct {
	Organization string
	CorpusID     string
	Version      content.Version
	Generation   content.Generation
	Segmentation content.Segmentation
	Embeddings   []content.EmbeddingData
}

// SegmentLexicalExpansion holds lexical additions produced for one segment.
type SegmentLexicalExpansion struct {
	// Terms are pre-approved expansion terms (such as configured domain
	// synonyms) appended directly without query-vector gating.
	Terms []string
	// Queries are synthetic queries (such as Doc2Query--) gated by cosine
	// similarity against the segment's served dense embedding.
	Queries []ScoredLexicalCandidate
}

// LexicalEnricher produces opt-in Phase-2 lexical expansions for a Version's
// segments during Stage-2 enrichment. Implementations must be safe for
// concurrent use and deterministic/idempotent across retries.
type LexicalEnricher interface {
	EnrichLexical(ctx context.Context, req LexicalEnrichmentRequest) (map[string]SegmentLexicalExpansion, error)
}

// CosineSimilarity computes the cosine similarity between two equal-length
// finite vectors in [-1, 1]. It returns (0, false) for empty vectors, length
// mismatches, zero-norm vectors, or non-finite components.
func CosineSimilarity(a, b []float32) (float64, bool) {
	if len(a) == 0 || len(a) != len(b) {
		return 0, false
	}
	var dot, normA, normB float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		if math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) {
			return 0, false
		}
		dot += x * y
		normA += x * x
		normB += y * y
	}
	if normA <= 0 || normB <= 0 {
		return 0, false
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom <= 0 || math.IsNaN(denom) || math.IsInf(denom, 0) {
		return 0, false
	}
	cos := dot / denom
	if cos > 1 {
		cos = 1
	} else if cos < -1 {
		cos = -1
	}
	return cos, true
}

// GateByCosineSalience keeps only candidate texts whose embedding cosine
// similarity to segmentVector is at least minCosine, ordered by highest cosine
// similarity first (tie-breaking by input order). Candidates with empty text or
// missing/invalid vectors are rejected fail-closed so off-topic hallucinations
// cannot pollute Segment.LexicalText. If maxKeep > 0, at most maxKeep texts are
// returned.
func GateByCosineSalience(segmentVector []float32, candidates []ScoredLexicalCandidate, minCosine float64, maxKeep int) []string {
	if len(segmentVector) == 0 || len(candidates) == 0 || math.IsNaN(minCosine) || math.IsInf(minCosine, 0) {
		return nil
	}
	type kept struct {
		text  string
		score float64
		order int
	}
	survivors := make([]kept, 0, len(candidates))
	for i, c := range candidates {
		text := strings.TrimSpace(c.Text)
		if text == "" {
			continue
		}
		score, ok := CosineSimilarity(segmentVector, c.Vector)
		if !ok || score < minCosine {
			continue
		}
		survivors = append(survivors, kept{text: text, score: score, order: i})
	}
	if len(survivors) == 0 {
		return nil
	}
	sort.SliceStable(survivors, func(i, j int) bool {
		if survivors[i].score != survivors[j].score {
			return survivors[i].score > survivors[j].score
		}
		return survivors[i].order < survivors[j].order
	})
	seen := make(map[string]struct{}, len(survivors))
	out := make([]string, 0, len(survivors))
	for _, s := range survivors {
		key := strings.ToLower(strings.Join(strings.Fields(s.text), " "))
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, s.text)
		if maxKeep > 0 && len(out) >= maxKeep {
			break
		}
	}
	return out
}

type synonymRule struct {
	key    string
	tokens []string
	values []string
}

// SynonymLexicalEnricher expands segments using a generic, operator-configured
// synonym map matched on whole-token boundaries.
type SynonymLexicalEnricher struct {
	rules []synonymRule
}

// NewSynonymLexicalEnricher validates and compiles a synonym dictionary into a
// deterministic, concurrency-safe LexicalEnricher.
func NewSynonymLexicalEnricher(synonyms map[string][]string) (*SynonymLexicalEnricher, error) {
	if len(synonyms) == 0 {
		return nil, errors.New("synonyms map must not be empty")
	}
	rules := make([]synonymRule, 0, len(synonyms))
	seenKeys := make(map[string]string, len(synonyms))
	for rawKey, rawValues := range synonyms {
		tokens := content.LexicalTokens(rawKey)
		if len(tokens) == 0 {
			return nil, fmt.Errorf("synonym key %q must contain at least one letter or digit", rawKey)
		}
		normKey := strings.Join(tokens, " ")
		if prev, dup := seenKeys[normKey]; dup {
			return nil, fmt.Errorf("synonym keys %q and %q normalize to the same token sequence %q", prev, rawKey, normKey)
		}
		seenKeys[normKey] = rawKey
		seen := map[string]struct{}{}
		values := make([]string, 0, len(rawValues))
		for _, v := range rawValues {
			cleaned := strings.Join(strings.Fields(v), " ")
			if cleaned == "" || !content.ValidLexicalText(cleaned, content.MaxLexicalTextRunes) {
				return nil, fmt.Errorf("synonym value for %q must be non-empty valid UTF-8 without NUL", rawKey)
			}
			norm := strings.ToLower(cleaned)
			if _, dup := seen[norm]; dup {
				continue
			}
			seen[norm] = struct{}{}
			values = append(values, cleaned)
		}
		if len(values) == 0 {
			return nil, fmt.Errorf("synonym key %q must map to at least one expansion term", rawKey)
		}
		rules = append(rules, synonymRule{key: normKey, tokens: tokens, values: values})
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].key < rules[j].key })
	return &SynonymLexicalEnricher{rules: rules}, nil
}

// EnrichLexical matches configured synonym keys against each segment's
// canonical source text on whole-token boundaries. Matching only immutable
// Segment.Text guarantees retries never cascade on previously appended terms.
func (e *SynonymLexicalEnricher) EnrichLexical(ctx context.Context, req LexicalEnrichmentRequest) (map[string]SegmentLexicalExpansion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := map[string]SegmentLexicalExpansion{}
	for _, p := range req.Segmentation.Segments {
		tokens := content.LexicalTokens(p.Text)
		if len(tokens) == 0 {
			continue
		}
		var terms []string
		for _, rule := range e.rules {
			if content.ContainsTokenSequence(tokens, rule.tokens) {
				terms = append(terms, rule.values...)
			}
		}
		if len(terms) > 0 {
			out[p.ID] = SegmentLexicalExpansion{Terms: terms}
		}
	}
	return out, nil
}

// ChainLexicalEnricher runs multiple LexicalEnrichers in order and merges their
// per-segment terms and candidate queries.
type ChainLexicalEnricher struct {
	Enrichers []LexicalEnricher
}

func (c ChainLexicalEnricher) EnrichLexical(ctx context.Context, req LexicalEnrichmentRequest) (map[string]SegmentLexicalExpansion, error) {
	merged := map[string]SegmentLexicalExpansion{}
	for _, enricher := range c.Enrichers {
		if enricher == nil {
			continue
		}
		part, err := enricher.EnrichLexical(ctx, req)
		if err != nil {
			return nil, err
		}
		for segID, exp := range part {
			cur := merged[segID]
			cur.Terms = append(cur.Terms, exp.Terms...)
			cur.Queries = append(cur.Queries, exp.Queries...)
			merged[segID] = cur
		}
	}
	return merged, nil
}

// HTTPLexicalEnricherConfig configures a generic HTTP lexical expansion /
// synthetic query (e.g. Doc2Query--) sidecar client.
type HTTPLexicalEnricherConfig struct {
	Endpoint   string
	APIKey     string
	Timeout    time.Duration
	MaxQueries int
	Client     *http.Client
}

// HTTPLexicalEnricher calls an external HTTP endpoint during Stage-2 enrichment
// to obtain expansion terms and/or synthetic queries per segment.
type HTTPLexicalEnricher struct {
	endpoint   string
	apiKey     string
	maxQueries int
	client     *http.Client
}

func isLoopbackHostname(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// NewHTTPLexicalEnricher validates configuration and builds an HTTPLexicalEnricher.
func NewHTTPLexicalEnricher(cfg HTTPLexicalEnricherConfig) (*HTTPLexicalEnricher, error) {
	u, err := url.Parse(strings.TrimSpace(cfg.Endpoint))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("lexical enrichment endpoint must be a valid http or https URL without userinfo or fragment")
	}
	if cfg.APIKey != "" && u.Scheme != "https" && !isLoopbackHostname(u.Hostname()) {
		return nil, errors.New("lexical enrichment endpoint with an API key must use https or a loopback host")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultLexicalEnrichmentTimeout
	}
	if timeout > MaxLexicalEnrichmentTimeout {
		return nil, fmt.Errorf("lexical enrichment timeout must not exceed %s", MaxLexicalEnrichmentTimeout)
	}
	maxQueries := cfg.MaxQueries
	if maxQueries <= 0 {
		maxQueries = DefaultMaxSyntheticQueries
	}
	var client *http.Client
	if cfg.Client == nil {
		client = &http.Client{
			Timeout:       timeout,
			Transport:     outbound.DefaultTransport(),
			CheckRedirect: outbound.CheckRedirect,
		}
	} else {
		copied := *cfg.Client
		if copied.Timeout <= 0 || copied.Timeout > timeout {
			copied.Timeout = timeout
		}
		client = &copied
	}
	return &HTTPLexicalEnricher{
		endpoint:   u.String(),
		apiKey:     cfg.APIKey,
		maxQueries: maxQueries,
		client:     client,
	}, nil
}

type lexicalHTTPRequest struct {
	OrganizationID string                   `json:"organization_id"`
	CorpusID       string                   `json:"corpus_id"`
	RecordID       string                   `json:"record_id"`
	VersionID      string                   `json:"version_id"`
	MaxQueries     int                      `json:"max_queries,omitempty"`
	Segments       []lexicalHTTPSegmentItem `json:"segments"`
}

type lexicalHTTPSegmentItem struct {
	SegmentID   string `json:"segment_id"`
	PartKey     string `json:"part_key"`
	Title       string `json:"title,omitempty"`
	Text        string `json:"text"`
	LexicalText string `json:"lexical_text,omitempty"`
}

type lexicalHTTPQuery struct {
	Text   string    `json:"text"`
	Vector []float32 `json:"vector,omitempty"`
}

// UnmarshalJSON accepts either a plain query string ("how does x work?") or an
// object ({"text": "how does x work?", "vector": [...]}).
func (q *lexicalHTTPQuery) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		q.Text = text
		q.Vector = nil
		return nil
	}
	type alias lexicalHTTPQuery
	var obj alias
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	*q = lexicalHTTPQuery(obj)
	return nil
}

type lexicalHTTPResponse struct {
	Segments []struct {
		SegmentID string             `json:"segment_id"`
		Terms     []string           `json:"terms,omitempty"`
		Queries   []lexicalHTTPQuery `json:"queries,omitempty"`
	} `json:"segments"`
}

// EnrichLexical posts the Version's segments to the configured HTTP endpoint
// and returns per-segment terms and candidate synthetic queries.
func (e *HTTPLexicalEnricher) EnrichLexical(ctx context.Context, req LexicalEnrichmentRequest) (map[string]SegmentLexicalExpansion, error) {
	if len(req.Segmentation.Segments) == 0 {
		return nil, nil
	}
	wire := lexicalHTTPRequest{
		OrganizationID: req.Organization,
		CorpusID:       req.CorpusID,
		RecordID:       req.Version.RecordID,
		VersionID:      req.Version.ID,
		MaxQueries:     e.maxQueries,
		Segments:       make([]lexicalHTTPSegmentItem, len(req.Segmentation.Segments)),
	}
	known := make(map[string]struct{}, len(req.Segmentation.Segments))
	for i, p := range req.Segmentation.Segments {
		known[p.ID] = struct{}{}
		wire.Segments[i] = lexicalHTTPSegmentItem{
			SegmentID:   p.ID,
			PartKey:     p.PartKey,
			Title:       p.Title,
			Text:        p.Text,
			LexicalText: p.Derivation.LexicalText,
		}
	}
	payload, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("lexical enrichment request invalid")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+e.apiKey)
	}
	res, err := e.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("lexical enrichment endpoint unavailable: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("lexical enrichment endpoint failed (HTTP %d)", res.StatusCode)
	}
	limited := io.LimitReader(res.Body, maxLexicalHTTPResponseBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("lexical enrichment response read failed: %w", err)
	}
	if len(raw) > maxLexicalHTTPResponseBytes {
		return nil, errors.New("lexical enrichment response exceeds byte limit")
	}
	var decoded lexicalHTTPResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, errors.New("lexical enrichment response invalid JSON")
	}
	out := make(map[string]SegmentLexicalExpansion, len(decoded.Segments))
	seenBySeg := make(map[string]map[string]struct{}, len(decoded.Segments))
	for _, seg := range decoded.Segments {
		if _, ok := known[seg.SegmentID]; !ok {
			continue
		}
		seen := seenBySeg[seg.SegmentID]
		if seen == nil {
			seen = map[string]struct{}{}
			seenBySeg[seg.SegmentID] = seen
		}
		exp := out[seg.SegmentID]
		addCandidate := func(raw string) {
			cleaned := strings.Join(strings.Fields(raw), " ")
			if cleaned == "" {
				return
			}
			key := strings.ToLower(cleaned)
			if _, dup := seen[key]; dup {
				return
			}
			seen[key] = struct{}{}
			exp.Queries = append(exp.Queries, ScoredLexicalCandidate{Text: cleaned})
		}
		// All remote HTTP output (terms and synthetic queries) passes through
		// cosine-salience gating using engine-encoded served-space vectors so
		// an untrusted sidecar cannot bypass hallucination filtering.
		for _, term := range seg.Terms {
			addCandidate(term)
		}
		for _, q := range seg.Queries {
			addCandidate(q.Text)
		}
		if len(exp.Queries) > 0 {
			out[seg.SegmentID] = exp
		}
	}
	return out, nil
}

// applyLexicalEnrichment runs the opt-in Phase-2 lexical enricher, gates
// synthetic queries by cosine similarity against each segment's served dense
// embedding, merges additions into Segment.LexicalText, persists canonical
// changes to Postgres, and returns the desired Weaviate lexical updates so
// retries always converge.
func (s Service) applyLexicalEnrichment(
	ctx context.Context,
	org, corpusID string,
	v content.Version,
	g content.Generation,
	seg content.Segmentation,
	data []content.EmbeddingData,
) (content.Segmentation, []content.SegmentLexicalUpdate, error) {
	if s.LexicalEnricher == nil || len(seg.Segments) == 0 {
		return seg, nil, nil
	}
	if s.Content.Embeddings != nil {
		eligible, err := s.Content.EnrichmentEligible(ctx, org, v.ID)
		if err != nil {
			return seg, nil, err
		}
		if !eligible {
			return seg, nil, nil
		}
	}
	expansions, err := s.LexicalEnricher.EnrichLexical(ctx, LexicalEnrichmentRequest{
		Organization: org,
		CorpusID:     corpusID,
		Version:      v,
		Generation:   g,
		Segmentation: seg,
		Embeddings:   data,
	})
	if err != nil {
		return seg, nil, err
	}
	if len(expansions) == 0 {
		return seg, nil, nil
	}

	servedSpace := g.ServedFor(content.PluginOfRecipe(seg.Recipe))
	if servedSpace == "" {
		servedSpace = g.SpaceID
	}
	segVectors := make(map[string][]float32, len(seg.Segments))
	for _, d := range data {
		if d.Artifact.SpaceID == servedSpace && len(d.Vector) > 0 {
			segVectors[d.Artifact.SegmentID] = d.Vector
		}
	}

	opts := s.LexicalOptions
	maxRunes := opts.maxRunes()
	minCosine := opts.minCosine()
	maxQueries := opts.maxQueries()
	encodedQueries := map[string][]float32{}

	var postgresUpdates []content.SegmentLexicalUpdate
	var projectionUpdates []content.SegmentLexicalUpdate
	for _, p := range seg.Segments {
		exp, ok := expansions[p.ID]
		if !ok {
			continue
		}
		additions := make([]string, 0, len(exp.Terms)+len(exp.Queries))
		additions = append(additions, exp.Terms...)
		if len(exp.Queries) > 0 {
			segVec := segVectors[p.ID]
			if len(segVec) > 0 {
				scored := make([]ScoredLexicalCandidate, 0, len(exp.Queries))
				for _, q := range exp.Queries {
					qText := strings.TrimSpace(q.Text)
					if qText == "" {
						continue
					}
					vec := q.Vector
					if len(vec) == 0 && opts.QueryEncoder != nil && servedSpace != "" {
						if cached, hit := encodedQueries[qText]; hit {
							vec = cached
						} else {
							encoded, encErr := opts.QueryEncoder.EncodeQuery(ctx, org, servedSpace, qText)
							if encErr != nil {
								if errors.Is(encErr, content.ErrInvalid) {
									encodedQueries[qText] = nil
									continue
								}
								return seg, nil, encErr
							}
							encodedQueries[qText] = encoded
							vec = encoded
						}
					}
					if len(vec) > 0 {
						scored = append(scored, ScoredLexicalCandidate{Text: qText, Vector: vec})
					}
				}
				additions = append(additions, GateByCosineSalience(segVec, scored, minCosine, maxQueries)...)
			}
		}
		if len(additions) == 0 {
			continue
		}
		base := p.Derivation.LexicalText
		seededFromText := false
		if base == "" && p.Text != "" {
			base = p.Text
			seededFromText = true
		}
		merged := content.MergeLexicalText(base, additions, maxRunes)
		if merged == "" {
			continue
		}
		if seededFromText && merged == content.MergeLexicalText(base, nil, maxRunes) {
			// Nothing new was added beyond the canonical segment text; avoid
			// persisting a redundant or truncated copy into Derivation.LexicalText.
			continue
		}
		update := content.SegmentLexicalUpdate{SegmentID: p.ID, LexicalText: merged}
		// Always pass the converged target to the projection so retries after
		// a Postgres-succeeded / Weaviate-failed crash still update Weaviate.
		projectionUpdates = append(projectionUpdates, update)
		if merged != p.Derivation.LexicalText {
			postgresUpdates = append(postgresUpdates, update)
		}
	}
	if len(postgresUpdates) > 0 {
		updated, err := s.Content.UpdateSegmentLexicalText(ctx, org, seg, postgresUpdates)
		if err != nil {
			return seg, nil, err
		}
		seg = updated
	}
	return seg, projectionUpdates, nil
}
