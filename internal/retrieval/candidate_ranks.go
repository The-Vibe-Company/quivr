package retrieval

import (
	"cmp"
	"slices"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

// NormalizeCandidateRanks puts independent index partitions on the existing
// reciprocal-rank scale before they are merged. Raw BM25 and hybrid scores
// describe different populations. The retrieval plugin still ranks the hits.
// It keeps each generation/segment's best object and leaves rows unchanged.
func NormalizeCandidateRanks(rows []content.Candidate) []content.Candidate {
	pool := map[string]content.Candidate{}
	for _, row := range rows {
		key := row.GenerationID + "/" + row.SegmentID
		if previous, ok := pool[key]; !ok || row.Score > previous.Score {
			pool[key] = row
		}
	}
	out := make([]content.Candidate, 0, len(pool))
	for _, row := range pool {
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b content.Candidate) int {
		return cmp.Or(cmp.Compare(b.Score, a.Score), cmp.Compare(a.SegmentID, b.SegmentID), cmp.Compare(a.GenerationID+"/"+a.VersionID, b.GenerationID+"/"+b.VersionID))
	})
	for i := range out {
		out[i].Score = 1 / float64(60+i+1)
	}
	return out
}
