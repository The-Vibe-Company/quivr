package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestCorrectionAndWithdrawalNotices proves the THE-657 commit boundaries
// against the real journal: a matching correction creates a linked successor
// Match and a self-sufficient match.corrected notice; a negative decision on a
// correction commits one match.no_longer_matches notice for the prior Match and
// no Match; a record.withdrawn event is dispatched as withdrawal intents that
// idempotently create match.withdrawn notices after rechecking eligibility;
// admission refuses superseded and withdrawn ordinary notices but admits the
// withdrawal notice.
func TestCorrectionAndWithdrawalNotices(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := newCorrectionFixture(t, ctx, "adapter-corrections-")
	pool, org, scope, store, contents, service := f.pool, f.org, f.scope, f.store, f.contents, f.service
	publish, count, noticeOf, commit := f.publish, f.count, f.noticeOf, f.commit
	var err error
	// s0: positive correction; s1: negative correction; s2: disabled after
	// the withdrawal is dispatched; s3: disabled before it is dispatched.
	// Both disabled ones still get their withdrawal notice (THE-696).
	subs := make([]monitoring.Subscription, 4)
	for i := range subs {
		subs[i] = f.subscribe(fmt.Sprint("s", i))
	}
	evaluation := postgres.EvaluationStore{ContentStore: store, Page: 3} // four withdrawal candidates span two pages
	intent := func(i int, recordID, versionID string) monitoring.Intent {
		return f.intent(subs[i], recordID, versionID)
	}
	evidence := f.evidence(subs[0])
	matchOf := func(i int, versionID string) string { return f.matchOf(subs[i], versionID) }

	// A first Version is alerted to every Subscription.
	record, v1 := publish("r", "r-1", "Dépêche r v1")
	for i := range subs {
		commit(monitoring.OutcomeMatched, func() (string, error) { return evaluation.CommitMatch(ctx, intent(i, record, v1), evidence) })
	}
	// A Record without a prior Match: a negative decision commits nothing.
	fresh, freshV := publish("fresh", "fresh-1", "Dépêche fresh")
	commit(monitoring.OutcomeNoMatch, func() (string, error) { return evaluation.CommitNoMatch(ctx, intent(1, fresh, freshV)) })

	// Ordinary correction v2.
	_, v2 := publish("r", "r-2", "Dépêche r v2")
	// Positive: a linked successor Match with a self-sufficient notice.
	for _, want := range []string{monitoring.OutcomeMatched, monitoring.OutcomeDuplicate} {
		commit(want, func() (string, error) { return evaluation.CommitMatch(ctx, intent(0, record, v2), evidence) })
	}
	successor, err := store.Match(ctx, org, matchOf(0, v2))
	if err != nil || successor.PreviousMatchID != matchOf(0, v1) || successor.RecordVersionID != v2 {
		t.Fatalf("successor Match %+v %v", successor, err)
	}
	corrected, correctedNotice := noticeOf(successor.ID, monitoring.NoticeCorrected)
	want := monitoring.NoticeReferences{MatchID: successor.ID, RecordID: record, RecordVersionID: v2, SubscriptionID: subs[0].ID, SubscriptionVersionID: subs[0].Current.VersionID, DeliveryID: corrected.ID, PreviousMatchID: matchOf(0, v1)}
	if correctedNotice.References != want || !corrected.Admission.Allowed {
		t.Fatalf("match.corrected %+v %+v", correctedNotice.References, corrected.Admission)
	}
	// Negative: one notice for the prior Match, no Match for the correction.
	for _, want := range []string{monitoring.OutcomeNoLongerMatches, monitoring.OutcomeDuplicate} {
		commit(want, func() (string, error) { return evaluation.CommitNoMatch(ctx, intent(1, record, v2)) })
	}
	if n := count(`SELECT count(*) FROM matches WHERE organization=$1 AND subscription_id=$2`, org, subs[1].ID); n != 1 {
		t.Fatalf("negative correction fabricated a Match: %d", n)
	}
	invalidated, invalidation := noticeOf(matchOf(1, v1), monitoring.NoticeNoLongerMatches)
	want = monitoring.NoticeReferences{MatchID: matchOf(1, v1), RecordID: record, RecordVersionID: v2, SubscriptionID: subs[1].ID, SubscriptionVersionID: subs[1].Current.VersionID, DeliveryID: invalidated.ID}
	if invalidation.References != want || !invalidated.Admission.Allowed {
		t.Fatalf("match.no_longer_matches %+v %+v", invalidation.References, invalidated.Admission)
	}
	// Distinct immutable identities, shared by the feed.
	if correctedNotice.EventID == invalidation.EventID {
		t.Fatal("notice identities collide")
	}
	w, err := store.ReadChanges(ctx, org, f.corpusID, 0, 1000, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, e := range w.Events {
		if e.ID == correctedNotice.EventID || e.ID == invalidation.EventID {
			n := correctedNotice
			if e.ID == invalidation.EventID {
				n = invalidation
			}
			if e.Type != n.Type || e.ResourceKind != "match" || e.ResourceID != n.References.MatchID || e.Monitoring == nil ||
				e.Monitoring.RecordVersionID != n.References.RecordVersionID || e.Monitoring.PreviousMatchID != n.References.PreviousMatchID || !e.OccurredAt.Equal(n.OccurredAt) {
				t.Fatalf("feed event %+v differs from notice %+v", e, n)
			}
			seen++
		}
	}
	if seen != 2 {
		t.Fatalf("feed carries %d of 2 correction notices", seen)
	}
	// The undelivered earlier positives are superseded; the prior Matches stay readable.
	for i := 0; i < 2; i++ {
		d, _ := noticeOf(matchOf(i, v1), monitoring.NoticeCreated)
		if d.Admission.Allowed || d.Admission.Reason != "superseded" || d.State != "pending" {
			t.Fatalf("superseded match.created %+v", d)
		}
		if m, err := store.Match(ctx, org, matchOf(i, v1)); err != nil || m.RecordVersionID != v1 {
			t.Fatal("prior Match no longer inspectable", err)
		}
	}
	// A second non-matching correction of the same Match is not a new notice;
	// a no-longer-current Version cannot invalidate.
	_, v3 := publish("r", "r-3", "Dépêche r v3")
	commit(monitoring.OutcomeDuplicate, func() (string, error) { return evaluation.CommitNoMatch(ctx, intent(1, record, v3)) })
	commit(monitoring.OutcomeIneligible, func() (string, error) { return evaluation.CommitNoMatch(ctx, intent(1, record, v2)) })
	if n := count(`SELECT count(*) FROM monitoring_notices WHERE organization=$1 AND kind='match.no_longer_matches'`, org); n != 1 {
		t.Fatalf("no_longer_matches notices: %d", n)
	}

	// Worker admission: superseded work is parked without an attempt; a
	// correction notice is admitted.
	ds := postgres.DeliveryStore{ContentStore: store, Organization: org}
	configured := func(string, string) bool { return true }
	// s2 and s3 keep their match.created pending for the withdrawal checks.
	for i := 2; i < 4; i++ {
		d, _ := noticeOf(matchOf(i, v1), monitoring.NoticeCreated)
		if _, err = pool.Exec(ctx, `UPDATE delivery_outbox SET available_at='infinity' WHERE organization=$1 AND delivery_id=$2`, org, d.ID); err != nil {
			t.Fatal(err)
		}
	}
	admitted := map[string]string{}
	for {
		w, err := ds.ClaimDelivery(ctx, time.Minute)
		if errors.Is(err, monitoring.ErrNoWork) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		a, refused, err := ds.Admit(ctx, w, time.Hour, configured)
		if err != nil {
			t.Fatal(err)
		}
		admitted[w.DeliveryID] = refused
		if refused == "" {
			if err = ds.Record(ctx, a, monitoring.AttemptOutcome{Outcome: monitoring.AttemptAcknowledged, HTTPStatus: 204}, monitoring.Retry{Window: time.Hour}); err != nil {
				t.Fatal(err)
			}
		}
	}
	superseded, _ := noticeOf(matchOf(0, v1), monitoring.NoticeCreated)
	if admitted[superseded.ID] != "superseded" || admitted[corrected.ID] != "" || admitted[invalidated.ID] != "" || superseded.AttemptCount != 0 {
		t.Fatalf("admission %v", admitted)
	}
	if n := count(`SELECT count(*) FROM delivery_attempts WHERE organization=$1 AND delivery_id=$2`, org, superseded.ID); n != 0 {
		t.Fatal("superseded notice was attempted")
	}

	// Withdrawal: s3 is disabled before the withdrawal is dispatched.
	if _, err = service.DisableSubscription(ctx, scope, "disable-s3", subs[3].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = contents.Withdraw(ctx, scope, content.Withdrawal{Key: "withdraw-r", Source: content.Source{CorpusID: f.corpusID, Namespace: "corrections", RecordKey: "r"}}); err != nil {
		t.Fatal(err)
	}
	// Suppression is immediate: nothing positive commits for the Record any more.
	commit(monitoring.OutcomeIneligible, func() (string, error) { return evaluation.CommitMatch(ctx, intent(2, record, v3), evidence) })
	drain := func() {
		t.Helper()
		for i := 0; i < 50; i++ {
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
	withdrawals := func() map[string]monitoring.Intent {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT subscription_id,subscription_version_id,sequence,corpus_id,record_id,record_version_id FROM evaluation_intents WHERE organization=$1 AND kind='withdrawal'`, org)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]monitoring.Intent{}
		for rows.Next() {
			in := monitoring.Intent{Kind: monitoring.IntentWithdrawal, Organization: org}
			if err = rows.Scan(&in.SubscriptionID, &in.SubscriptionVersionID, &in.Sequence, &in.CorpusID, &in.RecordID, &in.VersionID); err != nil {
				t.Fatal(err)
			}
			out[in.SubscriptionID] = in
		}
		return out
	}
	intents := withdrawals()
	if len(intents) != 4 || intents[subs[3].ID].SubscriptionID != subs[3].ID || intents[subs[0].ID].VersionID != v2 || intents[subs[1].ID].VersionID != v1 || intents[subs[0].ID].RecordID != record {
		t.Fatalf("withdrawal intents %+v", intents)
	}
	// s2 is disabled after dispatch: its notice is committed all the same.
	if _, err = service.DisableSubscription(ctx, scope, "disable-s2", subs[2].ID); err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{2, 3} {
		commit(monitoring.OutcomeWithdrawalNotified, func() (string, error) { return evaluation.CommitWithdrawal(ctx, intents[subs[i].ID]) })
	}
	for _, want := range []string{monitoring.OutcomeWithdrawalNotified, monitoring.OutcomeDuplicate} {
		commit(want, func() (string, error) { return evaluation.CommitWithdrawal(ctx, intents[subs[0].ID]) })
	}
	commit(monitoring.OutcomeWithdrawalNotified, func() (string, error) { return evaluation.CommitWithdrawal(ctx, intents[subs[1].ID]) })
	if n := count(`SELECT count(*) FROM monitoring_notices WHERE organization=$1 AND kind='match.withdrawn'`, org); n != 4 {
		t.Fatalf("withdrawal notices: %d", n)
	}
	// The withdrawal notice references the latest positive Match and its Version.
	withdrawn, withdrawal := noticeOf(successor.ID, monitoring.NoticeWithdrawn)
	want = monitoring.NoticeReferences{MatchID: successor.ID, RecordID: record, RecordVersionID: v2, SubscriptionID: subs[0].ID, SubscriptionVersionID: subs[0].Current.VersionID, DeliveryID: withdrawn.ID}
	if withdrawal.References != want || !withdrawn.Admission.Allowed {
		t.Fatalf("match.withdrawn %+v %+v", withdrawal.References, withdrawn.Admission)
	}
	// Ordinary notices of the withdrawn Record are refused; the withdrawal notice is admitted.
	if d, _ := noticeOf(matchOf(2, v1), monitoring.NoticeCreated); d.Admission.Reason != "subscription_disabled" {
		t.Fatal("disabled", d.Admission)
	}
	if _, err = pool.Exec(ctx, `UPDATE delivery_outbox SET available_at=now() WHERE organization=$1`, org); err != nil {
		t.Fatal(err)
	}
	admitted = map[string]string{}
	for {
		w, err := ds.ClaimDelivery(ctx, time.Minute)
		if errors.Is(err, monitoring.ErrNoWork) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, admitted[w.DeliveryID], err = ds.Admit(ctx, w, time.Hour, configured); err != nil {
			t.Fatal(err)
		}
	}
	created2, _ := noticeOf(matchOf(2, v1), monitoring.NoticeCreated)
	withdrawn2, _ := noticeOf(matchOf(2, v1), monitoring.NoticeWithdrawn)
	if admitted[withdrawn.ID] != "" || admitted[superseded.ID] != "record_withdrawn" || admitted[created2.ID] != "subscription_disabled" || admitted[withdrawn2.ID] != "subscription_disabled" {
		t.Fatalf("admission after withdrawal %v", admitted)
	}
	// A repeated withdrawal emits no second record.withdrawn and no new intent.
	if _, err = contents.Withdraw(ctx, scope, content.Withdrawal{Key: "withdraw-r-again", Source: content.Source{CorpusID: f.corpusID, Namespace: "corrections", RecordKey: "r"}}); err != nil {
		t.Fatal(err)
	}
	drain()
	if len(withdrawals()) != 4 {
		t.Fatal("repeated withdrawal dispatched new work")
	}
}

// TestNoLongerMatchesSupersededByLaterCorrection proves THE-694 against the
// real journal: an undelivered match.no_longer_matches is superseded once a
// match.corrected with a later journal position exists for the same
// Subscription and Record, never by an earlier one. An attempt admitted
// before that correction finishes; afterwards the work is parked without a
// new attempt and leaves the backlog, and the correction is admitted.
func TestNoLongerMatchesSupersededByLaterCorrection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := newCorrectionFixture(t, ctx, "adapter-rematch-")
	sub := f.subscribe("s")
	evaluation := postgres.EvaluationStore{ContentStore: f.store}
	ds := postgres.DeliveryStore{ContentStore: f.store, Organization: f.org}
	configured := func(string, string) bool { return true }
	evidence := f.evidence(sub)
	match := func(record, version string) {
		t.Helper()
		f.commit(monitoring.OutcomeMatched, func() (string, error) { return evaluation.CommitMatch(ctx, f.intent(sub, record, version), evidence) })
	}
	parkAllBut := func(deliveryID string) {
		t.Helper()
		if _, err := f.pool.Exec(ctx, `UPDATE delivery_outbox SET available_at=CASE WHEN delivery_id=$2 THEN now() ELSE 'infinity' END WHERE organization=$1`, f.org, deliveryID); err != nil {
			t.Fatal(err)
		}
	}
	claim := func(deliveryID string) monitoring.DeliveryWork {
		t.Helper()
		parkAllBut(deliveryID)
		w, err := ds.ClaimDelivery(ctx, time.Minute)
		if err != nil || w.DeliveryID != deliveryID {
			t.Fatalf("claim %s: %+v %v", deliveryID, w, err)
		}
		return w
	}

	// v1 alerts, v2 corrects positively, v3 no longer matches.
	record, v1 := f.publish("r", "r-1", "Dépêche r v1")
	match(record, v1)
	_, v2 := f.publish("r", "r-2", "Dépêche r v2")
	match(record, v2)
	_, v3 := f.publish("r", "r-3", "Dépêche r v3")
	f.commit(monitoring.OutcomeNoLongerMatches, func() (string, error) { return evaluation.CommitNoMatch(ctx, f.intent(sub, record, v3)) })
	stale, _ := f.noticeOf(f.matchOf(sub, v2), monitoring.NoticeNoLongerMatches)
	// The earlier match.corrected does not supersede it; it supersedes the
	// earlier positives as before.
	if !stale.Admission.Allowed {
		t.Fatalf("match.no_longer_matches superseded by an earlier correction: %+v", stale.Admission)
	}
	for _, n := range []struct{ version, kind string }{{v1, monitoring.NoticeCreated}, {v2, monitoring.NoticeCorrected}} {
		if d, _ := f.noticeOf(f.matchOf(sub, n.version), n.kind); d.Admission.Reason != "superseded" {
			t.Fatalf("%s not superseded: %+v", n.kind, d.Admission)
		}
	}

	// Its first attempt is admitted, then v4 matches again while it is in flight.
	inFlight, refused, err := ds.Admit(ctx, claim(stale.ID), time.Hour, configured)
	if err != nil || refused != "" {
		t.Fatalf("first attempt refused %q %v", refused, err)
	}
	_, v4 := f.publish("r", "r-4", "Dépêche r v4")
	match(record, v4)
	rematch, rematchNotice := f.noticeOf(f.matchOf(sub, v4), monitoring.NoticeCorrected)
	if rematchNotice.References.PreviousMatchID != f.matchOf(sub, v2) || !rematch.Admission.Allowed {
		t.Fatalf("match.corrected after no_longer_matches %+v %+v", rematchNotice.References, rematch.Admission)
	}
	// The in-flight attempt finishes and records its outcome.
	if err = ds.Record(ctx, inFlight, monitoring.AttemptOutcome{Outcome: monitoring.AttemptRetryableError, HTTPStatus: 503}, monitoring.Retry{Window: time.Hour}); err != nil {
		t.Fatal(err)
	}
	stale, _ = f.noticeOf(f.matchOf(sub, v2), monitoring.NoticeNoLongerMatches)
	if stale.State != "pending" || stale.AttemptCount != 1 || stale.Admission.Allowed || stale.Admission.Reason != "superseded" {
		t.Fatalf("stale match.no_longer_matches: state %s, %d attempts, admission %+v", stale.State, stale.AttemptCount, stale.Admission)
	}
	// Its retry is refused without an attempt and parked out of the backlog.
	if _, refused, err = ds.Admit(ctx, claim(stale.ID), time.Hour, configured); err != nil || refused != "superseded" {
		t.Fatalf("retry admitted: %q %v", refused, err)
	}
	if n := f.count(`SELECT count(*) FROM delivery_attempts WHERE organization=$1 AND delivery_id=$2`, f.org, stale.ID); n != 1 {
		t.Fatalf("superseded notice attempted again: %d attempts", n)
	}
	if n := f.count(`SELECT count(*) FROM delivery_outbox WHERE organization=$1 AND delivery_id=$2 AND available_at<'infinity'`, f.org, stale.ID); n != 0 {
		t.Fatal("superseded work still scheduled")
	}
	// The later correction itself is delivered.
	if _, refused, err = ds.Admit(ctx, claim(rematch.ID), time.Hour, configured); err != nil || refused != "" {
		t.Fatalf("match.corrected refused %q %v", refused, err)
	}
}

// correctionFixture is one Organization with a Corpus and a Saved Query over
// it, plus the helpers the correction adapter tests share.
type correctionFixture struct {
	t        *testing.T
	ctx      context.Context
	pool     *pgxpool.Pool
	run, org string
	corpusID string
	scope    corpus.Scope
	store    postgres.ContentStore
	contents content.Service
	service  monitoring.Service
	query    monitoring.SavedQuery
}

func newCorrectionFixture(t *testing.T, ctx context.Context, prefix string) *correctionFixture {
	t.Helper()
	pool := adapterPool(t, ctx)
	f := &correctionFixture{t: t, ctx: ctx, pool: pool, run: fmt.Sprint(time.Now().UnixNano()), store: postgres.ContentStore{Pool: pool}}
	f.org = prefix + f.run
	f.scope = corpus.Scope{Organization: f.org, Actions: []string{"corpora:write", "content:write", "content:read", "monitoring:read", "monitoring:write"}, Corpora: []string{"*"}}
	a, _, err := corpus.Service{Store: postgres.Store{Pool: pool}}.Create(ctx, f.scope, corpus.CreateInput{Key: "a", Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	f.corpusID = a.ID
	f.contents = content.Service{Repository: f.store, Baseline: f.store}
	org := f.org
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `UPDATE evaluation_intents SET state='done',outcome='test_cleanup' WHERE organization=$1 AND state='pending'`, org)
		_, _ = pool.Exec(bg, `UPDATE delivery_outbox SET available_at='infinity' WHERE organization=$1`, org)
	})
	f.service = monitoring.Service{Store: f.store, Corpora: f.store, Destinations: map[string]monitoring.Destination{"dest": {Organization: org}}, MatchStore: f.store}
	if f.query, err = f.service.CreateSavedQuery(ctx, f.scope, monitoring.SavedQueryInput{Key: "q", Name: "Q", Definition: monitoring.Definition{CorpusIDs: []string{a.ID}, Expression: map[string]any{}, RetrievalProfile: "balanced", TemporalPolicy: "from_activation"}}); err != nil {
		t.Fatal(err)
	}
	return f
}

// subscribe creates an enabled fixture-evaluator Subscription on "dest".
func (f *correctionFixture) subscribe(key string) monitoring.Subscription {
	f.t.Helper()
	s, err := f.service.CreateSubscription(f.ctx, f.scope, monitoring.SubscriptionInput{Key: key, Name: key, SavedQueryID: f.query.ID, SavedQueryVersionID: f.query.Current.VersionID,
		Evaluator: monitoring.Evaluator{PluginID: monitoring.FixtureEvaluator, Version: monitoring.FixtureEvaluatorVersion, Configuration: map[string]any{"decisions": map[string]any{"default": "match"}}}, DestinationID: "dest"})
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

// publish accepts, publishes and promotes one Version of recordKey.
func (f *correctionFixture) publish(recordKey, requestKey, text string) (string, string) {
	t, ctx, org, store := f.t, f.ctx, f.org, f.store
	t.Helper()
	cmd := content.Command{Key: requestKey, Source: content.Source{CorpusID: f.corpusID, Namespace: "corrections", RecordKey: recordKey}, Content: content.Text{Kind: "text", Text: text}}
	r, err := f.contents.Accept(ctx, f.scope, cmd)
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := store.Work(ctx, org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Publish(ctx, work, publication(content.Blob{Key: "fixture/" + requestKey, SHA256: "text-" + f.run + requestKey, Size: 10}, content.Blob{Key: "fixture/m-" + requestKey, SHA256: "manifest-" + f.run + requestKey, Size: 2})); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE ingestion_outbox SET dispatched=true WHERE organization=$1`, org); err != nil {
		t.Fatal(err)
	}
	v := content.Version{ID: work.VersionID, RecordID: work.RecordID, Manifest: content.ManifestFor(cmd)}
	seg := wholeBodySegmentation(org, v)
	if err = f.contents.SaveSegmentation(ctx, org, v, seg); err != nil {
		t.Fatal(err)
	}
	generation, err := store.Generation(ctx, org, f.corpusID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.contents.Promote(ctx, org, seg, generation); err != nil {
		t.Fatal(err)
	}
	return work.RecordID, work.VersionID
}

func (f *correctionFixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(f.ctx, query, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *correctionFixture) intent(s monitoring.Subscription, recordID, versionID string) monitoring.Intent {
	return monitoring.Intent{Kind: monitoring.IntentEvaluation, Organization: f.org, SubscriptionID: s.ID, SubscriptionVersionID: s.Current.VersionID, Sequence: 1, CorpusID: f.corpusID, RecordID: recordID, VersionID: versionID}
}

func (f *correctionFixture) evidence(s monitoring.Subscription) monitoring.MatchEvidence {
	return monitoring.MatchEvidence{Evaluator: s.Current.Evaluator, Explanation: "fixture", PartKeys: []string{"body"}}
}

func (f *correctionFixture) commit(want string, do func() (string, error)) {
	f.t.Helper()
	if got, err := do(); err != nil || got != want {
		f.t.Fatalf("commit: want %s, got %s %v", want, got, err)
	}
}

func (f *correctionFixture) matchOf(s monitoring.Subscription, versionID string) string {
	return content.StableID("match", f.org, s.Current.VersionID, versionID)
}

// noticeOf reads the Delivery and its stored notice for one Match and kind.
func (f *correctionFixture) noticeOf(matchID, kind string) (monitoring.Delivery, monitoring.Notice) {
	f.t.Helper()
	var id string
	if err := f.pool.QueryRow(f.ctx, `SELECT id FROM deliveries WHERE organization=$1 AND match_id=$2 AND event_kind=$3`, f.org, matchID, kind).Scan(&id); err != nil {
		f.t.Fatalf("no %s Delivery for %s: %v", kind, matchID, err)
	}
	d, err := f.store.Delivery(f.ctx, f.org, id)
	if err != nil {
		f.t.Fatal(err)
	}
	var n monitoring.Notice
	if err = json.Unmarshal(d.Event, &n); err != nil || n.Type != kind {
		f.t.Fatalf("notice %s %v", d.Event, err)
	}
	return d, n
}
