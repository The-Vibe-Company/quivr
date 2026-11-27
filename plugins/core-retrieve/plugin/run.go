// Command quivr-core-retrieve is the first-party retrieval plugin
// (core.retrieve): the search the engine ran itself before THE-779. It asks
// the engine for one candidate list that follows the search mode, and returns
// that list as the ranking; see README.md.
package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"sort"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

// alpha weights the vector side of a hybrid search; relative score fusion
// normalizes each side's scores before weighting them.
const alpha = 0.5

const (
	fusionTwoLegMinMax = "two_leg_minmax"
	fusionTwoLegDBSF   = "two_leg_dbsf"
	fusionTwoLegRRF    = "two_leg_rrf"
	rrfConstant        = 60.0
)

func isTwoLegFusion(fusion string) bool {
	return fusion == fusionTwoLegMinMax || fusion == fusionTwoLegDBSF || fusion == fusionTwoLegRRF
}

// configuration is validated by the manifest schema at install and by the
// SDK on each invocation. Defaults are applied here, not by JSON Schema.
type configuration struct {
	DenseWeight    float64 `json:"dense_weight"`
	CandidateCount int     `json:"candidate_count"`
	HybridFusion   string  `json:"hybrid_fusion"`
}

func parseConfig(req *quivrplugin.SearchRequest) (configuration, error) {
	config := configuration{DenseWeight: alpha, CandidateCount: req.Limit, HybridFusion: "relative_score"}
	if len(req.Configuration) > 0 {
		if err := json.Unmarshal(req.Configuration, &config); err != nil {
			return configuration{}, quivrplugin.TerminalSearchError("invalid_configuration", err.Error())
		}
	}
	return config, nil
}

type retriever struct{}

func (retriever) Search(_ context.Context, req *quivrplugin.SearchRequest) (*quivrplugin.SearchAnswer, error) {
	config, err := parseConfig(req)
	if err != nil {
		return nil, err
	}
	pending := []quivrplugin.SearchSpace{}
	if req.Query.Mode != "lexical" {
		requested := map[string]bool{}
		for _, served := range req.Served {
			if served.Request.Space != "" {
				requested[served.Request.Space] = true
			}
		}
		for _, space := range req.Spaces {
			if space.Role == "served" && !requested[space.ID] {
				pending = append(pending, space)
			}
		}
	}
	if req.Round == 1 || (req.Round < 3 && len(pending) > 0) {
		if req.Query.Mode == "hybrid" && isTwoLegFusion(config.HybridFusion) {
			return askTwoLeg(req, config, pending), nil
		}
		c, err := request(req, config)
		if err != nil {
			return nil, err
		}
		requests := []quivrplugin.CandidateRequest{c}
		if c.Primitive != quivrplugin.PrimitiveBM25 {
			requests = nil
			for _, space := range pending[:min(8, len(pending))] {
				next := c
				next.Space = space.ID
				// Pure image cross-modal spaces (indexes includes "image" without "text"
				// or legacy "hybrid") carry no lexical text in that space; query them by
				// near_vector while preserving hybrid fusion for text and legacy spaces.
				if next.Primitive == quivrplugin.PrimitiveHybrid && isImageOnlySpace(space.Indexes) {
					next.Primitive = quivrplugin.PrimitiveNearVector
					next.Field = ""
					next.Alpha = nil
					next.Fusion = ""
				}
				requests = append(requests, next)
			}
		}
		return quivrplugin.Ask(requests...), nil
	}
	if req.Query.Mode == "hybrid" && isTwoLegFusion(config.HybridFusion) {
		return rankTwoLeg(req, config), nil
	}
	// Served candidates rank by score, then segment id; the Record dedupe below keeps the first in that order.
	hits := []quivrplugin.RankedHit{}
	segmentRecords := map[string]string{}
	for _, s := range req.Served {
		explanation := explain(s.Request)
		for _, c := range s.Candidates {
			segmentRecords[c.SegmentID] = c.RecordID
			hits = append(hits, quivrplugin.RankedHit{SegmentID: c.SegmentID, Score: c.Score, Explanation: explanation})
		}
	}
	// The index returns candidates of equal score in the order their objects
	// were written, which enrichment and rebuilds change: equal scores rank by
	// segment id, so the same search over the same Records ranks the same way.
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].SegmentID < hits[j].SegmentID
	})
	unique := hits[:0]
	seen := map[string]bool{}
	for _, h := range hits {
		key := segmentRecords[h.SegmentID]
		if key == "" {
			key = h.SegmentID
		}
		if !seen[key] {
			seen[key] = true
			unique = append(unique, h)
		}
	}
	return quivrplugin.Rank(unique[:min(len(unique), req.Limit)]...), nil
}

