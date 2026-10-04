package monitoring_test

import (
	"bytes"
	"context"
	"fmt"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost/fakeplugin"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
)

// phraseEvaluator stands for a pinned subscription plugin: it matches when
// the expression text appears in a Part, and records every call it answers.
type phraseEvaluator struct {
	max int
	// tooLarge refuses a request with more items than this before sending (0: never).
	tooLarge int
	err      error
	// poison fails every call carrying this expression text.
	poison string
	// failAll fails every call with this error.
	failAll error
	mu      sync.Mutex
	calls   []monitoring.Batch
}

func (p *phraseEvaluator) MaxBatch() int                                 { return p.max }
func (p *phraseEvaluator) Validate(map[string]any, map[string]any) error { return nil }
func (p *phraseEvaluator) Evaluate(_ context.Context, b monitoring.Batch) ([]monitoring.Outcome, error) {
	if p.tooLarge > 0 && len(b.Items) > p.tooLarge {
		return nil, monitoring.ErrRequestTooLarge
	}
	p.mu.Lock()
	p.calls = append(p.calls, b)
	p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	if p.failAll != nil {
		return nil, p.failAll
	}
	for _, item := range b.Items {
		if p.poison != "" && item.Expression["text"] == p.poison {
			return nil, fmt.Errorf("%w: backend refused %s", monitoring.ErrEvaluation, p.poison)
		}
	}
	out := make([]monitoring.Outcome, len(b.Items))
	for i, item := range b.Items {
		text, _ := item.Expression["text"].(string)
		out[i] = monitoring.Outcome{Evaluation: monitoring.Evaluation{Decision: monitoring.DecisionNoMatch}}
		for _, part := range b.Article.Parts {
			if strings.Contains(part.Text, text) {
				out[i] = monitoring.Outcome{Evaluation: monitoring.Evaluation{Decision: monitoring.DecisionMatch, Explanation: text + " appears", PartKeys: []string{part.Key}}}
			}
		}
	}
	return out, nil
}

const alertsKey = "acme.alerts@0.1.0"

// batchScenario queues one claimed intent per Subscription (all on the same
// Record Version), each pinning the expression text of the same index.
func batchScenario(evaluator *phraseEvaluator, texts ...string) (*fakeEvaluation, monitoring.Engine, *monitoring.EvaluationMetrics) {
	store := &fakeEvaluation{targets: map[string]monitoring.Target{}}
	var intents []monitoring.Intent
	for i, text := range texts {
		sv := fmt.Sprintf("subv-%d", i)
		intents = append(intents, monitoring.Intent{Organization: "org", SubscriptionID: fmt.Sprintf("sub-%d", i), SubscriptionVersionID: sv, Sequence: 9, CorpusID: "c", RecordID: "r", VersionID: "v"})
		store.targets[sv] = monitoring.Target{Enabled: true, Definition: monitoring.Definition{Expression: map[string]any{"text": text}},
			Subscription: monitoring.SubscriptionVersion{Owner: fmt.Sprintf("user-%d", i), SavedQueryID: "q-" + text, SavedQueryVersionID: "qv-" + text, Evaluator: monitoring.Evaluator{PluginID: "acme.alerts", Version: "0.1.0", Configuration: map[string]any{}}}}
	}
	store.intents, store.related = intents[:1], intents[1:]
	metrics := &monitoring.EvaluationMetrics{}
	engine := monitoring.Engine{Store: store, Versions: parts{{Key: "title", Role: "title", Text: "Harbour strike"}, {Key: "body", Role: "body", Text: "Dock workers vote"}},
		Evaluators: monitoring.Evaluators{alertsKey: evaluator}, Metrics: metrics}
	return store, engine, metrics
}

