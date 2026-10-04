// Command fusion-retriever is a sample retrieval plugin built with the Quivr
// Go plugin SDK. Round 1 asks for keyword candidates and vector candidates in
// the served space (following the client's mode), and the last round fuses
// every served list by reciprocal rank, explaining each hit's ranks. Its deep
// profile adds a round that asks for passages close to the best keyword hit.
// It is deterministic and needs no model.
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

// rrfK damps the weight of the first ranks in reciprocal rank fusion.
const rrfK = 60

type fusion struct {
	k int
}

func (f fusion) Search(_ context.Context, req *quivrplugin.SearchRequest) (*quivrplugin.SearchAnswer, error) {
	switch {
	case req.Round == 1:
		return quivrplugin.Ask(f.first(req)...), nil
	case req.Round == 2 && req.Profile == "deep":
		if seed := best(req, quivrplugin.PrimitiveBM25); seed != nil && req.ServedSpace() != nil {
			return quivrplugin.Ask(quivrplugin.CandidateRequest{Primitive: quivrplugin.PrimitiveNearVector, Space: req.ServedSpace().ID, QueryText: seed.Text, K: f.k}), nil
		}
	}
	return quivrplugin.Rank(fuse(req)...), nil
}

// first follows the client's mode: keywords, vectors in the served space, or
// both.
func (f fusion) first(req *quivrplugin.SearchRequest) []quivrplugin.CandidateRequest {
	keywords := quivrplugin.CandidateRequest{Primitive: quivrplugin.PrimitiveBM25, Field: quivrplugin.FieldSource, QueryText: req.Query.Text, K: f.k}
	space := req.ServedSpace()
	if space == nil || req.Query.Mode == "lexical" {
		return []quivrplugin.CandidateRequest{keywords}
	}
	vectors := quivrplugin.CandidateRequest{Primitive: quivrplugin.PrimitiveNearVector, Space: space.ID, QueryText: req.Query.Text, K: f.k}
	if req.Query.Mode == "semantic" {
		return []quivrplugin.CandidateRequest{vectors}
	}
	return []quivrplugin.CandidateRequest{keywords, vectors}
}

// best is the first candidate served for a primitive, or nil.
func best(req *quivrplugin.SearchRequest, primitive string) *quivrplugin.Candidate {
	for _, s := range req.Served {
		if s.Request.Primitive == primitive && len(s.Candidates) > 0 {
			return &s.Candidates[0]
		}
	}
	return nil
}

// fuse ranks every served candidate by the sum of 1/(rrfK+rank) over the
// lists that served it, ties broken by segment id.
func fuse(req *quivrplugin.SearchRequest) []quivrplugin.RankedHit {
	scores := map[string]float64{}
	ranks := map[string][]string{}
	for _, s := range req.Served {
		label := map[string]string{quivrplugin.PrimitiveBM25: "keywords", quivrplugin.PrimitiveNearVector: "vectors", quivrplugin.PrimitiveHybrid: "hybrid"}[s.Request.Primitive]
		if s.Round > 1 {
			label = "near the best keyword hit"
		}
		for i, c := range s.Candidates {
			scores[c.SegmentID] += 1 / float64(rrfK+i+1)
			ranks[c.SegmentID] = append(ranks[c.SegmentID], fmt.Sprintf("#%d by %s", i+1, label))
		}
	}
	ids := make([]string, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if scores[ids[i]] != scores[ids[j]] {
			return scores[ids[i]] > scores[ids[j]]
		}
		return ids[i] < ids[j]
	})
	hits := []quivrplugin.RankedHit{}
	for _, id := range ids[:min(len(ids), req.Limit)] {
		hits = append(hits, quivrplugin.RankedHit{SegmentID: id, Score: scores[id], Explanation: strings.Join(ranks[id], ", ")})
	}
	return hits
}

func main() {
	plugin, err := quivrplugin.New("")
	if err == nil {
		err = plugin.Retrieval(fusion{k: plugin.Manifest().Retrieval.Limits.MaxCandidates})
	}
	if err == nil {
		err = plugin.Serve()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fusion-retriever:", err)
		os.Exit(1)
	}
}
