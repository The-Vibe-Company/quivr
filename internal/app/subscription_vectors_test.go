package app

import (
	"context"
	"errors"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
)

type unavailableVectorRouting struct {
	calls int
	err   error
}

func (r *unavailableVectorRouting) Authorize(context.Context, corpus.Scope, []string) error {
	return nil
}

func (r *unavailableVectorRouting) Generation(context.Context, string, string) (content.Generation, error) {
	r.calls++
	return content.Generation{}, r.err
}

func TestExternalMeaningQueriesDoNotRequireLocalVectors(t *testing.T) {
	pins, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: "../../plugins/alerts/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:9999"}})
	if err != nil {
		t.Fatal(err)
	}
	live := &monitoring.LiveEvaluators{}
	live.Store(monitoring.PlanEvaluators{Served: monitoring.Evaluators{"alerts@0.3.0": pluginhttp.Evaluator{Pin: pins.Evaluators()[0]}}})
	unavailable := errors.New("local vector routing unavailable")
	for _, test := range []struct {
		kind, backend string
		local         bool
	}{
		{"described", "", false}, {"described", "jev", false},
		{"keywords_or_meaning", "jev", false}, {"keywords_and_meaning", "jev", false},
		{"meaning", "vectors", true}, {"described", "vectors", true},
		{"keywords_or_meaning", "vectors", true}, {"keywords_and_meaning", "vectors", true},
	} {
		t.Run(test.kind+"/"+test.backend, func(t *testing.T) {
			routing := &unavailableVectorRouting{err: unavailable}
			encoder := savedQueryEncoder{evaluators: live, search: retrieval.Service{Routing: routing}}
			expression := map[string]any{"kind": test.kind, "description": "Strikes at ports"}
			if test.backend != "" {
				expression["meaning_check"] = test.backend
			}
			if test.kind == "keywords_or_meaning" || test.kind == "keywords_and_meaning" {
				expression["match"] = map[string]any{"term": "port"}
			}
			vectors, err := encoder.EncodeSavedQuery(context.Background(), "neutral-org", monitoring.Definition{CorpusIDs: []string{"corpus"}, Expression: expression})
			if test.local {
				if !errors.Is(err, unavailable) || routing.calls != 1 {
					t.Fatalf("local expression did not invoke vector routing: calls=%d err=%v", routing.calls, err)
				}
			} else if err != nil || len(vectors) != 0 || routing.calls != 0 {
				t.Fatalf("external expression depends on local vectors: calls=%d vectors=%v err=%v", routing.calls, vectors, err)
			}
		})
	}
}
