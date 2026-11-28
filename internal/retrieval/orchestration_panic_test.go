package retrieval_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/visibility"
)

// What a panic value may carry and a log must never repeat.
const (
	panicQuery     = "confidential lanterne"
	panicOrg       = "org-confidential"
	panicPrincipal = "user:confidential-reader"
)

func leak(site string) {
	panic(fmt.Sprintf("%s: %s %s %s", site, panicQuery, panicOrg, panicPrincipal))
}

type panickingProjection struct{ fakeProjection }

func (p *panickingProjection) Search(context.Context, []retrieval.Route, corpus.Scope, retrieval.Request) ([]content.Candidate, error) {
	leak("projection")
	return nil, nil
}

type panickingBaseline struct{ content.BaselineRepository }

func (panickingBaseline) Hydrate(context.Context, corpus.Scope, []content.Candidate) (map[int]content.Located, error) {
	leak("hydrate")
	return nil, nil
}

// panickingLeads hydrates rich candidates but panics looking up their leads.
type panickingLeads struct{ *richBaseline }

func (panickingLeads) LeadSegments(context.Context, string, []content.LeadSegmentRef) (map[string]string, error) {
	leak("leads")
	return nil, nil
}

type panickingEmbedder struct{ fakeEmbedder }

func (panickingEmbedder) Embed(context.Context, string) ([]float32, error) {
	leak("embedder")
	return nil, nil
}

// syncBuffer is a log sink the search's goroutines may write concurrently.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// A dependency that panics on any search path, on the search goroutine or on
// one the engine started for it, fails that search as unavailable instead of
// the process, stops the work it started, and logs where it panicked without
// the query, Organization or principals the panic value carried.
func TestOrchestrationPanic(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  string
		setup func(*retrieval.Service)
	}{
		{name: "projection search", mode: "lexical", setup: func(s *retrieval.Service) { s.Projection = &panickingProjection{} }},
		{name: "projection search in a concurrent round", setup: func(s *retrieval.Service) {
			s.Projection = &panickingProjection{}
			s.Ranker = &scriptedRanker{answer: fusion}
		}},
		{name: "ranker round", setup: func(s *retrieval.Service) {
			s.Ranker = &scriptedRanker{answer: func(context.Context, plugins.SearchRequest) ([]byte, error) {
				leak("ranker")
				return nil, nil
			}}
		}},
		{name: "content hydrate", setup: func(s *retrieval.Service) { s.Content.Baseline = panickingBaseline{} }},
		{name: "content lead segments", mode: "lexical", setup: func(s *retrieval.Service) {
			gen := content.Generation{ID: "gen-rich", Collection: "Shared", ProfileVersion: retrieval.ProfileVersion, SpaceID: "space"}
			s.Routing = spaceRouting{"corpus": gen}
			s.Projection = &spaceProjection{bySpace: map[string][]content.Candidate{"": {{SegmentID: "seg-deep", GenerationID: gen.ID, Score: 0.9}}}}
			s.Ranker = &richRanker{id: "example.rich", apiRange: ">=0.21.0 <0.22.0", includeLead: true}
			s.Content = content.Service{Baseline: panickingLeads{&richBaseline{}}, Blobs: richBlobs{}}
		}},
		{name: "query embedder", setup: func(s *retrieval.Service) {
			// The shared query-vector cache encodes on its own goroutine.
			s.Embedder, s.QueryVectors = panickingEmbedder{}, retrieval.NewQueryVectorCache()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := &syncBuffer{}
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			synctest.Test(t, func(t *testing.T) {
				baseline := runtime.NumGoroutine()
				p := &fakeProjection{candidates: []content.Candidate{{SegmentID: "vec-a", GenerationID: "gen", Score: 1}}}
				s := service(p, &fakeEmbeddings{})
				tc.setup(&s)
				ctx := visibility.WithPrincipals(t.Context(), []string{panicPrincipal})
				scope := searchScope
				scope.Organization = panicOrg
				_, err := s.Search(ctx, scope, retrieval.Request{Query: panicQuery, Mode: tc.mode, CorpusIDs: []string{"corpus"}})
				if !errors.Is(err, retrieval.ErrUnavailable) {
					t.Fatalf("search with a panicking dependency = %v, want ErrUnavailable", err)
				}
				// Wait until every goroutine the search started, such as the
				// query-vector cache's encoding goroutine, has exited or is
				// durably blocked: none may remain.
				synctest.Wait()
				if n := runtime.NumGoroutine(); n != baseline {
					t.Fatalf("%d goroutines after the search settled, want the %d before it", n, baseline)
				}
			})
			out := logs.String()
			if !strings.Contains(out, "panic") {
				t.Fatalf("no panic was logged; logs:\n%s", out)
			}
			for _, secret := range []string{panicQuery, panicOrg, panicPrincipal} {
				if strings.Contains(out, secret) {
					t.Fatalf("logs repeat %q:\n%s", secret, out)
				}
			}
		})
	}
}