func sorted(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

// One call decides every due Subscription of a Record Version: identical
// expressions and configurations are sent once and their decision fans out.
func TestEngineBatchesAndDeduplicatesPerRecordVersion(t *testing.T) {
	evaluator := &phraseEvaluator{max: 32}
	store, engine, metrics := batchScenario(evaluator, "strike", "strike", "election", "strike", "election")
	if progressed, err := engine.Step(context.Background()); !progressed || err != nil {
		t.Fatal(progressed, err)
	}
	if len(evaluator.calls) != 1 {
		t.Fatalf("want one call, got %d", len(evaluator.calls))
	}
	call := evaluator.calls[0]
	if len(call.Items) != 2 || call.RecordID != "r" || call.VersionID != "v" || len(call.Article.Parts) != 2 {
		t.Fatalf("batch %+v", call)
	}
	subscriptions := map[string][]string{}
	for _, item := range call.Items {
		for _, ref := range item.Subscriptions {
			subscriptions[item.Expression["text"].(string)] = append(subscriptions[item.Expression["text"].(string)], ref.SubscriptionVersionID)
		}
	}
	if got := sorted(subscriptions["strike"]); fmt.Sprint(got) != "[subv-0 subv-1 subv-3]" {
		t.Fatalf("strike stands for %v", got)
	}
	// Each Subscription keeps its owner in the batch, whoever shares the evaluation.
	for _, item := range call.Items {
		for _, ref := range item.Subscriptions {
			if ref.Owner != "user-"+strings.TrimPrefix(ref.SubscriptionVersionID, "subv-") {
				t.Fatalf("owner %q for %s", ref.Owner, ref.SubscriptionVersionID)
			}
		}
	}
	if got := sorted(store.matched); fmt.Sprint(got) != "[subv-0 subv-1 subv-3]" {
		t.Fatalf("matched %v", got)
	}
	if got := sorted(store.negatives); fmt.Sprint(got) != "[subv-2 subv-4]" {
		t.Fatalf("negatives %v", got)
	}
	for _, ev := range store.committed {
		if ev.Evaluator.PluginID != "acme.alerts" || ev.Explanation != "strike appears" || len(ev.PartKeys) != 1 || ev.PartKeys[0] != "title" {
			t.Fatalf("evidence %+v", ev)
		}
	}
	var out bytes.Buffer
	metrics.Write(&out)
	for _, want := range []string{
		`quivr_evaluation_calls_per_record_version_bucket{le="1"} 1`,
		`quivr_evaluation_expressions_per_call_sum 2`,
		`quivr_evaluation_subscriptions_per_call_sum 5`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("metrics lack %q:\n%s", want, out.String())
		}
	}
}

// A batch larger than the plugin's max_batch_size is split into several calls.
func TestEngineSplitsAtTheEvaluatorBatchSize(t *testing.T) {
	evaluator := &phraseEvaluator{max: 2}
	store, engine, _ := batchScenario(evaluator, "q1", "q2", "q3", "q4", "q5")
	if _, err := engine.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.limit != 7 {
		t.Fatalf("related claim limit %d, want 4×2-1", store.limit)
	}
	sizes := []int{}
	for _, c := range evaluator.calls {
		sizes = append(sizes, len(c.Items))
	}
	sort.Ints(sizes)
	if fmt.Sprint(sizes) != "[1 2 2]" || len(store.negatives) != 5 {
		t.Fatalf("calls %v negatives %v", sizes, store.negatives)
	}
}

// A request over the protocol bound is halved until it fits; the refused
// request is not counted as a call.
func TestEngineHalvesOversizeRequests(t *testing.T) {
	evaluator := &phraseEvaluator{max: 8, tooLarge: 1}
	store, engine, metrics := batchScenario(evaluator, "q1", "q2", "q3", "strike")
	if _, err := engine.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(evaluator.calls) != 4 || len(store.matched) != 1 || len(store.negatives) != 3 {
		t.Fatalf("calls %d matched %v negatives %v", len(evaluator.calls), store.matched, store.negatives)
	}
	var out bytes.Buffer
	metrics.Write(&out)
	if !strings.Contains(out.String(), "quivr_evaluation_calls_per_record_version_sum 4") {
		t.Fatalf("calls metric:\n%s", out.String())
	}
}