func askTwoLeg(req *quivrplugin.SearchRequest, config configuration, pending []quivrplugin.SearchSpace) *quivrplugin.SearchAnswer {
	requests := make([]quivrplugin.CandidateRequest, 0, min(8, len(pending)+1))
	if req.Round == 1 {
		requests = append(requests, quivrplugin.CandidateRequest{
			Primitive: quivrplugin.PrimitiveBM25,
			Field:     quivrplugin.FieldSource,
			QueryText: req.Query.Text,
			K:         config.CandidateCount,
			GroupBy:   "record",
		})
	}
	maxSpaces := 8 - len(requests)
	for _, space := range pending[:min(maxSpaces, len(pending))] {
		requests = append(requests, quivrplugin.CandidateRequest{
			Primitive: quivrplugin.PrimitiveNearVector,
			Space:     space.ID,
			QueryText: req.Query.Text,
			K:         config.CandidateCount,
			GroupBy:   "record",
		})
	}
	return quivrplugin.Ask(requests...)
}

func rankTwoLeg(req *quivrplugin.SearchRequest, config configuration) *quivrplugin.SearchAnswer {
	lexByRecord := map[string]quivrplugin.Candidate{}
	denseByRecord := map[string]quivrplugin.Candidate{}
	for _, s := range req.Served {
		target := denseByRecord
		if s.Request.Primitive == quivrplugin.PrimitiveBM25 {
			target = lexByRecord
		}
		for _, c := range s.Candidates {
			key := c.RecordID
			if key == "" {
				key = c.SegmentID
			}
			prev, ok := target[key]
			if !ok || c.Score > prev.Score || (c.Score == prev.Score && c.SegmentID < prev.SegmentID) {
				target[key] = c
			}
		}
	}
	normLex := normalizeCandidates(lexByRecord, config.HybridFusion)
	normDense := normalizeCandidates(denseByRecord, config.HybridFusion)
	wDense := config.DenseWeight
	wLex := 1 - wDense
	explanation := fmt.Sprintf("two-leg keywords and vectors, alpha %g, %s", wDense, config.HybridFusion)
	records := map[string]bool{}
	for key := range lexByRecord {
		records[key] = true
	}
	for key := range denseByRecord {
		records[key] = true
	}
	hits := make([]quivrplugin.RankedHit, 0, len(records))
	for key := range records {
		score := wLex*normLex[key] + wDense*normDense[key]
		// Prefer the dense passage representative when available, matching engine fusion.
		cand, ok := denseByRecord[key]
		if !ok {
			cand = lexByRecord[key]
		}
		hits = append(hits, quivrplugin.RankedHit{
			SegmentID:   cand.SegmentID,
			Score:       score,
			Explanation: explanation,
		})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].SegmentID < hits[j].SegmentID
	})
	return quivrplugin.Rank(hits[:min(len(hits), req.Limit)]...)
}

