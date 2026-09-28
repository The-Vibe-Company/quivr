package monitoring_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
)

// fakeEvaluation records what the engine asked the store to do; the real
// claim/commit semantics are proven against PostgreSQL.
type fakeEvaluation struct {
	intents   []monitoring.Intent
	target    monitoring.Target
	completed []string
	retried   []string
	committed []monitoring.MatchEvidence
}

func (f *fakeEvaluation) FanOut(context.Context) (int, error) { return 0, nil }
func (f *fakeEvaluation) Claim(context.Context, time.Duration) (monitoring.Intent, error) {
	if len(f.intents) == 0 {
		return monitoring.Intent{}, monitoring.ErrNoWork
	}
	in := f.intents[0]
	f.intents = f.intents[1:]
	return in, nil
}
func (f *fakeEvaluation) Target(context.Context, monitoring.Intent) (monitoring.Target, error) {
	return f.target, nil
}
func (f *fakeEvaluation) Complete(_ context.Context, _ monitoring.Intent, outcome string) error {
	f.completed = append(f.completed, outcome)
	return nil
}
func (f *fakeEvaluation) Retry(_ context.Context, _ monitoring.Intent, code string, delay time.Duration) error {
	if delay <= 0 || delay > 5*time.Minute {
		return errors.New("unbounded delay")
	}
	f.retried = append(f.retried, code)
	return nil
}
func (f *fakeEvaluation) CommitMatch(_ context.Context, _ monitoring.Intent, ev monitoring.MatchEvidence) (string, error) {
	f.committed = append(f.committed, ev)
	return monitoring.OutcomeMatched, nil
}
func (f *fakeEvaluation) Backlog(context.Context) (monitoring.Backlog, error) {
	return monitoring.Backlog{}, nil
}

type parts []monitoring.Part

func (p parts) Parts(context.Context, string, string, string, string) ([]monitoring.Part, error) {
	return p, nil
}

type badEvaluator struct{ out monitoring.Evaluation }

func (b badEvaluator) Evaluate(context.Context, monitoring.EvaluationInput) (monitoring.Evaluation, error) {
	return b.out, nil
}

func engineFor(store *fakeEvaluation, decisions map[string]any, enabled bool) monitoring.Engine {
	store.intents = []monitoring.Intent{{Organization: "org", SubscriptionID: "sub", SubscriptionVersionID: "subv", Sequence: 7, CorpusID: "c", RecordID: "r", VersionID: "v"}}
	store.target = monitoring.Target{Enabled: enabled, Subscription: monitoring.SubscriptionVersion{Evaluator: monitoring.Evaluator{PluginID: monitoring.FixtureEvaluator, Version: monitoring.FixtureEvaluatorVersion, Configuration: map[string]any{"decisions": decisions}}}}
	return monitoring.Engine{Store: store, Versions: parts{{Key: "body", Role: "body", Text: "Dépêche ALERTE PANNE"}}, Evaluators: map[string]monitoring.EvaluationPort{monitoring.FixtureEvaluator + "@" + monitoring.FixtureEvaluatorVersion: monitoring.Fixture{}}}
}

func TestEngineStepOutcomes(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		decisions map[string]any
		enabled   bool
		completed string
		retried   string
		committed bool
	}{
		{"match commits", map[string]any{"ALERTE": "match"}, true, "", "", true},
		{"no_match completes", map[string]any{"default": "no_match"}, true, monitoring.OutcomeNoMatch, "", false},
		{"not_ready completes without a Match", map[string]any{"ALERTE": "not_ready"}, true, monitoring.OutcomeNotReady, "", false},
		{"error retries and is never negative", map[string]any{"ALERTE": "error"}, true, "", "evaluator_error", false},
		{"invalid configuration retries", map[string]any{"ALERTE": "perhaps"}, true, "", "evaluator_configuration_invalid", false},
		{"disabled Subscription stops", map[string]any{"ALERTE": "match"}, false, monitoring.OutcomeSubscriptionDisabled, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &fakeEvaluation{}
			progressed, err := engineFor(store, c.decisions, c.enabled).Step(ctx)
			if err != nil || !progressed {
				t.Fatal(progressed, err)
			}
			if (c.completed != "") != (len(store.completed) == 1) || (c.completed != "" && store.completed[0] != c.completed) {
				t.Fatalf("completed %v", store.completed)
			}
			if (c.retried != "") != (len(store.retried) == 1) || (c.retried != "" && store.retried[0] != c.retried) {
				t.Fatalf("retried %v", store.retried)
			}
			if c.committed != (len(store.committed) == 1) {
				t.Fatalf("committed %v", store.committed)
			}
		})
	}
	store := &fakeEvaluation{}
	engine := engineFor(store, nil, true)
	if progressed, err := engine.Step(ctx); err != nil || !progressed {
		t.Fatal(err)
	}
	if progressed, err := engine.Step(ctx); err != nil || progressed {
		t.Fatal("idle step progressed", err)
	}
}