// When the plugin is unavailable every intent of the batch stays pending with
// evaluator_unavailable; nothing becomes a negative decision.
func TestEngineUnavailablePluginRetriesTheWholeBatch(t *testing.T) {
	evaluator := &phraseEvaluator{max: 32, err: fmt.Errorf("%w: connection refused", monitoring.ErrEvaluatorUnavailable)}
	store, engine, _ := batchScenario(evaluator, "strike", "election", "strike")
	if _, err := engine.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(store.retried) != "[evaluator_unavailable evaluator_unavailable evaluator_unavailable]" || store.negative != 0 || len(store.committed) != 0 || len(store.completed) != 0 {
		t.Fatalf("%+v", store)
	}
	for code, err := range map[string]error{
		"evaluator_error":    monitoring.ErrEvaluation,
		"evaluation_invalid": monitoring.ErrEvaluationInvalid,
	} {
		evaluator := &phraseEvaluator{max: 32, err: err}
		store, engine, _ := batchScenario(evaluator, "strike")
		if _, err := engine.Step(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(store.retried) != 1 || store.retried[0] != code || store.negative != 0 {
			t.Fatalf("%s: %+v", code, store)
		}
	}
}

// A per-item failure retries only the intents of that item; the other
// decisions of the same batch commit.
func TestEngineItemFailureRetriesOnlyItsIntents(t *testing.T) {
	store := &fakeEvaluation{targets: map[string]monitoring.Target{}}
	fixture := func(decisions map[string]any) monitoring.Target {
		return monitoring.Target{Enabled: true, Subscription: monitoring.SubscriptionVersion{Evaluator: monitoring.Evaluator{PluginID: fakeplugin.FixtureEvaluator, Version: fakeplugin.FixtureEvaluatorVersion, Configuration: map[string]any{"decisions": decisions}}}}
	}
	store.targets["ok"] = fixture(map[string]any{"ALERTE": "match"})
	store.targets["fault"] = fixture(map[string]any{"ALERTE": "error"})
	store.targets["off"] = monitoring.Target{Enabled: false}
	intent := func(sv string) monitoring.Intent {
		return monitoring.Intent{Organization: "org", SubscriptionID: sv, SubscriptionVersionID: sv, CorpusID: "c", RecordID: "r", VersionID: "v"}
	}
	store.intents = []monitoring.Intent{intent("ok")}
	store.related = []monitoring.Intent{intent("fault"), intent("off")}
	engine := monitoring.Engine{Store: store, Versions: parts{{Key: "body", Role: "body", Text: "ALERTE"}}, Evaluators: fakeplugin.FixtureEvaluators()}
	if _, err := engine.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(store.matched) != "[ok]" || fmt.Sprint(store.retried) != "[evaluator_error]" || fmt.Sprint(store.completed) != "["+monitoring.OutcomeSubscriptionDisabled+"]" {
		t.Fatalf("%+v", store)
	}
}

// One evaluation the plugin cannot decide fails its whole call; the engine
// halves the call so only that evaluation's Subscriptions stay pending.
func TestEnginePluginErrorIsolatesTheFailingEvaluation(t *testing.T) {
	evaluator := &phraseEvaluator{max: 8, poison: "boom"}
	store, engine, _ := batchScenario(evaluator, "strike", "boom", "q3", "strike")
	if _, err := engine.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(store.retried) != "[evaluator_error]" || fmt.Sprint(sorted(store.matched)) != "[subv-0 subv-3]" || fmt.Sprint(store.negatives) != "[subv-2]" {
		t.Fatalf("retried %v matched %v negatives %v", store.retried, store.matched, store.negatives)
	}
}

// An error that fails every evaluation is not split without bound: a
// retryable plugin error (a backend down) is retried as one batch, and a
// terminal one spends a bounded number of calls.
func TestEngineErrorOnEveryCallIsBounded(t *testing.T) {
	texts := make([]string, 32)
	for i := range texts {
		texts[i] = fmt.Sprintf("q%d", i)
	}
	retryable := &phraseEvaluator{max: 32, failAll: fmt.Errorf("%w: %w: backend down", monitoring.ErrEvaluation, monitoring.ErrEvaluationRetryable)}
	store, engine, _ := batchScenario(retryable, texts...)
	if _, err := engine.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(retryable.calls) != 1 || len(store.retried) != 32 || store.negative != 0 {
		t.Fatalf("retryable: %d calls, %d retried", len(retryable.calls), len(store.retried))
	}
	terminal := &phraseEvaluator{max: 32, failAll: fmt.Errorf("%w: cannot decide", monitoring.ErrEvaluation)}
	store, engine, _ = batchScenario(terminal, texts...)
	if _, err := engine.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(terminal.calls) > 20 || len(store.retried) != 32 || store.negative != 0 {
		t.Fatalf("terminal: %d calls, %d retried", len(terminal.calls), len(store.retried))
	}
}

// An intent whose Subscription Version already decided the Record Version
// (the enrichment trigger after the searchable one) completes as a duplicate
// and is never sent to the evaluator.
func TestEngineDoesNotAskADecidedPairAgain(t *testing.T) {
	evaluator := &phraseEvaluator{max: 32}
	store, engine, _ := batchScenario(evaluator, "strike", "election")
	decided := store.targets["subv-1"]
	decided.Decided = true
	store.targets["subv-1"] = decided
	if progressed, err := engine.Step(context.Background()); !progressed || err != nil {
		t.Fatal(progressed, err)
	}
	if len(evaluator.calls) != 1 || len(evaluator.calls[0].Items) != 1 || evaluator.calls[0].Items[0].Expression["text"] != "strike" {
		t.Fatalf("the decided pair was sent again: %+v", evaluator.calls)
	}
	if fmt.Sprint(store.completed) != "[duplicate]" {
		t.Fatalf("completed %v", store.completed)
	}
}
