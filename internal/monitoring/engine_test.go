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
	intents []monitoring.Intent
	// related is what ClaimRelated leases, once.
	related   []monitoring.Intent
	target    monitoring.Target
	targets   map[string]monitoring.Target
	completed []string
	retried   []string
	committed []monitoring.MatchEvidence
	// matched and negatives name the Subscription Versions committed.
	matched   []string
	negatives []string
	negative  int
	withdrawn int
	targeted  int
	failing   bool
	limit     int
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
func (f *fakeEvaluation) ClaimRelated(_ context.Context, _ monitoring.Intent, _ monitoring.Evaluator, limit int, _ time.Duration) ([]monitoring.Intent, error) {
	f.limit = limit
	related := f.related
	if len(related) > limit {
		related = related[:limit]
	}
	f.related = nil
	return related, nil
}
func (f *fakeEvaluation) Target(_ context.Context, in monitoring.Intent) (monitoring.Target, error) {
	f.targeted++
	if t, ok := f.targets[in.SubscriptionVersionID]; ok {
		return t, nil
	}
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
func (f *fakeEvaluation) CommitMatch(_ context.Context, in monitoring.Intent, ev monitoring.MatchEvidence) (string, error) {
	f.committed = append(f.committed, ev)
	f.matched = append(f.matched, in.SubscriptionVersionID)
	return monitoring.OutcomeMatched, nil
}
func (f *fakeEvaluation) CommitNoMatch(_ context.Context, in monitoring.Intent) (string, error) {
	f.negative++
	f.negatives = append(f.negatives, in.SubscriptionVersionID)
	return monitoring.OutcomeNoLongerMatches, nil
}
func (f *fakeEvaluation) CommitWithdrawal(context.Context, monitoring.Intent) (string, error) {
	if f.failing {
		return "", errors.New("storage down")
	}
	f.withdrawn++
	return monitoring.OutcomeWithdrawalNotified, nil
}
func (f *fakeEvaluation) Backlog(context.Context) (monitoring.Backlog, error) {
	return monitoring.Backlog{}, nil
}

type parts []monitoring.Part

func (p parts) Article(context.Context, string, string, string, string) (monitoring.Article, error) {
	return monitoring.Article{Parts: p}, nil
}

// badEvaluator answers every item with the same evaluation.
type badEvaluator struct{ out monitoring.Evaluation }

func (badEvaluator) MaxBatch() int                                 { return 8 }
func (badEvaluator) Validate(map[string]any, map[string]any) error { return nil }
func (b badEvaluator) Evaluate(_ context.Context, batch monitoring.Batch) ([]monitoring.Outcome, error) {
	out := make([]monitoring.Outcome, len(batch.Items))
	for i := range out {
		out[i] = monitoring.Outcome{Evaluation: b.out}
	}
	return out, nil
}

func engineFor(store *fakeEvaluation, decisions map[string]any, enabled bool) monitoring.Engine {
	store.intents = []monitoring.Intent{{Organization: "org", SubscriptionID: "sub", SubscriptionVersionID: "subv", Sequence: 7, CorpusID: "c", RecordID: "r", VersionID: "v"}}
	store.target = monitoring.Target{Enabled: enabled, Subscription: monitoring.SubscriptionVersion{Evaluator: monitoring.Evaluator{PluginID: monitoring.FixtureEvaluator, Version: monitoring.FixtureEvaluatorVersion, Configuration: map[string]any{"decisions": decisions}}}}
	return monitoring.Engine{Store: store, Versions: parts{{Key: "body", Role: "body", Text: "Dépêche ALERTE PANNE"}}, Evaluators: monitoring.FixtureEvaluators()}
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
		negative  bool
	}{
		{"match commits", map[string]any{"ALERTE": "match"}, true, "", "", true, false},
		// Only a completed negative decision reaches the negative commit, which
		// decides under the lock whether it invalidates a prior positive Match.
		{"no_match commits a negative decision", map[string]any{"default": "no_match"}, true, "", "", false, true},
		{"not_ready completes without a Match", map[string]any{"ALERTE": "not_ready"}, true, monitoring.OutcomeNotReady, "", false, false},
		{"error retries and is never negative", map[string]any{"ALERTE": "error"}, true, "", "evaluator_error", false, false},
		{"invalid configuration retries", map[string]any{"ALERTE": "perhaps"}, true, "", "evaluator_configuration_invalid", false, false},
		{"disabled Subscription stops", map[string]any{"ALERTE": "match"}, false, monitoring.OutcomeSubscriptionDisabled, "", false, false},
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
			if c.negative != (store.negative == 1) || store.withdrawn != 0 {
				t.Fatalf("negative %d withdrawn %d", store.negative, store.withdrawn)
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

// TestEngineWithdrawalIntents proves withdrawal work never reaches an
// evaluator or the evaluation target, and a storage failure keeps it pending.
func TestEngineWithdrawalIntents(t *testing.T) {
	ctx := context.Background()
	for _, failing := range []bool{false, true} {
		store := &fakeEvaluation{failing: failing}
		engine := engineFor(store, map[string]any{"ALERTE": "error"}, true)
		store.intents[0].Kind = monitoring.IntentWithdrawal
		if progressed, err := engine.Step(ctx); err != nil || !progressed {
			t.Fatal(progressed, err)
		}
		if store.targeted != 0 || len(store.committed) != 0 || store.negative != 0 || len(store.completed) != 0 {
			t.Fatalf("withdrawal ran evaluation: %+v", store)
		}
		if failing && (len(store.retried) != 1 || store.retried[0] != "storage_unavailable") {
			t.Fatalf("failed withdrawal commit must stay pending: %v", store.retried)
		}
		if !failing && (store.withdrawn != 1 || len(store.retried) != 0) {
			t.Fatalf("withdrawal commit: %+v", store)
		}
	}
}

// TestAdmissionReason pins which notice kinds each refusal applies to: a
// deleted or disabled Subscription refuses every kind;
// match.withdrawn has its own eligibility and is never superseded; a positive
// notice is superseded by any later correction notice, and
// match.no_longer_matches only by a later match.corrected.
func TestAdmissionReason(t *testing.T) {
	none := monitoring.Later{}
	all := monitoring.Later{Corrected: true, NoLongerMatches: true}
	kinds := []string{monitoring.NoticeCreated, monitoring.NoticeCorrected, monitoring.NoticeNoLongerMatches, monitoring.NoticeWithdrawn}
	for _, kind := range kinds {
		if got := monitoring.AdmissionReason(kind, true, false, false, none); got != "" {
			t.Fatal(kind, got)
		}
		if got := monitoring.AdmissionReason(kind, false, false, true, all); got != "subscription_disabled" {
			t.Fatal(kind, got)
		}
		// A deleted Subscription (also disabled) refuses every notice for good.
		if got := monitoring.AdmissionReason(kind, false, true, true, all); got != "subscription_deleted" {
			t.Fatal(kind, got)
		}
		want := "record_withdrawn"
		if kind == monitoring.NoticeWithdrawn {
			want = ""
		}
		if got := monitoring.AdmissionReason(kind, true, false, true, none); got != want {
			t.Fatal(kind, got)
		}
	}
	superseded := func(yes bool) string {
		if yes {
			return "superseded"
		}
		return ""
	}
	for _, kind := range kinds {
		positive := kind == monitoring.NoticeCreated || kind == monitoring.NoticeCorrected
		for later, want := range map[monitoring.Later]string{
			{Corrected: true}:       superseded(positive || kind == monitoring.NoticeNoLongerMatches),
			{NoLongerMatches: true}: superseded(positive),
			all:                     superseded(kind != monitoring.NoticeWithdrawn),
		} {
			if got := monitoring.AdmissionReason(kind, true, false, false, later); got != want {
				t.Fatalf("%s after %+v: %q, want %q", kind, later, got, want)
			}
		}
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
			engine.Evaluators = monitoring.Evaluators{monitoring.FixtureEvaluator + "@" + monitoring.FixtureEvaluatorVersion: badEvaluator{out}}
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
	service := monitoring.Service{Store: store, Evaluators: monitoring.FixtureEvaluators(), MatchStore: matchStore{match: monitoring.Match{ID: "m", SubscriptionID: "sub"}, delivery: monitoring.Delivery{ID: "d", SubscriptionID: "sub"}}}
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
