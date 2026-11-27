package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

// Wide-funnel candidate generation.
//
// The plugin contract caps every candidate request at k <= 100, so a wider
// candidate funnel is built from several *diverse* requests instead of one deep
// request: a record-grouped dense recall leg (100 distinct records instead of
// 100 passages), a boilerplate-stripped dense view, a boilerplate-stripped
// lexical view, and an optional zero-LLM Round-2 consensus pseudo-relevance
// feedback (PRF) pass over dense and/or lexical. Each extra leg is fused as a
// separate calibrated convex component so that the primary legs keep their
// scale, and legs that a record does not appear in contribute zero.

// Leg names used for request bookkeeping and diagnostics.
const (
	legDense       = "dense"
	legDenseGroup  = "dense_grouped"
	legDenseAux    = "dense_ungrouped_aux"
	legLexical     = "lex"
	legEntity      = "entity"
	legDenseView   = "dense_view"
	legDenseRecall = "dense_recall"
	legLexView     = "lex_view"
	legR2Dense     = "r2_dense"
	legR2Lex       = "r2_lex"
)

// namedRequest pairs a Round-1 candidate request with the leg it implements so
// served candidates can be attributed by request_index on later rounds.
type namedRequest struct {
	Name string
	Req  quivrplugin.CandidateRequest
}

// extraLeg is an additional calibrated fusion component.
type extraLeg struct {
	Name      string
	Weight    float64
	NormDepth int
	Cands     []quivrplugin.Candidate
}

// legHit is one record's best evidence within one extra leg.
type legHit struct {
	Score float64
	Rank  int
}

// denseViewQuery returns the boilerplate-stripped dense query view, if it
// differs from the full query.
func denseViewQuery(query string, cfg configuration) (string, bool) {
	mode := cfg.DenseViewMode
	if mode == "" || mode == "none" {
		return "", false
	}
	return extractCoreQuery(query, mode)
}

// lexicalViewQuery returns the cleaned BM25 text for the stripped query view,
// if it differs from the primary cleaned lexical query.
func lexicalViewQuery(query string, cfg configuration) (string, bool) {
	mode := cfg.LexicalViewMode
	if mode == "" || mode == "none" {
		return "", false
	}
	core, ok := extractCoreQuery(query, mode)
	if !ok {
		return "", false
	}
	view := strings.TrimSpace(cleanLexicalQuery(core))
	primary := strings.TrimSpace(query)
	if cfg.CleanLexicalQuery {
		primary = strings.TrimSpace(cleanLexicalQuery(query))
	}
	if view == "" || strings.EqualFold(view, primary) {
		return "", false
	}
	return view, true
}

// dedupByRecord returns a record-level ranking (best passage per record) from
// an ungrouped passage list, preserving descending score order.
func dedupByRecord(cands []quivrplugin.Candidate) []quivrplugin.Candidate {
	seen := make(map[string]bool, len(cands))
	out := make([]quivrplugin.Candidate, 0, len(cands))
	sorted := make([]quivrplugin.Candidate, len(cands))
	copy(sorted, cands)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Score > sorted[j].Score })
	for _, c := range sorted {
		k := recordKey(c)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, c)
	}
	return out
}

// statsAtDepth computes leg statistics over the first depth candidates only, so
// a deeper admission list (e.g. k=100) can widen the pool without loosening the
// min-max floor that calibrates the head of the list. depth <= 0 uses all.
func statsAtDepth(cands []quivrplugin.Candidate, depth int) legStats {
	if depth > 0 && depth < len(cands) {
		return computeLegStats(cands[:depth])
	}
	return computeLegStats(cands)
}

