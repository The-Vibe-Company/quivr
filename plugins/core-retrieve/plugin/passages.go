package plugin

import (
	"math"
	"sort"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

// Passage aggregation, off by default: one ungrouped near_vector request in
// the served space lets several passages of a Record be served, and each
// Record is scored by a weighted mix of its best passage (MaxP), the mean of
// its two best (Top2Mean) and its first passage (FirstP). A Record that
// discusses the query across passages, or in its lead, then outranks one
// whose single passage mentions it in passing.

const (
	defaultPassageCandidates = 100
)

type scored struct {
	segment string
	score   float64
}

// minMax maps scores onto [0,1]; a constant list maps to 1.
func minMax(values map[string]scored) map[string]float64 {
	out := make(map[string]float64, len(values))
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, v := range values {
		lo, hi = math.Min(lo, v.score), math.Max(hi, v.score)
	}
	for k, v := range values {
		if hi > lo {
			out[k] = (v.score - lo) / (hi - lo)
		} else {
			out[k] = 1
		}
	}
	return out
}

// passageScores aggregates the ungrouped passage candidates per Record:
// weights apply to MaxP, Top2Mean and FirstP over min-max normalized passage
// scores. A passage not served counts as the pool's lowest score, so a
// Record needs evidence in two passages, or a strong lead, to gain.
func passageScores(served []quivrplugin.ServedRequest, cfg configuration) map[string]float64 {
	type rec struct {
		scores []float64
		lead   float64
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, s := range served {
		if !isPassageRequest(s.Request) {
			continue
		}
		for _, c := range s.Candidates {
			lo, hi = math.Min(lo, c.Score), math.Max(hi, c.Score)
		}
	}
	norm := func(v float64) float64 {
		if hi > lo {
			return (v - lo) / (hi - lo)
		}
		return 1
	}
	records := map[string]*rec{}
	seen := map[string]bool{}
	for _, s := range served {
		if !isPassageRequest(s.Request) {
			continue
		}
		for _, c := range s.Candidates {
			if seen[c.SegmentID] {
				continue
			}
			seen[c.SegmentID] = true
			r := records[c.RecordID]
			if r == nil {
				r = &rec{}
				records[c.RecordID] = r
			}
			v := norm(c.Score)
			r.scores = append(r.scores, v)
			if c.Start == 0 {
				r.lead = math.Max(r.lead, v)
			}
		}
	}
	total := cfg.PassageMaxWeight + cfg.PassageTop2Weight + cfg.PassageLeadWeight
	out := make(map[string]float64, len(records))
	if total <= 0 {
		return out
	}
	for id, r := range records {
		sort.Sort(sort.Reverse(sort.Float64Slice(r.scores)))
		maxP := r.scores[0]
		second := 0.0
		if len(r.scores) > 1 {
			second = r.scores[1]
		}
		top2 := (maxP + second) / 2
		out[id] = (cfg.PassageMaxWeight*maxP + cfg.PassageTop2Weight*top2 + cfg.PassageLeadWeight*r.lead) / total
	}
	return out
}

func isPassageRequest(c quivrplugin.CandidateRequest) bool {
	return c.GroupBy == "" && c.Primitive == quivrplugin.PrimitiveNearVector
}
