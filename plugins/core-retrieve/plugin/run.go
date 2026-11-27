// Command quivr-core-retrieve is the first-party retrieval plugin
// (core.retrieve): the search the engine ran itself before THE-779. It asks
// the engine for one candidate list that follows the search mode, and returns
// that list as the ranking; see README.md.
package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

// alpha weights the vector side of a hybrid search; relative score fusion
// normalizes each side's scores before weighting them.
const alpha = 0.5

// configuration is validated by the manifest schema at install and by the
// SDK on each invocation. Defaults are applied here, not by JSON Schema.
type configuration struct {
	DenseWeight    float64 `json:"dense_weight"`
	CandidateCount int     `json:"candidate_count"`
	HybridFusion   string  `json:"hybrid_fusion"`

	// Passage aggregation (passages.go); off while PassageWeight is 0.
	PassageWeight     float64 `json:"passage_weight"`
	PassageCandidates int     `json:"passage_candidates"`
	PassageMaxWeight  float64 `json:"passage_max_weight"`
	PassageTop2Weight float64 `json:"passage_top2_weight"`
	PassageLeadWeight float64 `json:"passage_lead_weight"`
}

func parseConfiguration(req *quivrplugin.SearchRequest) (configuration, error) {
	config := configuration{
		DenseWeight: alpha, CandidateCount: req.Limit, HybridFusion: "relative_score",
		PassageCandidates: defaultPassageCandidates, PassageMaxWeight: 1,
	}
	if len(req.Configuration) > 0 {
		if err := json.Unmarshal(req.Configuration, &config); err != nil {
			return config, quivrplugin.TerminalSearchError("invalid_configuration", err.Error())
		}
	}
	return config, nil
}

type retriever struct{}

func (retriever) Search(_ context.Context, req *quivrplugin.SearchRequest) (*quivrplugin.SearchAnswer, error) {
	config, err := parseConfiguration(req)
	if err != nil {
		return nil, err
	}
	pending := []quivrplugin.SearchSpace{}
	if req.Query.Mode != "lexical" {
		requested := map[string]bool{}
		for _, served := range req.Served {
			requested[served.Request.Space] = true
		}
		for _, space := range req.Spaces {
			if space.Role == "served" && !requested[space.ID] {
				pending = append(pending, space)
			}
		}
	}
	if req.Round == 1 || (req.Round < 3 && len(pending) > 0) {
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
		// Passage aggregation adds one ungrouped request in the served space,
		// so several passages of a Record can be served.
		if served := req.ServedSpace(); req.Round == 1 && config.PassageWeight > 0 && c.Primitive != quivrplugin.PrimitiveBM25 && served != nil && !isImageOnlySpace(served.Indexes) && len(requests) < 8 {
			requests = append(requests, quivrplugin.CandidateRequest{Primitive: quivrplugin.PrimitiveNearVector, QueryText: req.Query.Text, Space: served.ID, K: config.PassageCandidates})
		}
		return quivrplugin.Ask(requests...), nil
	}
	// Served candidates rank by score, then segment id; the Record dedupe below keeps the first in that order.
	hits := []quivrplugin.RankedHit{}
	segmentRecords := map[string]string{}
	for _, s := range req.Served {
		if isPassageRequest(s.Request) {
			continue
		}
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
	if config.PassageWeight == 0 {
		return quivrplugin.Rank(unique[:min(len(unique), req.Limit)]...), nil
	}
	return quivrplugin.Rank(blend(req, config, unique, segmentRecords)[:min(len(unique), req.Limit)]...), nil
}

// blend re-scores the first-round ranking: each Record's min-max normalized
// first-round score is interpolated with its passage aggregate, which is 0
// for a Record none of whose passages was served.
func blend(req *quivrplugin.SearchRequest, config configuration, unique []quivrplugin.RankedHit, segmentRecords map[string]string) []quivrplugin.RankedHit {
	recordOf := func(h quivrplugin.RankedHit) string {
		if key := segmentRecords[h.SegmentID]; key != "" {
			return key
		}
		return h.SegmentID
	}
	firstRound := make(map[string]scored, len(unique))
	for _, h := range unique {
		firstRound[recordOf(h)] = scored{h.SegmentID, h.Score}
	}
	base := minMax(firstRound)
	passages := passageScores(req.Served, config)
	out := make([]quivrplugin.RankedHit, 0, len(unique))
	for _, h := range unique {
		key := recordOf(h)
		h.Score = (1-config.PassageWeight)*base[key] + config.PassageWeight*passages[key]
		out = append(out, h)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].SegmentID < out[j].SegmentID
	})
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
