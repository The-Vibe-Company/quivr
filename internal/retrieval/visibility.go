package retrieval

import (
	"context"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
)

func visibilityCandidate(h content.Hydrated, q Request) content.Candidate {
	return content.Candidate{
		SegmentID: h.Segment.ID, GenerationID: h.GenerationID,
		EvaluationPlugin: q.EvaluationPlugin, EvaluationSpace: q.Space,
	}
}

// Recheck the transcript before it can be sent again in a later plugin round.
func (sv *server) recheckServed(ctx context.Context, q Request, served []plugins.ServedRequest) (bool, error) {
	if len(served) == 0 {
		return true, nil
	}
	start := time.Now()
	defer func() { sv.timing.Hydration += time.Since(start) }()
	sv.mu.Lock()
	candidates := make([]content.Candidate, 0, len(sv.hydrated))
	seen := make(map[string]bool, len(sv.hydrated))
	for _, batch := range served {
		for _, candidate := range batch.Candidates {
			if !seen[candidate.SegmentID] {
				seen[candidate.SegmentID] = true
				candidates = append(candidates, visibilityCandidate(sv.hydrated[candidate.SegmentID], q))
			}
		}
	}
	sv.mu.Unlock()
	allowed, err := sv.s.Content.RecheckHydration(ctx, sv.scope, candidates)
	if err != nil {
		return false, ErrUnavailable
	}
	for i := range candidates {
		if !allowed[i] {
			return false, nil
		}
	}
	return true, nil
}

// A ranker can derive scores and explanations from any candidate it received,
// including one served by a composed profile. A revoked input invalidates the
// whole ranking, even if the ranker left that candidate out of its final hits.
func (s Service) recheckResult(ctx context.Context, scope corpus.Scope, seen map[content.Candidate]struct{}, result *Result) error {
	start := time.Now()
	candidates := make([]content.Candidate, 0, len(seen))
	for c := range seen {
		candidates = append(candidates, c)
	}
	allowed, err := s.Content.RecheckHydration(ctx, scope, candidates)
	if err != nil {
		return err
	}
	for i := range candidates {
		if !allowed[i] {
			result.Hits = result.Hits[:0]
			break
		}
	}
	if result.Usage != nil {
		elapsed := time.Since(start)
		result.Usage.Elapsed += elapsed
		result.Usage.Phases.Hydration += elapsed
		result.Usage.OverObjective = result.Usage.Elapsed > result.Usage.Objective
	}
	return nil
}