func TestEngineRejectsUnboundedEvidence(t *testing.T) {
	for name, out := range map[string]monitoring.Evaluation{
		"unknown part":      {Decision: monitoring.DecisionMatch, Explanation: "x", PartKeys: []string{"missing"}},
		"empty explanation": {Decision: monitoring.DecisionMatch},
		"unknown decision":  {Decision: "maybe", Explanation: "x"},
	} {
		t.Run(name, func(t *testing.T) {
			store := &fakeEvaluation{}
			engine := engineFor(store, nil, true)
			engine.Evaluators = map[string]monitoring.EvaluationPort{monitoring.FixtureEvaluator + "@" + monitoring.FixtureEvaluatorVersion: badEvaluator{out}}
			if _, err := engine.Step(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(store.committed) != 0 || len(store.retried) != 1 || store.retried[0] != "evaluation_invalid" {
				t.Fatalf("%+v", store)
			}
		})
	}
}

type matchStore struct {
	match    monitoring.Match
	delivery monitoring.Delivery
}

func (m matchStore) Match(_ context.Context, _, id string) (monitoring.Match, error) {
	if id != m.match.ID {
		return monitoring.Match{}, monitoring.ErrNotFound
	}
	return m.match, nil
}
func (m matchStore) Matches(context.Context, string, string, int64, int) ([]monitoring.Match, error) {
	return []monitoring.Match{m.match}, nil
}
func (m matchStore) Delivery(_ context.Context, _, id string) (monitoring.Delivery, error) {
	if id != m.delivery.ID {
		return monitoring.Delivery{}, monitoring.ErrNotFound
	}
	return m.delivery, nil
}

func (m matchStore) Attempts(_ context.Context, _, id string, after, limit int) ([]monitoring.Attempt, error) {
	return []monitoring.Attempt{{ID: "a1", DeliveryID: id, Number: 1, Outcome: monitoring.AttemptAcknowledged}}, nil
}

func TestMatchReadsRequireReadScopeAndVisibleSubscription(t *testing.T) {
	ctx := context.Background()
	store := newStore()
	store.subscriptions["org/sub"] = monitoring.Subscription{ID: "sub", Enabled: true, Current: monitoring.SubscriptionVersion{SubscriptionID: "sub", CorpusIDs: []string{"a", "b"}}}
	service := monitoring.Service{Store: store, MatchStore: matchStore{match: monitoring.Match{ID: "m", SubscriptionID: "sub"}, delivery: monitoring.Delivery{ID: "d", SubscriptionID: "sub"}}}
	full := corpus.Scope{Organization: "org", Actions: []string{"monitoring:read"}, Corpora: []string{"a", "b"}}
	if _, err := service.Match(ctx, full, "m"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Delivery(ctx, full, "d"); err != nil {
		t.Fatal(err)
	}
	// A Delivery whose destination is no longer configured is not admissible.
	allowed := service
	allowed.MatchStore = matchStore{match: monitoring.Match{ID: "m", SubscriptionID: "sub"}, delivery: monitoring.Delivery{ID: "d", SubscriptionID: "sub", DestinationID: "gone", Admission: monitoring.Admission{Allowed: true}}}
	if d, err := allowed.Delivery(ctx, full, "d"); err != nil || d.Admission.Allowed || d.Admission.Reason != "destination_unavailable" {
		t.Fatal("unconfigured destination admission", d.Admission, err)
	}
	allowed.Destinations = map[string]monitoring.Destination{"gone": {Organization: "org"}}
	if d, err := allowed.Delivery(ctx, full, "d"); err != nil || !d.Admission.Allowed {
		t.Fatal("configured destination admission", d.Admission, err)
	}
	if items, err := service.Matches(ctx, full, "sub", 0, 10); err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	if items, err := service.Attempts(ctx, full, "d", 0, 10); err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	if _, err := service.Attempts(ctx, full, "missing", 0, 10); !errors.Is(err, monitoring.ErrNotFound) {
		t.Fatal("attempts of a missing Delivery", err)
	}
	partial := corpus.Scope{Organization: "org", Actions: []string{"monitoring:read"}, Corpora: []string{"a"}}
	other := corpus.Scope{Organization: "other", Actions: []string{"monitoring:read"}, Corpora: []string{"*"}}
	for _, s := range []corpus.Scope{partial, other} {
		if _, err := service.Match(ctx, s, "m"); !errors.Is(err, monitoring.ErrNotFound) {
			t.Fatal("match visible", err)
		}
		if _, err := service.Delivery(ctx, s, "d"); !errors.Is(err, monitoring.ErrNotFound) {
			t.Fatal("delivery visible", err)
		}
		if _, err := service.Matches(ctx, s, "sub", 0, 10); !errors.Is(err, monitoring.ErrNotFound) {
			t.Fatal("list visible", err)
		}
		if _, err := service.Attempts(ctx, s, "d", 0, 10); !errors.Is(err, monitoring.ErrNotFound) {
			t.Fatal("attempts visible", err)
		}
	}
	writeOnly := corpus.Scope{Organization: "org", Actions: []string{"monitoring:write"}, Corpora: []string{"*"}}
	if _, err := service.Match(ctx, writeOnly, "m"); !errors.Is(err, monitoring.ErrForbidden) {
		t.Fatal(err)
	}
}
