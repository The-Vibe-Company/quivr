package plugin

import (
	"fmt"
	"math"
	"sort"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

// searchTwoLeg answers a hybrid search with two separate candidate lists:
// keywords (one bm25 request on the title and body) and vectors (one
// near_vector request per served space). The final round normalizes each
// list on its own and fuses them per Record with the configured weight, so
// the fusion does not depend on how the index blends scores internally.
func searchTwoLeg(req *quivrplugin.SearchRequest, config configuration) *quivrplugin.SearchAnswer {
	askedKeywords := false
	askedSpaces := map[string]bool{}
	for _, s := range req.Served {
		if s.Request.Primitive == quivrplugin.PrimitiveBM25 {
			askedKeywords = true
		} else {
			askedSpaces[s.Request.Space] = true
		}
	}
	if req.Round < 3 {
		base := quivrplugin.CandidateRequest{QueryText: req.Query.Text, K: config.CandidateCount, GroupBy: "record"}
		requests := []quivrplugin.CandidateRequest{}
		if !askedKeywords && req.Round == 1 {
			keywords := base
			keywords.Primitive, keywords.Field = quivrplugin.PrimitiveBM25, quivrplugin.FieldSource
			requests = append(requests, keywords)
		}
		for _, space := range req.Spaces {
			if len(requests) == 8 {
				break
			}
			if space.Role == "served" && !askedSpaces[space.ID] {
				vectors := base
				vectors.Primitive, vectors.Space = quivrplugin.PrimitiveNearVector, space.ID
				requests = append(requests, vectors)
			}
		}
		if len(requests) > 0 {
			return quivrplugin.Ask(requests...)
		}
	}
	return quivrplugin.Rank(fuseLegs(req.Served, config, req.Limit)...)
}

// legHit is a Record's best candidate in one served list.
type legHit struct {
	segment string
	score   float64
	rank    int
}

// fused is a Record's contribution from both legs.
type fused struct {
	record   string
	segment  string
	score    float64
	vector   float64 // the best vector contribution, across served spaces
	keywords bool
	vectors  bool
}

// fuseLegs normalizes every served list, keeps each Record's best normalized
// score per leg (keywords or vectors, the latter across served spaces), and
// ranks Records by w*vectors + (1-w)*keywords; a Record missing from a leg
// scores 0 on it. The passage shown is the vector one when available.
func fuseLegs(served []quivrplugin.ServedRequest, config configuration, limit int) []quivrplugin.RankedHit {
	w := config.DenseWeight
	byRecord := map[string]*fused{}
	order := []string{}
	for _, s := range served {
		dense := s.Request.Primitive != quivrplugin.PrimitiveBM25
		weight := 1 - w
		if dense {
			weight = w
		}
		hits := bestPerRecord(s.Candidates)
		norm := normalizer(config, dense, hits)
		for record, h := range hits {
			f := byRecord[record]
			if f == nil {
				f = &fused{record: record, segment: h.segment}
				byRecord[record] = f
				order = append(order, record)
			}
			contribution := weight * norm(h)
			if dense {
				// The best space counts once; its passage is shown.
				if !f.vectors || contribution > f.vector {
					f.score += contribution - f.vector
					f.vector, f.segment, f.vectors = contribution, h.segment, true
				}
			} else if !f.keywords {
				f.keywords = true
				f.score += contribution
			}
		}
	}
	out := make([]*fused, 0, len(order))
	for _, record := range order {
		out = append(out, byRecord[record])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].segment < out[j].segment
	})
	explanation := fmt.Sprintf("keywords and vectors fused by %s, dense weight %g", config.HybridFusion, w)
	ranking := make([]quivrplugin.RankedHit, 0, min(limit, len(out)))
	for _, f := range out[:min(limit, len(out))] {
		ranking = append(ranking, quivrplugin.RankedHit{SegmentID: f.segment, Score: f.score, Explanation: explanation})
	}
	return ranking
}

// bestPerRecord keeps each Record's highest-scoring candidate of one list and
// its 1-based rank among Records, by score then segment id.
func bestPerRecord(candidates []quivrplugin.Candidate) map[string]legHit {
	sorted := append([]quivrplugin.Candidate(nil), candidates...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Score != sorted[j].Score {
			return sorted[i].Score > sorted[j].Score
		}
		return sorted[i].SegmentID < sorted[j].SegmentID
	})
	out := map[string]legHit{}
	for _, c := range sorted {
		record := c.RecordID
		if record == "" {
			record = c.SegmentID
		}
		if _, ok := out[record]; !ok {
			out[record] = legHit{segment: c.SegmentID, score: c.Score, rank: len(out) + 1}
		}
	}
	return out
}

// normalizer maps one list's scores to [0, 1] (or a rank score for RRF):
//   - min_max: (s - min) / (max - min) over the list (convex combination);
//   - theoretical_min_max: (s - lower bound) / (max - lower bound), with the
//     score's theoretical lower bound (0 for BM25, -1 for cosine similarity),
//     so a short or uniform list is not stretched to the full range;
//   - dbsf: distribution-based score fusion, (s - (mean - 3 sd)) / (6 sd)
//     clamped to [0, 1];
//   - rrf: 1 / (k + rank), scaled by k + 1 so the top hit scores 1.
func normalizer(config configuration, dense bool, hits map[string]legHit) func(legHit) float64 {
	if len(hits) == 0 {
		return func(legHit) float64 { return 0 }
	}
	lo, hi, sum := math.Inf(1), math.Inf(-1), 0.0
	for _, h := range hits {
		lo, hi, sum = math.Min(lo, h.score), math.Max(hi, h.score), sum+h.score
	}
	switch config.HybridFusion {
	case fusionRRF:
		k := config.RRFK
		return func(h legHit) float64 { return (k + 1) / (k + float64(h.rank)) }
	case fusionDBSF:
		mean := sum / float64(len(hits))
		variance := 0.0
		for _, h := range hits {
			variance += (h.score - mean) * (h.score - mean)
		}
		sd := math.Sqrt(variance / float64(len(hits)))
		if sd == 0 {
			return func(legHit) float64 { return 1 }
		}
		low := mean - 3*sd
		return func(h legHit) float64 { return clamp01((h.score - low) / (6 * sd)) }
	case fusionTheoreticalMinMax:
		lo = 0
		if dense {
			lo = -1
		}
	}
	if hi <= lo {
		return func(legHit) float64 { return 1 }
	}
	return func(h legHit) float64 { return clamp01((h.score - lo) / (hi - lo)) }
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }
