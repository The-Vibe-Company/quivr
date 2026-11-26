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
func (sv *server) recheckServed(ctx context.Context, q Request, session *plugins.RetrievalSession) error {
	if len(sv.hydrated) == 0 {
		return nil
	}
	start := time.Now()
	defer func() { sv.timing.Hydration += time.Since(start) }()
	candidates := make([]content.Candidate, 0, len(sv.hydrated))
	for _, h := range sv.hydrated {
		candidates = append(candidates, visibilityCandidate(h, q))
	}
	allowed, err := sv.s.Content.RecheckHydration(ctx, sv.scope, candidates)
	if err != nil {
		return ErrUnavailable
	}
	for i, c := range candidates {
		if !allowed[i] {
			delete(sv.hydrated, c.SegmentID)
		}
	}
	session.KeepServed(func(id string) bool {
		_, ok := sv.hydrated[id]
		return ok
	})
	return nil
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
