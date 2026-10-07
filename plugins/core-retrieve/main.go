// Command quivr-core-retrieve is the first-party retrieval plugin
// (core.retrieve): the search the engine ran itself before THE-779. It asks
// the engine for one candidate list that follows the search mode, and returns
// that list as the ranking; see README.md.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
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
}

type retriever struct{}

func (retriever) Search(_ context.Context, req *quivrplugin.SearchRequest) (*quivrplugin.SearchAnswer, error) {
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
	if req.Round == 1 || len(pending) > 0 {
		c, err := request(req)
		if err != nil {
			return nil, err
		}
		requests := []quivrplugin.CandidateRequest{c}
		if req.Query.Mode != "lexical" {
			requests = nil
			for _, space := range pending[:min(8, len(pending))] {
				next := c
				next.Space = space.ID
				requests = append(requests, next)
			}
		}
		return quivrplugin.Ask(requests...), nil
	}
	// The served order is the ranking: the index ranked it, and the engine
	// kept each segment's first object, as the engine's own search did.
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
	config := configuration{DenseWeight: alpha, CandidateCount: req.Limit, HybridFusion: "relative_score"}
	if len(req.Configuration) > 0 {
		if err := json.Unmarshal(req.Configuration, &config); err != nil {
			return quivrplugin.CandidateRequest{}, quivrplugin.TerminalSearchError("invalid_configuration", err.Error())
		}
	}
	c := quivrplugin.CandidateRequest{QueryText: req.Query.Text, K: config.CandidateCount, GroupBy: "record"}
	if req.Query.Mode == "lexical" {
		c.Primitive, c.Field = quivrplugin.PrimitiveBM25, quivrplugin.FieldSource
		return c, nil
	}
	space := req.ServedSpace()
	if space == nil {
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

func main() {
	plugin, err := quivrplugin.New("")
	if err == nil {
		err = plugin.Retrieval(retriever{})
	}
	if err == nil {
		m := plugin.Manifest()
		host, port := os.Getenv(quivrplugin.EnvHost), os.Getenv(quivrplugin.EnvPort)
		if host == "" {
			host = "127.0.0.1"
		}
		if port == "" {
			port = "8080"
		}
		// One startup line, so an operator sees the sidecar came up and what it serves.
		fmt.Fprintf(os.Stderr, "quivr-core-retrieve: serving %s@%s (retrieval) on %s\n", m.ID, m.Version, net.JoinHostPort(host, port))
		err = plugin.Serve()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "quivr-core-retrieve:", err)
		os.Exit(1)
	}
}
