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
	RRFK           float64 `json:"rrf_k"`
}

// Two-leg fusions: the plugin asks for keywords and vectors separately and
// fuses the two lists itself, instead of asking the index for a hybrid list.
const (
	fusionDBSF              = "dbsf"
	fusionMinMax            = "min_max"
	fusionTheoreticalMinMax = "theoretical_min_max"
	fusionRRF               = "reciprocal_rank"
	defaultRRFK             = 60
)

func twoLeg(fusion string) bool {
	switch fusion {
	case fusionDBSF, fusionMinMax, fusionTheoreticalMinMax, fusionRRF:
		return true
	}
	return false
}

func (c *configuration) load(req *quivrplugin.SearchRequest) error {
	*c = configuration{DenseWeight: alpha, CandidateCount: req.Limit, HybridFusion: "relative_score", RRFK: defaultRRFK}
	if len(req.Configuration) > 0 {
		if err := json.Unmarshal(req.Configuration, c); err != nil {
			return quivrplugin.TerminalSearchError("invalid_configuration", err.Error())
		}
	}
	return nil
}

type retriever struct{}

func (retriever) Search(_ context.Context, req *quivrplugin.SearchRequest) (*quivrplugin.SearchAnswer, error) {
	if req.Query.Mode == "hybrid" && req.ServedSpace() != nil {
		var config configuration
		if err := config.load(req); err != nil {
			return nil, err
		}
		if twoLeg(config.HybridFusion) {
			return searchTwoLeg(req, config), nil
		}
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
		c, err := request(req)
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

// request is the one candidate request of a search: keywords on the title
// and body for lexical, the served space for semantic, both for hybrid.
func request(req *quivrplugin.SearchRequest) (quivrplugin.CandidateRequest, error) {
	var config configuration
	if err := config.load(req); err != nil {
		return quivrplugin.CandidateRequest{}, err
	}
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
