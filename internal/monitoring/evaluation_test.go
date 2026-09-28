package monitoring_test

import (
	"context"
	"errors"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
)

func fixtureInput(decisions map[string]any, enriched bool, texts ...string) monitoring.EvaluationInput {
	parts := make([]monitoring.Part, len(texts))
	for i, text := range texts {
		parts[i] = monitoring.Part{Key: []string{"title", "body", "extra"}[i], Role: "body", Text: text}
	}
	return monitoring.EvaluationInput{
		Evaluator: monitoring.Evaluator{PluginID: monitoring.FixtureEvaluator, Version: monitoring.FixtureEvaluatorVersion, Configuration: map[string]any{"decisions": decisions}},
		RecordID:  "record", VersionID: "version", Parts: parts, Enriched: enriched,
	}
}

func TestFixtureDecisions(t *testing.T) {
	ctx := context.Background()
	fixture := monitoring.Fixture{}
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
			got, err := fixture.Evaluate(ctx, fixtureInput(decisions, c.enriched, c.text))
			if err != nil || got.Decision != c.want {
				t.Fatalf("got %+v, %v; want %s", got, err, c.want)
			}
		})
	}
	got, err := fixture.Evaluate(ctx, fixtureInput(map[string]any{"default": "match"}, false, "Anything"))
	if err != nil || got.Decision != monitoring.DecisionMatch || got.Explanation == "" {
		t.Fatalf("default decision: %+v %v", got, err)
	}
	if got.Details["marker"] != "default" {
		t.Fatalf("details: %+v", got.Details)
	}
}

func TestFixtureEvidenceNamesMatchingParts(t *testing.T) {
	got, err := monitoring.Fixture{}.Evaluate(context.Background(), fixtureInput(map[string]any{"ALERTE": "match"}, false, "Titre ALERTE", "Corps", "ALERTE encore"))
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
	if _, err := (monitoring.Fixture{}).Evaluate(ctx, fixtureInput(map[string]any{"PANNE": "error"}, false, "PANNE")); !errors.Is(err, monitoring.ErrEvaluation) {
		t.Fatalf("error decision: %v", err)
	}
	if _, err := (monitoring.Fixture{}).Evaluate(ctx, fixtureInput(map[string]any{"X": "maybe"}, false, "X")); !errors.Is(err, monitoring.ErrEvaluatorConfiguration) {
		t.Fatalf("invalid decision: %v", err)
	}
	in := fixtureInput(nil, false, "x")
	in.Evaluator.Configuration = map[string]any{"decisions": "not an object"}
	if _, err := (monitoring.Fixture{}).Evaluate(ctx, in); !errors.Is(err, monitoring.ErrEvaluatorConfiguration) {
		t.Fatalf("invalid configuration: %v", err)
	}
}
