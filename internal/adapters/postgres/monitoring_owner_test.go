package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost/fakeplugin"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
)

// ownerFixture creates owned and global Subscriptions.
type ownerFixture struct{ editFixture }

func newOwnerFixture(t *testing.T, ctx context.Context) ownerFixture {
	return ownerFixture{newEditFixture(t, ctx, "org_owner_")}
}

func (f ownerFixture) create(key, owner string) (monitoring.Subscription, error) {
	return f.service.CreateSubscription(f.ctx, f.scope, monitoring.SubscriptionInput{Key: key, Name: key, Owner: owner, SavedQueryID: f.query.ID, SavedQueryVersionID: f.query.Current.VersionID,
		Evaluator: monitoring.Evaluator{PluginID: fakeplugin.FixtureEvaluator, Version: fakeplugin.FixtureEvaluatorVersion, Configuration: map[string]any{"decisions": map[string]any{"default": "match"}}}, DestinationID: "dest"})
}

func (f ownerFixture) mustCreate(key, owner string) monitoring.Subscription {
	f.t.Helper()
	s, err := f.create(key, owner)
	if err != nil {
		f.t.Fatalf("create %s for %q: %v", key, owner, err)
	}
	return s
}

// TestSubscriptionOwnerReadsAndListing checks where the Subscription Owner
// is visible: Subscription and Version reads (an edit keeps it), the listing
// by owner and of global Subscriptions (active, visible, paged by ID), and a
// Match with its notice and change feed event. Global ones carry none.
func TestSubscriptionOwnerReadsAndListing(t *testing.T) {
	ctx := context.Background()
	f := newOwnerFixture(t, ctx)
	owned := f.mustCreate("owned", "user-123")
	global := f.mustCreate("global", "")
	if owned.Owner != "user-123" || owned.Current.Owner != "user-123" || global.Owner != "" || global.Current.Owner != "" {
		t.Fatalf("owner on creation: %+v %+v", owned, global)
	}
	edited, err := f.service.CreateSubscriptionVersion(ctx, f.scope, owned.ID, monitoring.SubscriptionVersionInput{Key: "edit", SavedQueryVersionID: f.query.Current.VersionID, Evaluator: owned.Current.Evaluator, DestinationID: "dest"})
	if err != nil || edited.Owner != "user-123" {
		t.Fatalf("a new Version keeps the owner: %v %+v", err, edited)
	}
	if v, err := f.service.SubscriptionVersion(ctx, f.scope, owned.ID, owned.Current.VersionID); err != nil || v.Owner != "user-123" {
		t.Fatalf("earlier Version read: %v %+v", err, v)
	}
	if read, err := f.service.Subscription(ctx, f.scope, owned.ID); err != nil || read.Owner != "user-123" || read.Current.VersionID != edited.VersionID {
		t.Fatalf("Subscription read: %v %+v", err, read)
	}

	// Listing: active Subscriptions of one owner, in ID order, paged.
	second := f.mustCreate("owned-2", "user-123")
	disabled := f.mustCreate("owned-3", "user-123")
	if _, err = f.service.DisableSubscription(ctx, f.scope, "disable", disabled.ID); err != nil {
		t.Fatal(err)
	}
	f.mustCreate("someone-else", "user-456")
	ids := func(subs []monitoring.Subscription) []string {
		out := []string{}
		for _, s := range subs {
			out = append(out, s.ID)
		}
		return out
	}
	want := []string{owned.ID, second.ID}
	if want[0] > want[1] {
		want[0], want[1] = want[1], want[0]
	}
	page, err := f.service.Subscriptions(ctx, f.scope, monitoring.OwnerFilter{Owner: "user-123"}, "", 1)
	if err != nil || fmt.Sprint(ids(page)) != fmt.Sprint(want[:1]) || page[0].Owner != "user-123" {
		t.Fatalf("first page: %v %v", err, ids(page))
	}
	if page, err = f.service.Subscriptions(ctx, f.scope, monitoring.OwnerFilter{Owner: "user-123"}, want[0], 10); err != nil || fmt.Sprint(ids(page)) != fmt.Sprint(want[1:]) {
		t.Fatalf("next page: %v %v", err, ids(page))
	}
	if page, err = f.service.Subscriptions(ctx, f.scope, monitoring.OwnerFilter{Global: true}, "", 10); err != nil || fmt.Sprint(ids(page)) != fmt.Sprint([]string{global.ID}) {
		t.Fatalf("global listing: %v %v", err, ids(page))
	}
	// A key that lacks a pinned Corpus does not list the Subscription.
	other, _, err := corpus.Service{Store: postgres.Store{Pool: f.pool}}.Create(ctx, f.scope, corpus.CreateInput{Key: "b", Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	wide, err := f.service.CreateSavedQuery(ctx, f.scope, monitoring.SavedQueryInput{Key: "wide", Name: "W", Definition: monitoring.Definition{CorpusIDs: []string{f.corpusID, other.ID}, Expression: map[string]any{}, RetrievalProfile: "default", TemporalPolicy: "from_activation"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.CreateSubscription(ctx, f.scope, monitoring.SubscriptionInput{Key: "wide", Name: "wide", Owner: "user-789", SavedQueryID: wide.ID, SavedQueryVersionID: wide.Current.VersionID, Evaluator: owned.Current.Evaluator, DestinationID: "dest"}); err != nil {
		t.Fatal(err)
	}
	narrow := f.scope
	narrow.Corpora = []string{f.corpusID}
	if page, err = f.service.Subscriptions(ctx, narrow, monitoring.OwnerFilter{Owner: "user-789"}, "", 10); err != nil || len(page) != 0 {
		t.Fatalf("narrow key lists a Subscription on an ungranted Corpus: %v %v", err, ids(page))
	}
	if page, err = f.service.Subscriptions(ctx, narrow, monitoring.OwnerFilter{Owner: "user-123"}, "", 10); err != nil || len(page) != 2 {
		t.Fatalf("narrow key lists its own Corpus: %v %v", err, ids(page))
	}

	// A change dispatched to both Subscriptions: the owned Match carries the
	// owner in its reads, its notice and its feed event; the global one none.
	recordID, versionID := f.publish("owned-record", "owned-1", "Dépêche")
	f.drain()
	intents := f.intentsBySubscription(versionID)
	matches := map[string]monitoring.Match{}
	for _, sub := range []string{owned.ID, global.ID} {
		in, ok := intents[sub]
		if !ok {
			t.Fatalf("%s not dispatched: %+v", sub, intents)
		}
		if outcome, err := f.evaluation.CommitMatch(ctx, in, monitoring.MatchEvidence{Evaluator: owned.Current.Evaluator, Explanation: "fixture", PartKeys: []string{"body"}}); err != nil || outcome != monitoring.OutcomeMatched {
			t.Fatalf("commit %s: %s %v", sub, outcome, err)
		}
		m, err := f.service.Match(ctx, f.scope, content.StableID("match", f.org, in.SubscriptionVersionID, versionID))
		if err != nil || m.RecordID != recordID {
			t.Fatalf("match read: %v %+v", err, m)
		}
		matches[sub] = m
	}
	if matches[owned.ID].Owner != "user-123" || matches[global.ID].Owner != "" {
		t.Fatalf("Match owners: %+v", matches)
	}
	if listed, err := f.service.Matches(ctx, f.scope, owned.ID, 0, 10); err != nil || len(listed) != 1 || listed[0].Owner != "user-123" {
		t.Fatalf("Match list: %v %+v", err, listed)
	}
	_, notice := f.noticeOf(matches[owned.ID].ID, monitoring.NoticeCreated)
	_, globalNotice := f.noticeOf(matches[global.ID].ID, monitoring.NoticeCreated)
	if notice.References.Owner != "user-123" || globalNotice.References.Owner != "" {
		t.Fatalf("notice owners: %+v %+v", notice.References, globalNotice.References)
	}
	var raw struct {
		References map[string]any `json:"references"`
	}
	globalDelivery, _ := f.noticeOf(matches[global.ID].ID, monitoring.NoticeCreated)
	if err = json.Unmarshal(globalDelivery.Event, &raw); err != nil {
		t.Fatal(err)
	}
	if _, has := raw.References["owner"]; has {
		t.Fatalf("a global notice has no owner key: %s", globalDelivery.Event)
	}
	window, err := f.store.ReadChanges(ctx, f.org, f.corpusID, 0, 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	feed := map[string]string{}
	for _, e := range window.Events {
		if e.Monitoring != nil {
			feed[e.Monitoring.SubscriptionID] = e.Monitoring.Owner
		}
	}
	if len(feed) != 2 || feed[owned.ID] != "user-123" || feed[global.ID] != "" {
		t.Fatalf("feed owners: %v", feed)
	}
}

// intentsBySubscription returns the first dispatched evaluation intent of a
// Record Version for each Subscription.
func (f ownerFixture) intentsBySubscription(versionID string) map[string]monitoring.Intent {
	out := map[string]monitoring.Intent{}
	for _, in := range f.dispatched(versionID) {
		if _, ok := out[in.SubscriptionID]; !ok {
			out[in.SubscriptionID] = in
		}
	}
	return out
}
