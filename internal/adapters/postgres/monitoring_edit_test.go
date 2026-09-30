package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
)

// editFixture adds dispatch and admission helpers to the correction fixture.
type editFixture struct {
	*correctionFixture
	evaluation postgres.EvaluationStore
	deliveries postgres.DeliveryStore
}

func newEditFixture(t *testing.T, ctx context.Context, prefix string) editFixture {
	f := newCorrectionFixture(t, ctx, prefix)
	return editFixture{f, postgres.EvaluationStore{ContentStore: f.store}, postgres.DeliveryStore{ContentStore: f.store, Organization: f.org}}
}

// drain runs dispatch until it settles.
func (f editFixture) drain() {
	f.t.Helper()
	for i := 0; i < 50; i++ {
		n, err := f.evaluation.FanOut(f.ctx)
		if err != nil {
			f.t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
	f.t.Fatal("fan-out did not settle")
}

// dispatched returns the evaluation intents of a Record Version, as dispatched.
func (f editFixture) dispatched(versionID string) []monitoring.Intent {
	f.t.Helper()
	rows, err := f.pool.Query(f.ctx, `SELECT subscription_id,subscription_version_id,sequence,corpus_id,record_id,record_version_id FROM evaluation_intents WHERE organization=$1 AND kind='evaluation' AND record_version_id=$2 ORDER BY sequence`, f.org, versionID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []monitoring.Intent
	for rows.Next() {
		in := monitoring.Intent{Kind: monitoring.IntentEvaluation, Organization: f.org}
		if err = rows.Scan(&in.SubscriptionID, &in.SubscriptionVersionID, &in.Sequence, &in.CorpusID, &in.RecordID, &in.VersionID); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, in)
	}
	return out
}

// admitAll claims every due Delivery once and returns the admission refusal
// by Delivery ("" when an attempt was admitted).
func (f editFixture) admitAll() map[string]string {
	f.t.Helper()
	out := map[string]string{}
	for {
		w, err := f.deliveries.ClaimDelivery(f.ctx, time.Minute)
		if errors.Is(err, monitoring.ErrNoWork) {
			return out
		}
		if err != nil {
			f.t.Fatal(err)
		}
		if _, out[w.DeliveryID], err = f.deliveries.Admit(f.ctx, w, time.Hour, func(string, string) bool { return true }); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f editFixture) events(kind, id string) int {
	return f.count(`SELECT count(*) FROM change_events WHERE organization=$1 AND event_type=$2 AND resource_id=$3`, f.org, kind, id)
}

// TestSubscriptionEditAppliesFromItsCommit proves THE-724's editing rules
// against the real journal. Editing a Saved Query moves no Subscription. A new
// Subscription Version takes effect from its own commit: a change committed
// before it, even when dispatch only reaches it afterwards, is judged by the
// earlier Version, and every later one by the new Version. Matches keep the
// Versions that produced them, every Version stays readable, and replays
// return the same Version without another event.
func TestSubscriptionEditAppliesFromItsCommit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := newEditFixture(t, ctx, "adapter-edit-")
	sub := f.subscribe("s")
	first := sub.Current

	// Published before the edit; dispatch lags and only runs after it.
	before, beforeV := f.publish("before", "before-1", "Dépêche avant")

	edit := monitoring.SavedQueryVersionInput{Key: "q-edit", Definition: monitoring.Definition{CorpusIDs: []string{f.corpusID}, Expression: map[string]any{"fixture": "edited"}, RetrievalProfile: "default", TemporalPolicy: "from_activation"}}
	q2, err := f.service.CreateSavedQueryVersion(ctx, f.scope, f.query.ID, edit)
	if err != nil || q2.VersionID == f.query.Current.VersionID || q2.Definition.Expression["fixture"] != "edited" {
		t.Fatalf("saved query edit %+v %v", q2, err)
	}
	if again, err := f.service.CreateSavedQueryVersion(ctx, f.scope, f.query.ID, edit); err != nil || again.VersionID != q2.VersionID {
		t.Fatalf("saved query edit replay %+v %v", again, err)
	}
	changed := edit
	changed.Definition.Expression = map[string]any{"fixture": "other"}
	if _, err = f.service.CreateSavedQueryVersion(ctx, f.scope, f.query.ID, changed); !errors.Is(err, monitoring.ErrConflict) {
		t.Fatalf("changed saved query edit replay: %v", err)
	}
	if n := f.events("saved_query.updated", f.query.ID); n != 1 {
		t.Fatalf("saved_query.updated events: %d", n)
	}
	if q, _ := f.service.SavedQuery(ctx, f.scope, f.query.ID); q.Current.VersionID != q2.VersionID {
		t.Fatalf("saved query current Version %+v", q.Current)
	}
	if v, err := f.service.SavedQueryVersion(ctx, f.scope, f.query.ID, f.query.Current.VersionID); err != nil || v.Definition.Expression["fixture"] != nil {
		t.Fatalf("first saved query Version %+v %v", v, err)
	}
	if s, _ := f.service.Subscription(ctx, f.scope, sub.ID); s.Current.VersionID != first.VersionID || s.Current.SavedQueryVersionID != f.query.Current.VersionID {
		t.Fatalf("a Saved Query edit moved the Subscription: %+v", s.Current)
	}
	// Replaying the original creation still returns the Subscription, although
	// its Saved Query Version is no longer current.
	if s, err := f.service.CreateSubscription(ctx, f.scope, monitoring.SubscriptionInput{Key: "s", Name: "s", SavedQueryID: f.query.ID, SavedQueryVersionID: f.query.Current.VersionID,
		Evaluator: first.Evaluator, DestinationID: "dest"}); err != nil || s.ID != sub.ID {
		t.Fatalf("creation replay after a Saved Query edit %+v %v", s, err)
	}
	// A new Subscription may only pin the current Version.
	if _, err = f.service.CreateSubscription(ctx, f.scope, monitoring.SubscriptionInput{Key: "stale", Name: "stale", SavedQueryID: f.query.ID, SavedQueryVersionID: f.query.Current.VersionID,
		Evaluator: first.Evaluator, DestinationID: "dest"}); !errors.Is(err, monitoring.ErrUnknownSavedQuery) {
		t.Fatalf("new Subscription on a superseded Saved Query Version: %v", err)
	}

	subEdit := monitoring.SubscriptionVersionInput{Key: "s-edit", SavedQueryVersionID: q2.VersionID, Evaluator: monitoring.Evaluator{PluginID: monitoring.FixtureEvaluator, Version: monitoring.FixtureEvaluatorVersion, Configuration: map[string]any{"decisions": map[string]any{"default": "no_match"}}}, DestinationID: "dest"}
	stale := subEdit
	stale.Key, stale.SavedQueryVersionID = "s-stale", f.query.Current.VersionID
	if _, err = f.service.CreateSubscriptionVersion(ctx, f.scope, sub.ID, stale); !errors.Is(err, monitoring.ErrUnknownSavedQuery) {
		t.Fatalf("edit pinning a superseded Saved Query Version: %v", err)
	}
	second, err := f.service.CreateSubscriptionVersion(ctx, f.scope, sub.ID, subEdit)
	if err != nil || second.VersionID == first.VersionID || second.SavedQueryVersionID != q2.VersionID || second.ActivationPosition <= first.ActivationPosition {
		t.Fatalf("subscription edit %+v %v", second, err)
	}
	if again, err := f.service.CreateSubscriptionVersion(ctx, f.scope, sub.ID, subEdit); err != nil || again.VersionID != second.VersionID || again.ActivationPosition != second.ActivationPosition {
		t.Fatalf("subscription edit replay %+v %v", again, err)
	}
	changedSub := subEdit
	changedSub.DestinationID = "other"
	f.service.Destinations["other"] = monitoring.Destination{Organization: f.org}
	if _, err = f.service.CreateSubscriptionVersion(ctx, f.scope, sub.ID, changedSub); !errors.Is(err, monitoring.ErrConflict) {
		t.Fatalf("changed subscription edit replay: %v", err)
	}
	if n := f.events("subscription.updated", sub.ID); n != 1 {
		t.Fatalf("subscription.updated events: %d", n)
	}
	current, err := f.service.Subscription(ctx, f.scope, sub.ID)
	if err != nil || current.Current.VersionID != second.VersionID || !current.Enabled {
		t.Fatalf("edited Subscription %+v %v", current, err)
	}
	if v, err := f.service.SubscriptionVersion(ctx, f.scope, sub.ID, first.VersionID); err != nil || v.SavedQueryVersionID != f.query.Current.VersionID || v.ActivationPosition != first.ActivationPosition {
		t.Fatalf("first Subscription Version read %+v %v", v, err)
	}

	after, afterV := f.publish("after", "after-1", "Dépêche après")
	f.drain()
	early, late := f.dispatched(beforeV), f.dispatched(afterV)
	if len(early) != 1 || early[0].SubscriptionVersionID != first.VersionID {
		t.Fatalf("a change committed before the edit is judged by the earlier Version: %+v", early)
	}
	if len(late) != 1 || late[0].SubscriptionVersionID != second.VersionID {
		t.Fatalf("a change committed after the edit is judged by the new Version: %+v", late)
	}

	// The earlier Version still commits the Match it judged, pinned to itself.
	f.commit(monitoring.OutcomeMatched, func() (string, error) { return f.evaluation.CommitMatch(ctx, early[0], f.evidence(sub)) })
	m, err := f.store.Match(ctx, f.org, content.StableID("match", f.org, first.VersionID, beforeV))
	if err != nil || m.SubscriptionVersionID != first.VersionID || m.SavedQueryVersionID != f.query.Current.VersionID || m.RecordID != before {
		t.Fatalf("Match of the earlier Version %+v %v", m, err)
	}
	// A later Version never matches that Record Version again, whatever
	// trigger (such as its enrichment) reaches it after the edit.
	again := late[0]
	again.RecordID, again.VersionID = before, beforeV
	f.commit(monitoring.OutcomeDuplicate, func() (string, error) { return f.evaluation.CommitMatch(ctx, again, f.evidence(current)) })
	// The earlier Version never judges a later change.
	superseded := late[0]
	superseded.SubscriptionVersionID = first.VersionID
	f.commit(monitoring.OutcomeVersionSuperseded, func() (string, error) { return f.evaluation.CommitMatch(ctx, superseded, f.evidence(sub)) })
	if target, err := f.evaluation.Target(ctx, superseded); err != nil || !target.Superseded {
		t.Fatalf("superseded target %+v %v", target, err)
	}
	// The new Version judges it and its Match names the new Versions.
	if target, err := f.evaluation.Target(ctx, late[0]); err != nil || target.Superseded || !target.Enabled || target.Definition.Expression["fixture"] != "edited" {
		t.Fatalf("new Version target %+v %v", target, err)
	}
	f.commit(monitoring.OutcomeMatched, func() (string, error) { return f.evaluation.CommitMatch(ctx, late[0], f.evidence(current)) })
	m, err = f.store.Match(ctx, f.org, content.StableID("match", f.org, second.VersionID, afterV))
	if err != nil || m.SubscriptionVersionID != second.VersionID || m.SavedQueryVersionID != q2.VersionID || m.RecordID != after {
		t.Fatalf("Match of the new Version %+v %v", m, err)
	}
	history, err := f.service.Matches(ctx, f.scope, sub.ID, 0, 10)
	if err != nil || len(history) != 2 || history[0].SubscriptionVersionID != first.VersionID || history[1].SubscriptionVersionID != second.VersionID {
		t.Fatalf("Match history keeps each Version %+v %v", history, err)
	}
}

// TestSubscriptionDeleteStopsEvaluationAndDeliveries proves logical deletion
// against the real journal: one subscription.deleted fact whatever the
// replays; no further evaluation and no Delivery Attempt (admission reason
// subscription_deleted); a withdrawal notice is still committed, as for a
// disabled Subscription, but never attempted; no re-enable or edit; and the
// Subscription, its Versions, Matches and Deliveries stay readable. The Saved
// Query can be deleted once no Subscription that is not deleted uses it.
func TestSubscriptionDeleteStopsEvaluationAndDeliveries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := newEditFixture(t, ctx, "adapter-delete-")
	sub := f.subscribe("s")
	record, v1 := f.publish("r", "r-1", "Dépêche r")
	f.drain()
	intents := f.dispatched(v1)
	if len(intents) != 1 {
		t.Fatalf("intents %+v", intents)
	}
	f.commit(monitoring.OutcomeMatched, func() (string, error) { return f.evaluation.CommitMatch(ctx, intents[0], f.evidence(sub)) })
	created, _ := f.noticeOf(f.matchOf(sub, v1), monitoring.NoticeCreated)

	if _, err := f.service.DeleteSavedQuery(ctx, f.scope, "q-delete-early", f.query.ID); !errors.Is(err, monitoring.ErrSavedQueryInUse) {
		t.Fatalf("delete of a used Saved Query: %v", err)
	}

	deleted, err := f.service.DeleteSubscription(ctx, f.scope, "delete-1", sub.ID)
	if err != nil || !deleted.Deleted || deleted.Enabled {
		t.Fatalf("delete %+v %v", deleted, err)
	}
	for _, key := range []string{"delete-1", "delete-2"} {
		if again, err := f.service.DeleteSubscription(ctx, f.scope, key, sub.ID); err != nil || !again.Deleted {
			t.Fatalf("repeat delete %s %+v %v", key, again, err)
		}
	}
	if _, err = f.service.DisableSubscription(ctx, f.scope, "disable-after-delete", sub.ID); err != nil {
		t.Fatalf("disable of a deleted Subscription is a no-op: %v", err)
	}
	if n := f.events("subscription.deleted", sub.ID); n != 1 {
		t.Fatalf("subscription.deleted events: %d", n)
	}
	if n := f.events("subscription.disabled", sub.ID); n != 0 {
		t.Fatalf("delete also announced a disable: %d", n)
	}
	if _, err = f.service.EnableSubscription(ctx, f.scope, "enable", sub.ID); !errors.Is(err, monitoring.ErrSubscriptionDeleted) {
		t.Fatalf("enable after delete: %v", err)
	}
	if _, err = f.service.CreateSubscriptionVersion(ctx, f.scope, sub.ID, monitoring.SubscriptionVersionInput{Key: "edit", SavedQueryVersionID: f.query.Current.VersionID, Evaluator: sub.Current.Evaluator, DestinationID: "dest"}); !errors.Is(err, monitoring.ErrSubscriptionDeleted) {
		t.Fatalf("edit after delete: %v", err)
	}
	if s, _ := f.service.Subscription(ctx, f.scope, sub.ID); s.Enabled || !s.Deleted {
		t.Fatalf("refused commands changed the deleted Subscription: %+v", s)
	}

	// No Delivery Attempt: the pending notice is refused for good.
	if got := f.admitAll(); got[created.ID] != "subscription_deleted" {
		t.Fatalf("admission after delete %v", got)
	}
	// No further evaluation, at dispatch or at commit.
	_, v2 := f.publish("r2", "r2-1", "Dépêche r2")
	f.drain()
	if n := len(f.dispatched(v2)); n != 0 {
		t.Fatalf("deleted Subscription dispatched: %d", n)
	}
	stale := intents[0]
	stale.RecordID, stale.VersionID = content.StableID("record", f.org, f.corpusID, "corrections", "r2"), v2
	stale.Sequence++
	f.commit(monitoring.OutcomeSubscriptionDisabled, func() (string, error) { return f.evaluation.CommitMatch(ctx, stale, f.evidence(sub)) })

	// A withdrawal of an alerted Record: its notice is committed once, like
	// for a disabled Subscription, and never attempted.
	if _, err = f.contents.Withdraw(ctx, f.scope, content.Withdrawal{Key: "withdraw-r", Source: content.Source{CorpusID: f.corpusID, Namespace: "corrections", RecordKey: "r"}}); err != nil {
		t.Fatal(err)
	}
	f.drain()
	var withdrawal monitoring.Intent
	if err = f.pool.QueryRow(ctx, `SELECT subscription_id,subscription_version_id,sequence,corpus_id,record_id,record_version_id FROM evaluation_intents WHERE organization=$1 AND kind='withdrawal'`, f.org).Scan(
		&withdrawal.SubscriptionID, &withdrawal.SubscriptionVersionID, &withdrawal.Sequence, &withdrawal.CorpusID, &withdrawal.RecordID, &withdrawal.VersionID); err != nil {
		t.Fatal(err)
	}
	withdrawal.Kind, withdrawal.Organization = monitoring.IntentWithdrawal, f.org
	f.commit(monitoring.OutcomeWithdrawalNotified, func() (string, error) { return f.evaluation.CommitWithdrawal(ctx, withdrawal) })
	f.commit(monitoring.OutcomeDuplicate, func() (string, error) { return f.evaluation.CommitWithdrawal(ctx, withdrawal) })
	withdrawn, notice := f.noticeOf(f.matchOf(sub, v1), monitoring.NoticeWithdrawn)
	if notice.References.RecordID != record || withdrawn.State != "pending" || withdrawn.Admission.Reason != "subscription_deleted" {
		t.Fatalf("withdrawal notice after delete %+v %s %+v", notice.References, withdrawn.State, withdrawn.Admission)
	}
	if got := f.admitAll(); got[withdrawn.ID] != "subscription_deleted" {
		t.Fatalf("admission of the withdrawal notice %v", got)
	}
	if n := f.count(`SELECT count(*) FROM delivery_attempts WHERE organization=$1`, f.org); n != 0 {
		t.Fatalf("attempts after delete: %d", n)
	}

	// History stays readable.
	if s, err := f.service.Subscription(ctx, f.scope, sub.ID); err != nil || !s.Deleted {
		t.Fatalf("deleted Subscription read %+v %v", s, err)
	}
	if _, err = f.service.SubscriptionVersion(ctx, f.scope, sub.ID, sub.Current.VersionID); err != nil {
		t.Fatalf("deleted Subscription Version read %v", err)
	}
	f.service.MatchStore = f.store
	if history, err := f.service.Matches(ctx, f.scope, sub.ID, 0, 10); err != nil || len(history) != 1 {
		t.Fatalf("deleted Subscription Matches %+v %v", history, err)
	}
	if d, err := f.service.Delivery(ctx, f.scope, created.ID); err != nil || d.Admission.Reason != "subscription_deleted" {
		t.Fatalf("deleted Subscription Delivery %+v %v", d, err)
	}

	// The Saved Query is no longer used: it can be deleted, once.
	q, err := f.service.DeleteSavedQuery(ctx, f.scope, "q-delete", f.query.ID)
	if err != nil || !q.Deleted {
		t.Fatalf("saved query delete %+v %v", q, err)
	}
	if again, err := f.service.DeleteSavedQuery(ctx, f.scope, "q-delete-2", f.query.ID); err != nil || !again.Deleted {
		t.Fatalf("repeat saved query delete %+v %v", again, err)
	}
	if n := f.events("saved_query.deleted", f.query.ID); n != 1 {
		t.Fatalf("saved_query.deleted events: %d", n)
	}
	if _, err = f.service.CreateSavedQueryVersion(ctx, f.scope, f.query.ID, monitoring.SavedQueryVersionInput{Key: "late", Definition: f.query.Current.Definition}); !errors.Is(err, monitoring.ErrSavedQueryDeleted) {
		t.Fatalf("edit of a deleted Saved Query: %v", err)
	}
	if _, err = f.service.CreateSubscription(ctx, f.scope, monitoring.SubscriptionInput{Key: "late", Name: "late", SavedQueryID: f.query.ID, SavedQueryVersionID: f.query.Current.VersionID, Evaluator: sub.Current.Evaluator, DestinationID: "dest"}); !errors.Is(err, monitoring.ErrUnknownSavedQuery) {
		t.Fatalf("Subscription on a deleted Saved Query: %v", err)
	}
	if _, err = f.service.SavedQueryVersion(ctx, f.scope, f.query.ID, f.query.Current.VersionID); err != nil {
		t.Fatalf("deleted Saved Query Version read: %v", err)
	}
}

// TestSubscriptionEditChangesScope moves a Subscription from one Corpus to
// another. A change committed before the edit in the old Corpus is still
// judged by the earlier Version; after it only the new Corpus is evaluated.
// The edit is announced in both Corpora, and a key must grant every Corpus
// any Version pinned to see the Subscription and its Match history.
func TestSubscriptionEditChangesScope(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := newEditFixture(t, ctx, "adapter-edit-scope-")
	a := f.corpusID
	other, _, err := corpus.Service{Store: postgres.Store{Pool: f.pool}}.Create(ctx, f.scope, corpus.CreateInput{Key: "b", Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	b := other.ID
	sub := f.subscribe("s")
	first := sub.Current
	_, lagV := f.publish("lag", "lag-1", "Dépêche avant")

	q2, err := f.service.CreateSavedQueryVersion(ctx, f.scope, f.query.ID, monitoring.SavedQueryVersionInput{Key: "to-b", Definition: monitoring.Definition{CorpusIDs: []string{b}, Expression: map[string]any{}, RetrievalProfile: "default", TemporalPolicy: "from_activation"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.service.CreateSubscriptionVersion(ctx, f.scope, sub.ID, monitoring.SubscriptionVersionInput{Key: "to-b", SavedQueryVersionID: q2.VersionID, Evaluator: first.Evaluator, DestinationID: "dest"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{a, b} {
		for _, e := range []struct{ kind, id string }{{"saved_query.updated", f.query.ID}, {"subscription.updated", sub.ID}} {
			if n := f.count(`SELECT count(*) FROM change_events WHERE organization=$1 AND corpus_id=$2 AND event_type=$3 AND resource_id=$4`, f.org, c, e.kind, e.id); n != 1 {
				t.Fatalf("%s in %s: %d", e.kind, c, n)
			}
		}
	}
	_, inAV := f.publish("in-a", "in-a-1", "Dépêche A après")
	f.corpusID = b
	_, inBV := f.publish("in-b", "in-b-1", "Dépêche B après")
	f.corpusID = a
	f.drain()
	if got := f.dispatched(lagV); len(got) != 1 || got[0].SubscriptionVersionID != first.VersionID {
		t.Fatalf("change in the old Corpus before the edit: %+v", got)
	}
	if got := f.dispatched(inAV); len(got) != 0 {
		t.Fatalf("change in the old Corpus after the edit: %+v", got)
	}
	inB := f.dispatched(inBV)
	if len(inB) != 1 || inB[0].SubscriptionVersionID != second.VersionID {
		t.Fatalf("change in the new Corpus after the edit: %+v", inB)
	}
	// The earlier Version's scope does not cover the new Corpus.
	misjudged := inB[0]
	misjudged.SubscriptionVersionID, misjudged.Sequence = first.VersionID, second.ActivationPosition
	f.commit(monitoring.OutcomeIneligible, func() (string, error) { return f.evaluation.CommitMatch(ctx, misjudged, f.evidence(sub)) })

	onlyB := corpus.Scope{Organization: f.org, Actions: f.scope.Actions, Corpora: []string{b}}
	if _, err = f.service.Subscription(ctx, onlyB, sub.ID); !errors.Is(err, monitoring.ErrNotFound) {
		t.Fatalf("a key without the earlier Corpus sees the Subscription: %v", err)
	}
	if s, err := f.service.Subscription(ctx, f.scope, sub.ID); err != nil || len(s.PinnedCorpusIDs) != 2 {
		t.Fatalf("pinned Corpora %+v %v", s, err)
	}
}
