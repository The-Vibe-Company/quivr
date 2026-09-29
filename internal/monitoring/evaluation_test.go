package monitoring_test

import (
	"context"
	"errors"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
)

// fixtureEval decides one fixture evaluation of a Version with these texts.
type fixtureEval struct {
	configuration map[string]any
	enriched      bool
	texts         []string
}

func fixtureInput(decisions map[string]any, enriched bool, texts ...string) fixtureEval {
	return fixtureEval{configuration: map[string]any{"decisions": decisions}, enriched: enriched, texts: texts}
}

func (f fixtureEval) run(ctx context.Context) (monitoring.Evaluation, error) {
	parts := make([]monitoring.Part, len(f.texts))
	for i, text := range f.texts {
		parts[i] = monitoring.Part{Key: []string{"title", "body", "extra"}[i], Role: "body", Text: text}
	}
	out, err := monitoring.Fixture{}.Evaluate(ctx, monitoring.Batch{RecordID: "record", VersionID: "version", Enriched: f.enriched, Article: monitoring.Article{Parts: parts},
		Items: []monitoring.BatchItem{{ID: "e1", Configuration: f.configuration}}})
	if err != nil {
		return monitoring.Evaluation{}, err
	}
	return out[0].Evaluation, out[0].Err
}

func TestFixtureDecisions(t *testing.T) {
	ctx := context.Background()
	decisions := map[string]any{"ALERTE": "match", "IGNORE": "no_match", "ATTENTE": "not_ready", "PANNE": "error", "VECTEUR": "match_after_enrichment"}
	cases := []struct {
		name     string
		text     string
		enriched bool
		want     monitoring.Decision
	}{
		{"marker match", "Dépêche ALERTE séisme", false, monitoring.DecisionMatch},
		{"marker no match", "Dépêche IGNORE", false, monitoring.DecisionNoMatch},
		{"no marker uses absent default", "Dépêche ordinaire", false, monitoring.DecisionNoMatch},
		{"not ready", "ATTENTE de données", false, monitoring.DecisionNotReady},
		{"enrichment gate before", "VECTEUR", false, monitoring.DecisionNotReady},
		{"enrichment gate after", "VECTEUR", true, monitoring.DecisionMatch},
		// Markers are checked in sorted order, so ALERTE wins over IGNORE.
		{"sorted order", "IGNORE puis ALERTE", false, monitoring.DecisionMatch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := fixtureInput(decisions, c.enriched, c.text).run(ctx)
			if err != nil || got.Decision != c.want {
				t.Fatalf("got %+v, %v; want %s", got, err, c.want)
			}
		})
	}
	got, err := fixtureInput(map[string]any{"default": "match"}, false, "Anything").run(ctx)
	if err != nil || got.Decision != monitoring.DecisionMatch || got.Explanation == "" {
		t.Fatalf("default decision: %+v %v", got, err)
	}
	if got.Details["marker"] != "default" {
		t.Fatalf("details: %+v", got.Details)
	}
}

func TestFixtureEvidenceNamesMatchingParts(t *testing.T) {
	got, err := fixtureInput(map[string]any{"ALERTE": "match"}, false, "Titre ALERTE", "Corps", "ALERTE encore").run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.PartKeys) != 2 || got.PartKeys[0] != "title" || got.PartKeys[1] != "extra" {
		t.Fatalf("part keys: %v", got.PartKeys)
	}
}

// Errors stay errors: they never become a negative decision.
func TestFixtureErrorsAreNotNegativeDecisions(t *testing.T) {
	ctx := context.Background()
	if _, err := fixtureInput(map[string]any{"PANNE": "error"}, false, "PANNE").run(ctx); !errors.Is(err, monitoring.ErrEvaluation) {
		t.Fatalf("error decision: %v", err)
	}
	if _, err := fixtureInput(map[string]any{"X": "maybe"}, false, "X").run(ctx); !errors.Is(err, monitoring.ErrEvaluatorConfiguration) {
		t.Fatalf("invalid decision: %v", err)
	}
	in := fixtureInput(nil, false, "x")
	in.configuration = map[string]any{"decisions": "not an object"}
	if _, err := in.run(ctx); !errors.Is(err, monitoring.ErrEvaluatorConfiguration) {
		t.Fatalf("invalid configuration: %v", err)
	}
}
