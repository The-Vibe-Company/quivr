package retrieval

import (
	"context"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
)

type refusedHydration struct {
	content.BaselineRepository
}

func (refusedHydration) RecheckHydration(context.Context, corpus.Scope, []content.Candidate) (map[int]bool, error) {
	return map[int]bool{0: false}, nil
}

func TestVisibilityTranscriptRejectsExplicitRefusal(t *testing.T) {
	sv := server{
		s:        Service{Content: content.Service{Baseline: refusedHydration{}}},
		scope:    corpus.Scope{Actions: []string{"content:read", "search:query"}},
		timing:   &Phases{},
		hydrated: map[string]content.Hydrated{"segment": {Segment: content.Segment{ID: "segment"}, GenerationID: "generation"}},
	}
	allowed, err := sv.recheckServed(context.Background(), Request{}, []plugins.ServedRequest{{Candidates: []plugins.Candidate{{SegmentID: "segment"}}}})
	if err != nil || allowed {
		t.Fatalf("explicit denial authorized the next transcript: allowed=%t err=%v", allowed, err)
	}
}
