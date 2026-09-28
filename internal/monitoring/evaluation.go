package monitoring

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Decision is an evaluator's answer for one pinned Subscription Version and
// one eligible Record Version. An evaluation that could not complete is an
// error, never a Decision.
type Decision string

const (
	DecisionMatch    Decision = "match"
	DecisionNoMatch  Decision = "no_match"
	DecisionNotReady Decision = "not_ready"
)

var (
	// ErrEvaluation reports that an evaluator could not complete.
	ErrEvaluation = errors.New("evaluator_error")
	// ErrEvaluatorConfiguration reports a pinned configuration the evaluator cannot interpret.
	ErrEvaluatorConfiguration = errors.New("evaluator_configuration_invalid")
)

// Part is one canonical text Part of the evaluated Record Version.
type Part struct {
	Key, Role, Text string
}

// EvaluationInput is the logical evaluation seam: pinned definitions plus an
// eligible Record Version's canonical text. It is not a remote wire protocol.
type EvaluationInput struct {
	Definition Definition
	Evaluator  Evaluator
	RecordID   string
	VersionID  string
	Parts      []Part
	// Enriched reports that the Version has embedding coverage in the active generation.
	Enriched bool
}

// Evaluation is a completed decision. Explanation, PartKeys and Details become
// the Match evidence when the Decision is DecisionMatch.
type Evaluation struct {
	Decision    Decision
	Explanation string
	PartKeys    []string
	Details     map[string]any
}

// EvaluationPort evaluates one input. Implementations return an error, not a
// negative Decision, when they cannot decide. A later remote evaluator can run
// behind this port (for example as a Temporal activity) without changing how
// Monitoring commits Matches.
type EvaluationPort interface {
	Evaluate(ctx context.Context, in EvaluationInput) (Evaluation, error)
}

// Fixture is the deterministic test evaluator for notification mechanics; it
// is not a relevance algorithm. Its pinned configuration is
// {"decisions": {"<marker>": "<decision>", ..., "default": "<decision>"}}.
// Markers are literal substrings of Part text, checked in sorted order; the
// first present marker decides, otherwise "default" (absent: no_match).
// Decisions are match, no_match and not_ready, plus two fixture-only,
// test-oriented values: "error" fails the evaluation, and
// "match_after_enrichment" is not_ready until the Version is enriched.
type Fixture struct{}

func (Fixture) Evaluate(_ context.Context, in EvaluationInput) (Evaluation, error) {
	raw, ok := in.Evaluator.Configuration["decisions"]
	if !ok {
		raw = map[string]any{}
	}
	decisions, ok := raw.(map[string]any)
	if !ok {
		return Evaluation{}, fmt.Errorf("%w: decisions must be an object", ErrEvaluatorConfiguration)
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
		for _, p := range in.Parts {
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
		return Evaluation{}, fmt.Errorf("%w: fixture error decision", ErrEvaluation)
	case "match_after_enrichment":
		if !in.Enriched {
			decision = string(DecisionNotReady)
		} else {
			decision = string(DecisionMatch)
		}
	case string(DecisionMatch), string(DecisionNoMatch), string(DecisionNotReady):
	default:
		return Evaluation{}, fmt.Errorf("%w: unknown decision", ErrEvaluatorConfiguration)
	}
	return Evaluation{
		Decision:    Decision(decision),
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
