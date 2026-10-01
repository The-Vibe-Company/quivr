package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/app"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
)

const alertsManifest = "../../../plugins/alerts/quivr-plugin.yaml"

// alertsVersion is plugins/alerts at another version, registered as a new
// build would be.
func alertsVersion(t *testing.T, version string) registry.Registration {
	t.Helper()
	raw, err := os.ReadFile(alertsManifest)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := plugins.LoadPinManifest([]byte(strings.Replace(string(raw), "version: 0.2.0", "version: "+version, 1)), version, plugins.PinConfig{Endpoint: "http://127.0.0.1:9971"})
	if err != nil {
		t.Fatal(err)
	}
	set, err := plugins.NewPinSet([]*plugins.Pin{pin})
	if err != nil {
		t.Fatal(err)
	}
	r := registry.FromPins(set).Registrations[0]
	r.State = registry.StateRegistered
	return r
}

// TestAlertRuleVersionDrainsUntilItsSubscriptionsMove owns the upgrade of an
// alert-rule plugin on real PostgreSQL (THE-805): once 0.3.0 is activated,
// 0.2.0 reads as draining with the Subscriptions whose current Version pins
// it, the pending evaluations of Versions pinning it and the earlier such
// Versions dispatch has not passed yet, and as inactive once none is left. Moving a Subscription records a new current Version that
// keeps the Saved Query Version it pinned, its destination and
// configuration, announces subscription.updated, replays to the same Version,
// and refuses a Version that is no longer current. Both versions stay listed
// as evaluators Subscription Versions may pin, the earlier plan's first.
func TestAlertRuleVersionDrainsUntilItsSubscriptionsMove(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	if err := app.BootstrapDatabase(ctx, pool, app.DeploymentSpaces(nil)); err != nil {
		t.Fatal(err)
	}
	registrations := postgres.PluginStore{Pool: pool}
	seed := configured(t, plugins.PinConfig{Manifest: alertsManifest, Endpoint: "http://127.0.0.1:9970"})
	if _, err := registrations.ApplyConfiguration(ctx, seed); err != nil {
		t.Fatal(err)
	}
	old, next := seed.Registrations[0], alertsVersion(t, "0.3.0")
	if _, _, err := registrations.RegisterPlugin(ctx, next, "register-0.3.0"); err != nil {
		t.Fatal(err)
	}
	if err := registrations.RecordCheck(ctx, next.ID, registry.CheckReport{Certified: true, Checks: []registry.CheckResult{}}); err != nil {
		t.Fatal(err)
	}

	// Subscriptions of an Organization created while 0.2.0 served.
	store := postgres.ContentStore{Pool: pool}
	scope := corpus.Scope{Organization: "org_a", Actions: []string{"corpora:write", "monitoring:read", "monitoring:write"}, Corpora: []string{"*"}}
	c, _, err := corpus.Service{Store: postgres.Store{Pool: pool}}.Create(ctx, scope, corpus.CreateInput{Key: "c", Name: "C"})
	if err != nil {
		t.Fatal(err)
	}
	service := monitoring.Service{Store: store, Corpora: store, Destinations: map[string]monitoring.Destination{"dest": {Organization: "org_a"}},
		Evaluators: monitoring.Evaluators{"alerts@0.2.0": monitoring.Fixture{}, "alerts@0.3.0": monitoring.Fixture{}}}
	definition := monitoring.Definition{CorpusIDs: []string{c.ID}, Expression: map[string]any{"kind": "keywords"}, RetrievalProfile: "default", TemporalPolicy: "from_activation"}
	q, err := service.CreateSavedQuery(ctx, scope, monitoring.SavedQueryInput{Key: "q", Name: "Q", Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	subscribe := func(key, version string) monitoring.Subscription {
		t.Helper()
		sub, err := service.CreateSubscription(ctx, scope, monitoring.SubscriptionInput{Key: key, Name: key, SavedQueryID: q.ID, SavedQueryVersionID: q.Current.VersionID, DestinationID: "dest",
			Evaluator: monitoring.Evaluator{PluginID: "alerts", Version: version, Configuration: map[string]any{"fields": map[string]any{}}}})
		if err != nil {
			t.Fatal(err)
		}
		return sub
	}
	moving, edited, gone := subscribe("moving", "0.2.0"), subscribe("edited", "0.2.0"), subscribe("gone", "0.2.0")
	if _, err = (registry.Service{Store: registrations, Spaces: app.DeploymentSpaces}).Activate(ctx, operatorScope, next.ID); err != nil {
		t.Fatalf("activating alerts 0.3.0: %v", err)
	}
	subscribe("new", "0.3.0")
	// A change committed before the move that 0.2.0 must still judge.
	if _, err = pool.Exec(ctx, `INSERT INTO evaluation_intents(organization,subscription_version_id,sequence,subscription_id,corpus_id,record_id,record_version_id) VALUES('org_a',$1,1,$2,$3,'record','version')`, moving.Current.VersionID, moving.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.DeleteSubscription(ctx, scope, "delete-gone", gone.ID); err != nil {
		t.Fatal(err)
	}
	usage := func(r registry.Registration) string {
		t.Helper()
		read, err := registrations.PluginRegistration(ctx, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%s subscriptions=%d pinned_work=%d", read.State, read.Subscriptions, read.PinnedWork)
	}
	if got, want := usage(old), "draining subscriptions=2 pinned_work=1"; got != want {
		t.Fatalf("0.2.0 after the upgrade: %s, want %s (moving and edited, not the deleted one; moving's pending evaluation)", got, want)
	}
	if got, want := usage(next), "active subscriptions=1 pinned_work=0"; got != want {
		t.Fatalf("0.3.0 after the upgrade: %s, want %s", got, want)
	}
	if listed, err := store.PinningSubscriptions(ctx, "org_a", "alerts", "0.2.0", nil, "", 10); err != nil || len(listed) != 2 {
		t.Fatalf("Subscriptions pinning 0.2.0: %d (%v), want moving and edited", len(listed), err)
	}

	// The Saved Query moved on since: the move keeps the Version it pinned.
	if _, err = service.CreateSavedQueryVersion(ctx, scope, q.ID, monitoring.SavedQueryVersionInput{Key: "q2", Definition: definition}); err != nil {
		t.Fatal(err)
	}
	target := monitoring.Evaluator{PluginID: "alerts", Version: "0.3.0", Configuration: moving.Current.Evaluator.Configuration}
	moved, err := store.MoveEvaluator(ctx, "org_a", moving.Current, target)
	if err != nil || moved.VersionID == moving.Current.VersionID || moved.SavedQueryVersionID != moving.Current.SavedQueryVersionID || moved.DestinationID != "dest" ||
		!reflect.DeepEqual(moved.Evaluator, target) || moved.ActivationPosition <= moving.Current.ActivationPosition {
		t.Fatalf("moved Version %+v (%v), from %+v", moved, err, moving.Current)
	}
	if current, _ := service.Subscription(ctx, scope, moving.ID); current.Current.VersionID != moved.VersionID {
		t.Fatalf("the moved Subscription's current Version is %s, want %s", current.Current.VersionID, moved.VersionID)
	}
	var updated int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM change_events WHERE organization='org_a' AND event_type='subscription.updated' AND resource_id=$1`, moving.ID).Scan(&updated); err != nil || updated != 1 {
		t.Fatalf("subscription.updated events for the move: %d (%v)", updated, err)
	}
	if replay, err := store.MoveEvaluator(ctx, "org_a", moving.Current, target); err != nil || replay.VersionID != moved.VersionID {
		t.Fatalf("a replayed move: %+v (%v), want the same Version", replay, err)
	}
	if _, err = store.MoveEvaluator(ctx, "org_a", gone.Current, target); !errors.Is(err, monitoring.ErrSubscriptionDeleted) {
		t.Fatalf("moving a deleted Subscription: %v, want ErrSubscriptionDeleted", err)
	}
	// An edit that keeps 0.2.0 lands between the listing and the move.
	q2, _ := service.SavedQuery(ctx, scope, q.ID)
	edit, err := service.CreateSubscriptionVersion(ctx, scope, edited.ID, monitoring.SubscriptionVersionInput{Key: "edit", SavedQueryVersionID: q2.Current.VersionID, DestinationID: "dest", Evaluator: edited.Current.Evaluator})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.MoveEvaluator(ctx, "org_a", edited.Current, target); !errors.Is(err, monitoring.ErrConflict) {
		t.Fatalf("moving a Version that is no longer current: %v, want ErrConflict", err)
	}
	if _, err = store.MoveEvaluator(ctx, "org_a", edit, target); err != nil {
		t.Fatal(err)
	}

	// Dispatch has not reached the moves yet: changes before them may still
	// be for the three earlier Versions pinning 0.2.0.
	if got, want := usage(old), "draining subscriptions=0 pinned_work=4"; got != want {
		t.Fatalf("0.2.0 once every Subscription moved: %s, want %s (the pending evaluation and three earlier Versions)", got, want)
	}
	for evaluation := (postgres.EvaluationStore{ContentStore: store}); ; {
		n, err := evaluation.FanOut(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	if got, want := usage(old), "draining subscriptions=0 pinned_work=1"; got != want {
		t.Fatalf("0.2.0 once dispatch passed the moves: %s, want %s", got, want)
	}
	if _, err = pool.Exec(ctx, `UPDATE evaluation_intents SET state='done',outcome='matched' WHERE organization='org_a'`); err != nil {
		t.Fatal(err)
	}
	if got, want := usage(old), "inactive subscriptions=0 pinned_work=0"; got != want {
		t.Fatalf("0.2.0 once its last evaluation completed: %s, want %s", got, want)
	}
	named, err := registrations.EvaluatorRegistrations(ctx)
	if err != nil || len(named) != 2 || named[0].ID != old.ID || named[1].ID != next.ID {
		t.Fatalf("evaluator registrations %+v (%v), want 0.2.0 then 0.3.0", named, err)
	}
}
