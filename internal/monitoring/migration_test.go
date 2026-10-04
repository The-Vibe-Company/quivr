package monitoring_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
)

// PinningSubscriptions lists, in ID order, the Organization's Subscriptions
// that are not deleted whose current Version pins pluginID@version.
func (m *memoryStore) PinningSubscriptions(_ context.Context, org, pluginID, version string, _ []string, after string, limit int) ([]monitoring.Subscription, error) {
	var out []monitoring.Subscription
	for key, s := range m.subscriptions {
		if e := s.Current.Evaluator; strings.HasPrefix(key, org+"/") && !s.Deleted && e.PluginID == pluginID && e.Version == version && s.ID > after {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out[:min(limit, len(out))], nil
}

// MoveEvaluator makes current a copy of from that pins evaluator.
func (m *memoryStore) MoveEvaluator(_ context.Context, org string, from monitoring.SubscriptionVersion, evaluator monitoring.Evaluator) (monitoring.SubscriptionVersion, error) {
	m.writes = append(m.writes, "subscription move "+from.SubscriptionID)
	s := m.subscriptions[org+"/"+from.SubscriptionID]
	if s.Current.VersionID != from.VersionID {
		return monitoring.SubscriptionVersion{}, monitoring.ErrConflict
	}
	s.Current = from
	s.Current.VersionID, s.Current.Evaluator = from.VersionID+"_moved", evaluator
	m.subscriptions[org+"/"+s.ID] = s
	m.versions[org+"/"+s.ID+"/"+s.Current.VersionID] = s.Current
	return s.Current, nil
}

// strictEvaluator stands for a later version of schemaEvaluator's plugin
// whose expression schema also refuses a "legacy" member.
type strictEvaluator struct{ schemaEvaluator }

func (e strictEvaluator) Validate(expression, configuration map[string]any) error {
	if _, ok := expression["legacy"]; ok {
		return monitoring.Invalid(monitoring.ErrInvalidExpression, "/saved_query_version_id", "/expression: additional property 'legacy' not allowed")
	}
	return e.schemaEvaluator.Validate(expression, configuration)
}

var operator = corpus.Scope{Organization: "org_a", Actions: []string{monitoring.MigrationAction}, Corpora: []string{"*"}}

// upgraded is an alert-rule plugin acme.alerts whose 0.1.0 served the
// Subscriptions created so far, until 0.2.0 was activated: 0.2.0 serves new
// Subscription Versions and 0.1.0 stays installed for those pinning it.
func upgraded(t *testing.T) (monitoring.Service, *memoryStore, map[string]monitoring.Subscription) {
	t.Helper()
	ctx := context.Background()
	s, store := service()
	s.Moves = store
	created := map[string]monitoring.Subscription{}
	for name, expression := range map[string]map[string]any{"current": {"text": "harbour"}, "legacy": {"text": "pier", "legacy": true}} {
		in := query(name, "corpus_a")
		in.Definition.Expression = expression
		q, err := s.CreateSavedQuery(ctx, writer, in)
		if err != nil {
			t.Fatal(err)
		}
		sub, err := s.CreateSubscription(ctx, writer, monitoring.SubscriptionInput{Key: name, Name: name, SavedQueryID: q.ID, SavedQueryVersionID: q.Current.VersionID, DestinationID: "receiver_a",
			Evaluator: monitoring.Evaluator{PluginID: "acme.alerts", Version: "0.1.0", Configuration: map[string]any{"case_sensitive": true}}})
		if err != nil {
			t.Fatal(err)
		}
		created[name] = sub
	}
	s.Evaluators = monitoring.PlanEvaluators{Served: monitoring.Evaluators{"acme.alerts@0.2.0": strictEvaluator{}}, Retained: monitoring.Evaluators{"acme.alerts@0.1.0": schemaEvaluator{}}}
	return s, store, created
}

// TestUpgradedAlertRuleServesNewSubscriptionsOnly owns which alert-rule
// version a Subscription Version may pin once another version is active: a
// new Subscription or an edit pins the served version, and an edit may keep
// the earlier version its Subscription already pins, so an alert never has
// to move on an edit; no Subscription moves to an earlier version.
func TestUpgradedAlertRuleServesNewSubscriptionsOnly(t *testing.T) {
	ctx := context.Background()
	s, _, created := upgraded(t)
	sub := created["current"]
	pin := func(version string) monitoring.Evaluator {
		return monitoring.Evaluator{PluginID: "acme.alerts", Version: version, Configuration: map[string]any{}}
	}
	fresh := monitoring.SubscriptionInput{Key: "fresh", Name: "fresh", SavedQueryID: sub.Current.SavedQueryID, SavedQueryVersionID: sub.Current.SavedQueryVersionID, DestinationID: "receiver_a", Evaluator: pin("0.1.0")}
	if _, err := s.CreateSubscription(ctx, writer, fresh); !errors.Is(err, monitoring.ErrUnsupportedEvaluator) {
		t.Fatalf("a new Subscription on the earlier version: %v, want unsupported_evaluator", err)
	}
	fresh.Evaluator = pin("0.2.0")
	onServed, err := s.CreateSubscription(ctx, writer, fresh)
	if err != nil {
		t.Fatalf("a new Subscription on the served version: %v", err)
	}
	keep := monitoring.SubscriptionVersionInput{Key: "keep", SavedQueryVersionID: sub.Current.SavedQueryVersionID, DestinationID: "receiver_a", Evaluator: pin("0.1.0")}
	if v, err := s.CreateSubscriptionVersion(ctx, writer, sub.ID, keep); err != nil || v.Evaluator.Version != "0.1.0" {
		t.Fatalf("an edit keeping the version its Subscription pins: %+v (%v)", v, err)
	}
	if _, err := s.CreateSubscriptionVersion(ctx, writer, onServed.ID, keep); !errors.Is(err, monitoring.ErrUnsupportedEvaluator) {
		t.Fatalf("an edit moving a Subscription back to the earlier version: %v, want unsupported_evaluator", err)
	}
}

// TestMigrationMovesSubscriptionsTheServedVersionAccepts owns the operator
// migration: a dry run reports and writes nothing; a migration moves to the
// served version, with the same Saved Query Version, configuration and
// destination, every Subscription whose expression that version accepts and
// refuses the others with the first issue; running it again moves nothing
// more. Calls page in Subscription id order.
func TestMigrationMovesSubscriptionsTheServedVersionAccepts(t *testing.T) {
	ctx := context.Background()
	s, store, created := upgraded(t)
	in := monitoring.EvaluatorMigrationInput{PluginID: "acme.alerts", FromVersion: "0.1.0", DryRun: true}
	ids := func(m monitoring.EvaluatorMigration) (moved, refused []string) {
		for _, x := range m.Moved {
			moved = append(moved, x.SubscriptionID+"@"+x.VersionID)
		}
		for _, x := range m.Refused {
			refused = append(refused, x.SubscriptionID+":"+x.Code+":"+x.Pointer)
		}
		return moved, refused
	}
	wantRefused := []string{created["legacy"].ID + ":invalid_expression:/saved_query_version_id"}

	writes := len(store.writes)
	dry, err := s.MigrateEvaluator(ctx, operator, in)
	if moved, refused := ids(dry); err != nil || dry.ToVersion != "0.2.0" || !reflect.DeepEqual(moved, []string{created["current"].ID + "@"}) || !reflect.DeepEqual(refused, wantRefused) || len(store.writes) != writes {
		t.Fatalf("dry run: %+v (%v), %d writes; want current listed, legacy refused, nothing written", dry, err, len(store.writes)-writes)
	}

	in.DryRun = false
	done, err := s.MigrateEvaluator(ctx, operator, in)
	moved, refused := ids(done)
	if err != nil || len(moved) != 1 || !reflect.DeepEqual(refused, wantRefused) {
		t.Fatalf("migration: %+v (%v)", done, err)
	}
	before, after := created["current"].Current, store.subscriptions["org_a/"+created["current"].ID].Current
	if after.VersionID != done.Moved[0].VersionID || after.Evaluator.Version != "0.2.0" || after.SavedQueryVersionID != before.SavedQueryVersionID ||
		after.DestinationID != before.DestinationID || !reflect.DeepEqual(after.Evaluator.Configuration, before.Evaluator.Configuration) {
		t.Fatalf("moved Subscription: %+v, from %+v", after, before)
	}
	if legacy := store.subscriptions["org_a/"+created["legacy"].ID].Current; legacy.Evaluator.Version != "0.1.0" {
		t.Fatalf("a refused Subscription moved: %+v", legacy)
	}
	if again, err := s.MigrateEvaluator(ctx, operator, in); err != nil || len(again.Moved) != 0 || len(again.Refused) != 1 {
		t.Fatalf("running the migration again: %+v (%v); want nothing more moved", again, err)
	}

	in.DryRun, in.Limit = true, 1
	if page, err := s.MigrateEvaluator(ctx, operator, in); err != nil || page.Next != created["legacy"].ID {
		t.Fatalf("a full page: %+v (%v); want next after its last Subscription", page, err)
	}

	for name, c := range map[string]struct {
		scope corpus.Scope
		in    monitoring.EvaluatorMigrationInput
		want  error
	}{
		"without plugins:admin": {writer, in, monitoring.ErrForbidden},
		"unknown plugin":        {operator, monitoring.EvaluatorMigrationInput{PluginID: "other.alerts", FromVersion: "0.1.0"}, monitoring.ErrUnsupportedEvaluator},
		"from the served one":   {operator, monitoring.EvaluatorMigrationInput{PluginID: "acme.alerts", FromVersion: "0.2.0"}, monitoring.ErrInvalidMigration},
	} {
		if _, err := s.MigrateEvaluator(ctx, c.scope, c.in); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
}
