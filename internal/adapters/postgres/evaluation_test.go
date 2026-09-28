package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
)

// TestEvaluationDispatchAndAtomicMatchCommit proves the evaluation boundaries
// against the real journal: no dispatch before activation or for unscoped
// Corpora, paged idempotent fan-out, skipped pre-migration triggers, one
// intent per later trigger (enrichment included), a unique Match with its
// Delivery/notice/event/outbox, and rechecks that stop disabled Subscriptions
// and withdrawn Records from committing.
func TestEvaluationDispatchAndAtomicMatchCommit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	run := fmt.Sprint(time.Now().UnixNano())
	org := "adapter-evaluation-" + run
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read", "monitoring:read", "monitoring:write"}, Corpora: []string{"*"}}
	corpora := corpus.Service{Store: postgres.Store{Pool: pool}}
	a, _, err := corpora.Create(ctx, scope, corpus.CreateInput{Key: "a", Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := corpora.Create(ctx, scope, corpus.CreateInput{Key: "b", Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.ContentStore{Pool: pool}
	contents := content.Service{Repository: store, Baseline: store}
	// searchable publishes and promotes one Version, emitting record.retrieval_ready.
	searchable := func(corpusID, key string) (string, string) {
		t.Helper()
		cmd := content.Command{Key: key, Source: content.Source{CorpusID: corpusID, Namespace: "evaluation", RecordKey: key}, Content: content.Text{Kind: "text", Text: "Dépêche " + key}}
		r, err := contents.Accept(ctx, scope, cmd)
		if err != nil {
			t.Fatal(err)
		}
		work, _, err := store.Work(ctx, org, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Publish(ctx, work, publication(content.Blob{Key: "fixture/" + key, SHA256: "text-" + run + key, Size: 10}, content.Blob{Key: "fixture/m-" + key, SHA256: "manifest-" + run + key, Size: 2})); err != nil {
			t.Fatal(err)
		}
		// The fixture blobs are not in S3: keep this Receipt away from a live worker.
		if _, err = pool.Exec(ctx, `UPDATE ingestion_outbox SET dispatched=true WHERE organization=$1`, org); err != nil {
			t.Fatal(err)
		}
		v := content.Version{ID: work.VersionID, RecordID: work.RecordID, Manifest: content.ManifestFor(cmd)}
		seg := wholeBodySegmentation(org, v)
		if err = contents.SaveSegmentation(ctx, org, v, seg); err != nil {
			t.Fatal(err)
		}
		generation, err := store.Generation(ctx, org, corpusID)
		if err != nil {
			t.Fatal(err)
		}
		if err = contents.Promote(ctx, org, seg, generation); err != nil {
			t.Fatal(err)
		}
		return work.RecordID, work.VersionID
	}
	// trigger appends a synthetic trigger event through the journal protocol.
	trigger := func(kind, corpusID, recordID string, versionID any) {
		t.Helper()
		if _, err := pool.Exec(ctx, `WITH s AS (UPDATE organization_journals SET last_sequence=last_sequence+1 WHERE organization=$1 RETURNING last_sequence)
INSERT INTO change_events(organization,sequence,event_id,corpus_id,event_type,resource_type,resource_id,record_version_id) SELECT $1,s.last_sequence,$2||s.last_sequence,$3,$4,'record',$5,$6 FROM s`, org, "event_fixture_"+run+"_", corpusID, kind, recordID, versionID); err != nil {
			t.Fatal(err)
		}
	}
	// Leave no evaluation work for a live worker: fixture Versions have no S3 content.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE evaluation_intents SET state='done',outcome='test_cleanup' WHERE organization=$1 AND state='pending'`, org)
	})
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	intents := func(versionID string) int {
		return count(`SELECT count(*) FROM evaluation_intents WHERE organization=$1 AND record_version_id=$2`, org, versionID)
	}

	_, beforeVersion := searchable(a.ID, "before")
	service := monitoring.Service{Store: store, Corpora: store, Destinations: map[string]monitoring.Destination{"dest": {Organization: org}}}
	q, err := service.CreateSavedQuery(ctx, scope, monitoring.SavedQueryInput{Key: "q", Name: "Q", Definition: monitoring.Definition{CorpusIDs: []string{a.ID}, Expression: map[string]any{}, RetrievalProfile: "balanced", TemporalPolicy: "from_activation"}})
	if err != nil {
		t.Fatal(err)
	}
	subs := make([]monitoring.Subscription, 3)
	for i := range subs {
		if subs[i], err = service.CreateSubscription(ctx, scope, monitoring.SubscriptionInput{Key: fmt.Sprint("s", i), Name: fmt.Sprint("S", i), SavedQueryID: q.ID, SavedQueryVersionID: q.Current.VersionID,
			Evaluator: monitoring.Evaluator{PluginID: monitoring.FixtureEvaluator, Version: monitoring.FixtureEvaluatorVersion, Configuration: map[string]any{"decisions": map[string]any{"default": "match"}}}, DestinationID: "dest"}); err != nil {
			t.Fatal(err)
		}
	}
	hitRecord, hitVersion := searchable(a.ID, "hit")
	_, otherVersion := searchable(b.ID, "other-corpus")
	trigger("record.retrieval_ready", a.ID, hitRecord, nil) // pre-migration shape: no Version

	evaluation := postgres.EvaluationStore{ContentStore: store, Page: 2}
	drain := func() {
		t.Helper()
		for i := 0; i < 20; i++ {
			n, err := evaluation.FanOut(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if n == 0 {
				return
			}
		}
		t.Fatal("fan-out did not settle")
	}
	drain()
	drain()
	if got := intents(beforeVersion); got != 0 {
		t.Fatalf("pre-activation Version dispatched: %d", got)
	}
	if got := intents(otherVersion); got != 0 {
		t.Fatalf("unscoped Corpus dispatched: %d", got)
	}
	if got := intents(hitVersion); got != 3 {
		t.Fatalf("want one intent per Subscription across pages, got %d", got)
	}

	// A later enrichment trigger of the same Version is new evaluation work.
	trigger("record.enrichment_available", a.ID, hitRecord, hitVersion)
	drain()
	if got := intents(hitVersion); got != 6 {
		t.Fatalf("enrichment trigger: %d intents", got)
	}

	// Claims lease due work; a leased intent is not claimed again until it expires.
	claimed := map[string]bool{}
	for i := 0; i < 1000 && len(claimed) < 6; i++ {
		in, err := evaluation.Claim(ctx, time.Minute)
		if err == monitoring.ErrNoWork {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if in.Organization != org {
			continue
		}
		key := fmt.Sprint(in.SubscriptionVersionID, in.Sequence)
		if claimed[key] || in.VersionID != hitVersion || in.RecordID != hitRecord || in.CorpusID != a.ID {
			t.Fatalf("claimed %+v twice or with wrong references", in)
		}
		claimed[key] = true
	}
	if len(claimed) != 6 {
		t.Fatalf("claimed %d of 6 intents", len(claimed))
	}
	if _, err = pool.Exec(ctx, `UPDATE evaluation_intents SET lease_until='-infinity' WHERE organization=$1`, org); err != nil {
		t.Fatal(err)
	}

	var sequence int64
	if err = pool.QueryRow(ctx, `SELECT min(sequence) FROM evaluation_intents WHERE organization=$1 AND record_version_id=$2`, org, hitVersion).Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	intent := func(i int) monitoring.Intent {
		return monitoring.Intent{Organization: org, SubscriptionID: subs[i].ID, SubscriptionVersionID: subs[i].Current.VersionID, Sequence: sequence, CorpusID: a.ID, RecordID: hitRecord, VersionID: hitVersion}
	}
	target, err := evaluation.Target(ctx, intent(0))
	if err != nil || !target.Enabled || target.Enriched || target.Subscription.DestinationID != "dest" || len(target.Definition.CorpusIDs) != 1 {
		t.Fatalf("target %+v %v", target, err)
	}
	// An evaluator error keeps the intent pending with a bounded diagnostic.
	if err = evaluation.Retry(ctx, intent(0), "evaluator_error", time.Hour); err != nil {
		t.Fatal(err)
	}
	if count(`SELECT count(*) FROM evaluation_intents WHERE organization=$1 AND subscription_version_id=$2 AND sequence=$3 AND state='pending' AND error_code='evaluator_error' AND attempts=1`, org, subs[0].Current.VersionID, sequence) != 1 {
		t.Fatal("error was not retained as pending work")
	}
	evidence := monitoring.MatchEvidence{Evaluator: subs[0].Current.Evaluator, Explanation: "fixture", PartKeys: []string{"body"}}
	for i, want := range []string{monitoring.OutcomeMatched, monitoring.OutcomeDuplicate} {
		outcome, err := evaluation.CommitMatch(ctx, intent(0), evidence)
		if err != nil || outcome != want {
			t.Fatalf("commit %d: %s %v", i, outcome, err)
		}
	}
	matchID := content.StableID("match", org, subs[0].Current.VersionID, hitVersion)
	for table, n := range map[string]int{
		"matches":            count(`SELECT count(*) FROM matches WHERE organization=$1`, org),
		"deliveries":         count(`SELECT count(*) FROM deliveries WHERE organization=$1`, org),
		"monitoring_notices": count(`SELECT count(*) FROM monitoring_notices WHERE organization=$1`, org),
		"delivery_outbox":    count(`SELECT count(*) FROM delivery_outbox WHERE organization=$1`, org),
		"match.created":      count(`SELECT count(*) FROM change_events WHERE organization=$1 AND event_type='match.created'`, org),
	} {
		if n != 1 {
			t.Fatalf("%s: %d facts", table, n)
		}
	}
	if count(`SELECT count(*) FROM evaluation_intents WHERE organization=$1 AND subscription_version_id=$2 AND sequence=$3 AND state='done' AND outcome='matched'`, org, subs[0].Current.VersionID, sequence) != 1 {
		t.Fatal("intent not completed with the Match")
	}

	// The feed event and the stored notice share identity and references.
	w, err := store.ReadChanges(ctx, org, a.ID, 0, 100, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var feed *monitoring.Notice
	for _, e := range w.Events {
		if e.Type == "match.created" {
			if e.Monitoring == nil || e.ResourceKind != "match" || e.ResourceID != matchID || e.Monitoring.RecordVersionID != hitVersion {
				t.Fatalf("feed event %+v", e)
			}
			feed = &monitoring.Notice{EventID: e.ID, OccurredAt: e.OccurredAt, References: monitoring.NoticeReferences{DeliveryID: e.Monitoring.DeliveryID}}
		} else if e.Monitoring != nil {
			t.Fatalf("non-monitoring event with references: %+v", e)
		}
	}
	if feed == nil {
		t.Fatal("no match.created feed event")
	}
	delivery, err := store.Delivery(ctx, org, feed.References.DeliveryID)
	if err != nil || delivery.State != "pending" || delivery.AttemptCount != 0 || !delivery.Admission.Allowed || delivery.MatchID != matchID {
		t.Fatalf("delivery %+v %v", delivery, err)
	}
	var body monitoring.Notice
	if err = json.Unmarshal(delivery.Event, &body); err != nil || body.EventID != feed.EventID || body.Type != "match.created" || !body.OccurredAt.Equal(feed.OccurredAt) || body.References.MatchID != matchID {
		t.Fatalf("notice body %s %v", delivery.Event, err)
	}
	m, err := store.Match(ctx, org, matchID)
	if err != nil || m.RecordVersionID != hitVersion || m.SavedQueryVersionID != q.Current.VersionID || m.Evidence.Explanation != "fixture" {
		t.Fatalf("match %+v %v", m, err)
	}
	page, err := store.Matches(ctx, org, subs[0].ID, 0, 10)
	if err != nil || len(page) != 1 {
		t.Fatal(page, err)
	}
	if page, err = store.Matches(ctx, org, subs[0].ID, page[0].Position, 10); err != nil || len(page) != 0 {
		t.Fatal("keyset page", page, err)
	}

	// Disable serializes with commit: a disabled Subscription commits nothing.
	if _, err = service.DisableSubscription(ctx, scope, "disable-s1", subs[1].ID); err != nil {
		t.Fatal(err)
	}
	if outcome, err := evaluation.CommitMatch(ctx, intent(1), evidence); err != nil || outcome != monitoring.OutcomeSubscriptionDisabled {
		t.Fatal(outcome, err)
	}
	// A withdrawn Record commits nothing, and the pending Delivery reports it.
	if _, err = contents.Withdraw(ctx, scope, content.Withdrawal{Key: "withdraw-hit", Source: content.Source{CorpusID: a.ID, Namespace: "evaluation", RecordKey: "hit"}}); err != nil {
		t.Fatal(err)
	}
	if outcome, err := evaluation.CommitMatch(ctx, intent(2), evidence); err != nil || outcome != monitoring.OutcomeIneligible {
		t.Fatal(outcome, err)
	}
	if n := count(`SELECT count(*) FROM matches WHERE organization=$1`, org); n != 1 {
		t.Fatalf("matches after guards: %d", n)
	}
	if delivery, err = store.Delivery(ctx, org, delivery.ID); err != nil || delivery.Admission.Allowed || delivery.Admission.Reason != "record_withdrawn" {
		t.Fatalf("admission %+v %v", delivery.Admission, err)
	}
	// Disabled Subscriptions are no longer dispatched.
	laterRecord, laterVersion := searchable(a.ID, "later")
	drain()
	if got := intents(laterVersion); got != 2 {
		t.Fatalf("disabled Subscription dispatched: %d intents for %s", got, laterRecord)
	}
}

// wholeBodySegmentation is a valid one-segment baseline derivation, so the
// test needs no tokenizer; evaluation only depends on promotion facts.
func wholeBodySegmentation(org string, v content.Version) content.Segmentation {
	text := v.Manifest.Parts[0].Content.Text
	seg := content.Segmentation{ID: content.StableID("segmentation", org, v.ID, "evaluation-fixture"), VersionID: v.ID, Recipe: "evaluation-fixture", Provenance: json.RawMessage(`{}`)}
	p := content.Segment{PartKey: "body", Text: text, Start: 0, End: len([]rune(text)), Derivation: content.SegmentDerivation{UTF8End: len(text), NormalizedSHA256: content.Hash([]byte(text)), ModelInput: text, ModelInputSHA256: content.Hash([]byte(text)), ModelTokens: 1}}
	p.ID = content.SegmentID(org, seg.ID, p)
	seg.Segments = []content.Segment{p}
	return seg
}