// buildRound2Requests builds the optional zero-LLM Round-2 consensus PRF
// requests from Round-1 evidence.
func buildRound2Requests(req *quivrplugin.SearchRequest, cfg configuration, denseRecords, lexical []quivrplugin.Candidate) ([]quivrplugin.CandidateRequest, []string) {
	if cfg.Round2ConsensusWeight <= 0 && cfg.Round2LexicalWeight <= 0 {
		return nil, nil
	}
	r2Q, terms, ok := buildRound2ConsensusQuery(req.Query.Text, cfg, denseRecords, lexical)
	if !ok || len(terms) == 0 {
		return nil, nil
	}
	var reqs []quivrplugin.CandidateRequest
	if cfg.Round2ConsensusWeight > 0 && req.Query.Mode != "lexical" {
		if space := req.ServedSpace(); space != nil {
			reqs = append(reqs, quivrplugin.CandidateRequest{
				Primitive: quivrplugin.PrimitiveNearVector,
				QueryText: r2Q,
				Space:     space.ID,
				K:         clampK(cfg.Round2DenseK, req.Limit),
				GroupBy:   "record",
			})
		}
	}
	if cfg.Round2LexicalWeight > 0 && req.Query.Mode != "semantic" {
		base := req.Query.Text
		if cfg.CleanLexicalQuery {
			base = cleanLexicalQuery(base)
		}
		reqs = append(reqs, quivrplugin.CandidateRequest{
			Primitive: quivrplugin.PrimitiveBM25,
			QueryText: strings.TrimSpace(base + " " + strings.Join(terms, " ")),
			Field:     quivrplugin.FieldSource,
			K:         clampK(cfg.Round2LexicalK, req.Limit),
			GroupBy:   "record",
		})
	}
	return reqs, terms
}

// poolEntry is one record of a [newsroom-pool] diagnostic dump.
type poolEntry struct {
	R  string         `json:"r"`
	F  float64        `json:"f"`
	S1 float64        `json:"s1"`
	L  map[string]int `json:"l,omitempty"`
}

// dumpPool writes the fused candidate pool (final order) with per-leg ranks to
// stderr for offline pool-recall analysis. Diagnostics only.
func dumpPool(query string, round int, bundles []*recordBundle) {
	entries := make([]poolEntry, len(bundles))
	for i, b := range bundles {
		l := map[string]int{}
		if b.HasDense && b.DenseRank > 0 {
			l[legDense] = b.DenseRank
		}
		if b.HasLexical {
			l[legLexical] = b.LexicalRank
		}
		if b.HasEntity {
			l[legEntity] = b.EntityRank
		}
		for name, h := range b.Extra {
			l[name] = h.Rank
		}
		entries[i] = poolEntry{R: b.RecordID, F: b.FinalScore, S1: b.Stage1Score, L: l}
	}
	raw, err := json.Marshal(struct {
		Q     string      `json:"q"`
		Round int         `json:"round"`
		Pool  []poolEntry `json:"pool"`
	}{query, round, entries})
	if err != nil {
		return
	}
	fmt.Fprintf(os.Stderr, "[newsroom-pool] %s\n", raw)
}

// applyTailFusion keeps the precision-tuned head (positions 1..start) and
// re-orders positions start+1.. by weighted Reciprocal Rank Fusion over every
// leg's record-level rank plus the convex position. nDCG@10 depends only on the
// head, while rank-based fusion stops single-leg relevant records (strong in one
// leg, absent from another) from being pushed below the Recall@50 cut by
// min-max convex scaling. The tail's own sorted score multiset is reassigned in
// the new order, so scores stay strictly monotonic and their range is unchanged.
func applyTailFusion(bundles []*recordBundle, cfg configuration) {
	start := cfg.TailStart
	if cfg.TailFusion != "rrf" || start < 0 || start >= len(bundles)-1 {
		return
	}
	k := cfg.TailRRFK
	if k <= 0 {
		k = 60
	}
	tail := bundles[start:]
	scores := make([]float64, len(tail))
	for i, b := range tail {
		scores[i] = b.FinalScore
	}
	rrf := make(map[*recordBundle]float64, len(tail))
	for i, b := range tail {
		v := cfg.TailConvexWeight / (k + float64(start+i+1))
		if b.HasDense && b.DenseRank > 0 {
			v += 1.0 / (k + float64(b.DenseRank))
		}
		if b.HasLexical && b.LexicalRank > 0 {
			v += 1.0 / (k + float64(b.LexicalRank))
		}
		if b.HasEntity && b.EntityRank > 0 {
			v += 1.0 / (k + float64(b.EntityRank))
		}
		for _, h := range b.Extra {
			v += cfg.TailExtraWeight / (k + float64(h.Rank))
		}
		rrf[b] = v
	}
	sort.SliceStable(tail, func(i, j int) bool {
		if rrf[tail[i]] != rrf[tail[j]] {
			return rrf[tail[i]] > rrf[tail[j]]
		}
		return tail[i].RecordID < tail[j].RecordID
	})
	for i, b := range tail {
		b.FinalScore = scores[i]
	}
}
