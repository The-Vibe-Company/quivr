package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
)

// TestRenameChangesOnlyTheName proves THE-771 against the real journal: a
// rename changes the display name of a Subscription or Saved Query without a
// new Version, keeps the Matches it produced, commits one renamed event per
// real change, replays per key, and is refused once the resource is deleted.
func TestRenameChangesOnlyTheName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := newEditFixture(t, ctx, "adapter-rename-")
	sub := f.subscribe("s")
	_, v1 := f.publish("r", "r-1", "Dépêche r")
	f.drain()
	intents := f.dispatched(v1)
	if len(intents) != 1 {
		t.Fatalf("intents %+v", intents)
	}
	f.commit(monitoring.OutcomeMatched, func() (string, error) { return f.evaluation.CommitMatch(ctx, intents[0], f.evidence(sub)) })

	renamed, err := f.service.RenameSubscription(ctx, f.scope, "rename-1", sub.ID, "Renamed")
	if err != nil || renamed.Name != "Renamed" || renamed.Current.VersionID != sub.Current.VersionID || !renamed.Enabled {
		t.Fatalf("rename %+v %v", renamed, err)
	}
	if again, err := f.service.RenameSubscription(ctx, f.scope, "rename-1", sub.ID, "Renamed"); err != nil || again.Name != "Renamed" {
		t.Fatalf("replayed rename %+v %v", again, err)
	}
	if _, err = f.service.RenameSubscription(ctx, f.scope, "rename-1", sub.ID, "Other"); !errors.Is(err, monitoring.ErrConflict) {
		t.Fatalf("changed request under the same key: %v", err)
	}
	if _, err = f.service.RenameSubscription(ctx, f.scope, "rename-same", sub.ID, "Renamed"); err != nil {
		t.Fatalf("rename to the same name: %v", err)
	}
	if n := f.events("subscription.renamed", sub.ID); n != 1 {
		t.Fatalf("subscription.renamed events: %d", n)
	}
	if n := f.events("subscription.updated", sub.ID); n != 0 {
		t.Fatalf("a rename announced a new Version: %d", n)
	}
	if history, err := f.service.Matches(ctx, f.scope, sub.ID, 0, 10); err != nil || len(history) != 1 {
		t.Fatalf("Matches after rename %+v %v", history, err)
	}

	q, err := f.service.RenameSavedQuery(ctx, f.scope, "q-rename", f.query.ID, "Renamed query")
	if err != nil || q.Name != "Renamed query" || q.Current.VersionID != f.query.Current.VersionID {
		t.Fatalf("saved query rename %+v %v", q, err)
	}
	if n := f.events("saved_query.renamed", f.query.ID); n != 1 {
		t.Fatalf("saved_query.renamed events: %d", n)
	}

	if _, err = f.service.DeleteSubscription(ctx, f.scope, "delete", sub.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.RenameSubscription(ctx, f.scope, "rename-late", sub.ID, "Late"); !errors.Is(err, monitoring.ErrSubscriptionDeleted) {
		t.Fatalf("rename after delete: %v", err)
	}
	if _, err = f.service.DeleteSavedQuery(ctx, f.scope, "q-delete", f.query.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.RenameSavedQuery(ctx, f.scope, "q-rename-late", f.query.ID, "Late"); !errors.Is(err, monitoring.ErrSavedQueryDeleted) {
		t.Fatalf("saved query rename after delete: %v", err)
	}
}