func normalizeCandidates(pool map[string]quivrplugin.Candidate, fusion string) map[string]float64 {
	out := make(map[string]float64, len(pool))
	if len(pool) == 0 {
		return out
	}
	type entry struct {
		key  string
		cand quivrplugin.Candidate
	}
	rows := make([]entry, 0, len(pool))
	for k, c := range pool {
		rows = append(rows, entry{key: k, cand: c})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].cand.Score != rows[j].cand.Score {
			return rows[i].cand.Score > rows[j].cand.Score
		}
		return rows[i].cand.SegmentID < rows[j].cand.SegmentID
	})
	switch fusion {
	case fusionTwoLegRRF:
		for i, r := range rows {
			out[r.key] = 1.0 / (rrfConstant + float64(i+1))
		}
	case fusionTwoLegDBSF:
		if len(rows) == 1 {
			out[rows[0].key] = 0.5
			return out
		}
		sum := 0.0
		for _, r := range rows {
			sum += r.cand.Score
		}
		mu := sum / float64(len(rows))
		variance := 0.0
		for _, r := range rows {
			d := r.cand.Score - mu
			variance += d * d
		}
		sigma := math.Sqrt(variance / float64(len(rows)))
		if sigma == 0 {
			for _, r := range rows {
				out[r.key] = 0.5
			}
			return out
		}
		for _, r := range rows {
			out[r.key] = 0.5 + (r.cand.Score-mu)/(6*sigma)
		}
	default: // fusionTwoLegMinMax
		lo, hi := rows[len(rows)-1].cand.Score, rows[0].cand.Score
		for _, r := range rows {
			if hi == lo {
				out[r.key] = 1.0
			} else {
				out[r.key] = (r.cand.Score - lo) / (hi - lo)
			}
		}
	}
	return out
}

// request is the one candidate request of a search: keywords on the title
// and body for lexical, the served space for semantic, both for hybrid.
func request(req *quivrplugin.SearchRequest, config configuration) (quivrplugin.CandidateRequest, error) {
	c := quivrplugin.CandidateRequest{QueryText: req.Query.Text, K: config.CandidateCount, GroupBy: "record"}
	if req.Query.Mode == "lexical" {
		c.Primitive, c.Field = quivrplugin.PrimitiveBM25, quivrplugin.FieldSource
		return c, nil
	}
	space := req.ServedSpace()
	if space == nil {
		if req.Query.Mode == "hybrid" {
			c.Primitive, c.Field = quivrplugin.PrimitiveBM25, quivrplugin.FieldSource
			return c, nil
		}
		return c, quivrplugin.TerminalSearchError("no_served_space", "the searched Corpora serve no vector space; search by keywords, or rebuild them")
	}
	c.Space = space.ID
	if req.Query.Mode == "semantic" {
		c.Primitive = quivrplugin.PrimitiveNearVector
		return c, nil
	}
	c.Primitive, c.Field, c.Alpha, c.Fusion = quivrplugin.PrimitiveHybrid, quivrplugin.FieldSource, &config.DenseWeight, config.HybridFusion
	return c, nil
}

func explain(c quivrplugin.CandidateRequest) string {
	switch c.Primitive {
	case quivrplugin.PrimitiveBM25:
		return "keywords"
	case quivrplugin.PrimitiveNearVector:
		return "vectors in " + c.Space
	}
	weight := alpha
	if c.Alpha != nil {
		weight = *c.Alpha
	}
	fusion := "relative score fusion"
	if c.Fusion == "ranked" {
		fusion = "ranked fusion (RRF)"
	}
	return fmt.Sprintf("keywords and vectors in %s, alpha %g, %s", c.Space, weight, fusion)
}

func isImageOnlySpace(indexes []string) bool {
	return slices.Contains(indexes, "image") && !slices.Contains(indexes, "text") && !slices.Contains(indexes, "hybrid")
}

func Main() {
	plugin, err := quivrplugin.New("")
	if err == nil {
		err = plugin.Retrieval(retriever{})
	}
	if err == nil {
		m := plugin.Manifest()
		err = plugin.ServeReady(func(addr string) {
			// One startup line after bind succeeds, so an operator sees the sidecar came up and what it serves.
			fmt.Fprintf(os.Stderr, "quivr-core-retrieve: serving %s@%s (retrieval) on %s\n", m.ID, m.Version, addr)
		})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "quivr-core-retrieve:", err)
		os.Exit(1)
	}
}
