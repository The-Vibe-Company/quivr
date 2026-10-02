package fakeplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"sort"
	"strings"
)

const (
	FixtureEvaluator        = "quivr.fixture"
	FixtureEvaluatorVersion = "1.0.0"
)

// FixtureEvaluators installs only the deterministic fixture evaluator.
func FixtureEvaluators() monitoring.Evaluators {
	return monitoring.Evaluators{monitoring.EvaluatorKey(monitoring.Evaluator{PluginID: FixtureEvaluator, Version: FixtureEvaluatorVersion}): Fixture{}}
}

// Fixture is the deterministic test evaluator for notification mechanics; it
// is not a relevance algorithm, and it is installed only where a deployment
// enables it for tests. Its pinned configuration is
// {"decisions": {"<marker>": "<decision>", ..., "default": "<decision>"}}.
// Markers are literal substrings of monitoring.Part text, checked in sorted order; the
// first present marker decides, otherwise "default" (absent: no_match).
// Decisions are match, no_match and not_ready, plus two fixture-only,
// test-oriented values: "error" fails the evaluation, and
// "match_after_enrichment" is not_ready until the Version is enriched.
type Fixture struct{}

// MaxBatch accepts the protocol's largest batch.
func (Fixture) MaxBatch() int { return 256 }

// Validate accepts any expression; the fixture reads only its configuration.
func (Fixture) Validate(map[string]any, map[string]any) error { return nil }

func (f Fixture) Evaluate(_ context.Context, b monitoring.Batch) ([]monitoring.Outcome, error) {
	out := make([]monitoring.Outcome, len(b.Items))
	for i, item := range b.Items {
		ev, err := fixtureDecide(item.Configuration, b.Article.Parts, b.Enriched)
		out[i] = monitoring.Outcome{Evaluation: ev, Err: err}
	}
	return out, nil
}

func fixtureDecide(configuration map[string]any, parts []monitoring.Part, enriched bool) (monitoring.Evaluation, error) {
	raw, ok := configuration["decisions"]
	if !ok {
		raw = map[string]any{}
	}
	decisions, ok := raw.(map[string]any)
	if !ok {
		return monitoring.Evaluation{}, fmt.Errorf("%w: decisions must be an object", monitoring.ErrEvaluatorConfiguration)
	}
	markers := make([]string, 0, len(decisions))
	for marker := range decisions {
		if marker != "default" && marker != "" {
			markers = append(markers, marker)
		}
	}
	sort.Strings(markers)
	chosen, value := "default", any("no_match")
	if v, ok := decisions["default"]; ok {
		value = v
	}
	var keys []string
	for _, marker := range markers {
		for _, p := range parts {
			if strings.Contains(p.Text, marker) {
				keys = append(keys, p.Key)
			}
		}
		if len(keys) > 0 {
			chosen, value = marker, decisions[marker]
			break
		}
	}
	decision, _ := value.(string)
	switch decision {
	case "error":
		return monitoring.Evaluation{}, fmt.Errorf("%w: fixture error decision", monitoring.ErrEvaluation)
	case "match_after_enrichment":
		if !enriched {
			decision = string(monitoring.DecisionNotReady)
		} else {
			decision = string(monitoring.DecisionMatch)
		}
	case string(monitoring.DecisionMatch), string(monitoring.DecisionNoMatch), string(monitoring.DecisionNotReady):
	default:
		return monitoring.Evaluation{}, fmt.Errorf("%w: unknown decision", monitoring.ErrEvaluatorConfiguration)
	}
	return monitoring.Evaluation{
		Decision:    monitoring.Decision(decision),
		Explanation: fmt.Sprintf("Fixture evaluator decided %s from marker %q.", decision, truncate(chosen, 256)),
		PartKeys:    keys,
		Details:     map[string]any{"marker": truncate(chosen, 256), "decision": decision},
	}, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// fixtureSubscription translates the shared deterministic rule to the Plugin
// API. An item error fails the invocation; the engine splits terminal batches
// to isolate it, as it does for every remote alert rule.
func fixtureSubscription(body []byte) (int, any) {
	var request struct {
		Record struct {
			Parts []struct {
				Key  string `json:"key"`
				Role string `json:"role"`
				Text string `json:"text"`
			} `json:"parts"`
			Enriched bool `json:"enriched"`
		} `json:"record"`
		Evaluations []struct {
			ID            string         `json:"id"`
			Configuration map[string]any `json:"configuration"`
		} `json:"evaluations"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return 400, map[string]any{"code": "invalid_request", "message": "invalid JSON", "retryable": false}
	}
	parts := make([]monitoring.Part, len(request.Record.Parts))
	for i, p := range request.Record.Parts {
		parts[i] = monitoring.Part{Key: p.Key, Role: p.Role, Text: p.Text}
	}
	decisions := []any{}
	for _, item := range request.Evaluations {
		ev, err := fixtureDecide(item.Configuration, parts, request.Record.Enriched)
		if err != nil {
			code := "evaluation_failed"
			if errors.Is(err, monitoring.ErrEvaluatorConfiguration) {
				code = "invalid_configuration"
			}
			return 422, map[string]any{"code": code, "message": err.Error(), "retryable": false}
		}
		decision := map[string]any{"id": item.ID, "decision": ev.Decision}
		if ev.Decision == monitoring.DecisionMatch {
			evidence := map[string]any{"explanation": ev.Explanation, "details": ev.Details}
			if len(ev.PartKeys) > 0 {
				evidence["part_keys"] = ev.PartKeys
			}
			decision["evidence"] = evidence
		}
		decisions = append(decisions, decision)
	}
	return 200, map[string]any{"decisions": decisions}
}
