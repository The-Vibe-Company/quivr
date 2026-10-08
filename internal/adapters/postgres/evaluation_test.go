package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost/fakeplugin"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/monitoring"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/jackc/pgx/v5/pgconn"
	"net/http"
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
	ctx = telemetry.Extract(ctx, http.Header{"Traceparent": {"00-11111111111111111111111111111111-2222222222222222-01"}})
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
	store := contentStores(pool)
	contents := content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store, Baseline: store}
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
	service := monitoring.Service{Evaluators: fakeplugin.FixtureEvaluators(), Store: store, Corpora: store, Destinations: map[string]monitoring.Destination{"dest": {Organization: org}}}
	q, err := service.CreateSavedQuery(ctx, scope, monitoring.SavedQueryInput{Key: "q", Name: "Q", Definition: monitoring.Definition{CorpusIDs: []string{a.ID}, Expression: map[string]any{}, RetrievalProfile: "default", TemporalPolicy: "from_activation"}})
	if err != nil {
		t.Fatal(err)
	}
	subs := make([]monitoring.Subscription, 3)
	for i := range subs {
		if subs[i], err = service.CreateSubscription(ctx, scope, monitoring.SubscriptionInput{Key: fmt.Sprint("s", i), Name: fmt.Sprint("S", i), SavedQueryID: q.ID, SavedQueryVersionID: q.Current.VersionID,
			Evaluator: monitoring.Evaluator{PluginID: fakeplugin.FixtureEvaluator, Version: fakeplugin.FixtureEvaluatorVersion, Configuration: map[string]any{"decisions": map[string]any{"default": "match"}}}, DestinationID: "dest"}); err != nil {
			t.Fatal(err)
		}
	}
	hitRecord, hitVersion := searchable(a.ID, "hit")
	_, otherVersion := searchable(b.ID, "other-corpus")
	trigger("record.retrieval_ready", a.ID, hitRecord, nil) // pre-migration shape: no Version

	evaluation := postgres.EvaluationStore{Pool: store.Pool, Page: 2}
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
	// Alert evaluation applies where a Subscription covers the Corpus, so the
	// admin views wait for an evaluated step only there.
	evaluationOf := func(versionID string) string {
		t.Helper()
		activity, err := store.VersionActivity(ctx, org, versionID)
		if err != nil {
			t.Fatal(err)
		}
		return activity.Evaluation
	}
	if got := evaluationOf(hitVersion); got != content.EvaluationApplicable {
		t.Fatalf("watched Corpus: evaluation %q", got)
	}
	if got := evaluationOf(otherVersion); got != content.EvaluationNotApplicable {
		t.Fatalf("unwatched Corpus: evaluation %q", got)
	}

	// A later enrichment trigger of the same Version is new evaluation work.
	trigger("record.enrichment_available", a.ID, hitRecord, hitVersion)
	drain()
	if got := intents(hitVersion); got != 6 {
		t.Fatalf("enrichment trigger: %d intents", got)
	}

	// Claims lease due work; a leased intent is not claimed again until it
	// expires. While one worker evaluates a Version, other Claims leave that
	// Version's intents alone: the worker takes the other Subscriptions'
	// intents with ClaimRelated (both triggers of a pair may join the same
	// group, where they share one evaluation), and the leased Subscription's
	// second intent (the enrichment trigger) waits for the first to decide.
	var first monitoring.Intent
	for i := 0; i < 1000; i++ {
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
		if first.Organization != "" || in.VersionID != hitVersion || in.RecordID != hitRecord || in.CorpusID != a.ID {
			t.Fatalf("claimed %+v beside %+v, or with wrong references", in, first)
		}
		first = in
	}
	if first.Organization == "" {
		t.Fatal("no intent claimed")
	}
	related, err := evaluation.ClaimRelated(ctx, first, subs[0].Current.Evaluator, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	pairs := map[string]bool{first.SubscriptionVersionID: true}
	for _, in := range related {
		if in.SubscriptionVersionID == first.SubscriptionVersionID || in.VersionID != hitVersion {
			t.Fatalf("related %+v repeats the leased Subscription Version or is another Version", in)
		}
		pairs[in.SubscriptionVersionID] = true
	}
	if len(pairs) != 3 {
		t.Fatalf("want the 3 Subscriptions of the Version in one group, got %d", len(pairs))
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

	// Enrichment asks again only the Subscriptions that have not decided the
	// Version: one decided no_match, the other answered not_ready.
	rows, err := pool.Query(ctx, `SELECT subscription_version_id,sequence FROM evaluation_intents WHERE organization=$1 AND record_version_id=$2 ORDER BY subscription_version_id`, org, laterVersion)
	if err != nil {
		t.Fatal(err)
	}
	var later []monitoring.Intent
	for rows.Next() {
		in := monitoring.Intent{Organization: org}
		if err = rows.Scan(&in.SubscriptionVersionID, &in.Sequence); err != nil {
			t.Fatal(err)
		}
		later = append(later, in)
	}
	rows.Close()
	// The Version's evaluated step is recorded once every Subscription asked
	// has decided: a not_ready answer waits for the vectors' round.
	evaluated := func() *time.Time {
		t.Helper()
		activity, err := store.VersionActivity(ctx, org, laterVersion)
		if err != nil {
			t.Fatal(err)
		}
		return activity.Steps.Evaluated
	}
	if err = evaluation.Complete(ctx, later[0], monitoring.OutcomeNoMatch); err != nil {
		t.Fatal(err)
	}
	if at := evaluated(); at != nil {
		t.Fatalf("evaluated at %v while a Subscription has not decided", at)
	}
	if err = evaluation.Complete(ctx, later[1], monitoring.OutcomeNotReady); err != nil {
		t.Fatal(err)
	}
	if at := evaluated(); at != nil {
		t.Fatalf("evaluated at %v while a Subscription waits for vectors", at)
	}
	trigger("record.enrichment_available", a.ID, laterRecord, laterVersion)
	drain()
	if got := count(`SELECT count(*) FROM evaluation_intents WHERE organization=$1 AND record_version_id=$2 AND subscription_version_id=$3`, org, laterVersion, later[0].SubscriptionVersionID); got != 1 {
		t.Fatalf("a decided Subscription was asked again on enrichment: %d intents", got)
	}
	if got := count(`SELECT count(*) FROM evaluation_intents WHERE organization=$1 AND record_version_id=$2 AND subscription_version_id=$3`, org, laterVersion, later[1].SubscriptionVersionID); got != 2 {
		t.Fatalf("a not_ready Subscription must be asked again on enrichment: %d intents", got)
	}
	// Any other intent of the decided pair is admitted as already decided.
	for i, want := range []bool{true, false} {
		target, err := evaluation.Target(ctx, monitoring.Intent{Organization: org, SubscriptionVersionID: later[i].SubscriptionVersionID, Sequence: -1, CorpusID: a.ID, RecordID: laterRecord, VersionID: laterVersion})
		if err != nil || target.Decided != want {
			t.Fatalf("intent %d: decided %v, want %v (%v)", i, target.Decided, want, err)
		}
	}
	var retry monitoring.Intent
	if err = pool.QueryRow(ctx, `SELECT subscription_id,subscription_version_id,sequence,trace_context FROM evaluation_intents WHERE organization=$1 AND record_version_id=$2 AND state='pending'`, org, laterVersion).Scan(&retry.SubscriptionID, &retry.SubscriptionVersionID, &retry.Sequence, &retry.TraceContext); err != nil {
		t.Fatal(err)
	}
	retry.Kind, retry.Organization, retry.CorpusID, retry.RecordID, retry.VersionID = monitoring.IntentEvaluation, org, a.ID, laterRecord, laterVersion
	resolved, err := evaluation.CommitMatches(ctx, []monitoring.MatchCommit{{Intent: retry, Evidence: evidence}})
	if err != nil || len(resolved) != 1 || resolved[0] != monitoring.OutcomeMatched {
		t.Fatalf("positive decision after not_ready: outcomes=%v err=%v", resolved, err)
	}
	if evaluated() == nil {
		t.Fatal("evaluated step missing once every Subscription decided")
	}

	// Fail a later Delivery insert after the batch has written Matches and
	// journal positions, proving transaction rollback across statements.
	groupRecord, groupVersion := searchable(a.ID, "group")
	drain()
	rows, err = pool.Query(ctx, `SELECT subscription_id,subscription_version_id,sequence,trace_context FROM evaluation_intents WHERE organization=$1 AND record_version_id=$2`, org, groupVersion)
	if err != nil {
		t.Fatal(err)
	}
	groupIntents := map[string]monitoring.Intent{}
	for rows.Next() {
		in := monitoring.Intent{Kind: monitoring.IntentEvaluation, Organization: org, CorpusID: a.ID, RecordID: groupRecord, VersionID: groupVersion}
		if err = rows.Scan(&in.SubscriptionID, &in.SubscriptionVersionID, &in.Sequence, &in.TraceContext); err != nil {
			t.Fatal(err)
		}
		if in.TraceContext != telemetry.Encode(ctx) {
			t.Fatalf("prefix fanout lost its event parent: %q", in.TraceContext)
		}
		groupIntents[in.SubscriptionID] = in
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(groupIntents) != 2 {
		t.Fatalf("group dispatch: got %d intents, want 2 enabled Subscriptions", len(groupIntents))
	}
	group := []monitoring.MatchCommit{{Intent: groupIntents[subs[2].ID], Evidence: evidence}, {Intent: groupIntents[subs[0].ID], Evidence: evidence}}
	head := func() int64 {
		t.Helper()
		var n int64
		if err := pool.QueryRow(ctx, `SELECT last_sequence FROM organization_journals WHERE organization=$1`, org).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	facts := func() map[string]int {
		t.Helper()
		out := map[string]int{}
		for _, table := range []string{"matches", "deliveries", "monitoring_notices", "delivery_outbox", "change_events"} {
			out[table] = count(`SELECT count(*) FROM `+table+` WHERE organization=$1`, org)
		}
		return out
	}
	beforeHead, beforeFacts := head(), facts()
	if _, err = pool.Exec(ctx, `CREATE FUNCTION fail_fixture_match_delivery() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.organization=TG_ARGV[0] THEN
  IF NOT EXISTS(SELECT 1 FROM matches WHERE organization=NEW.organization AND id=NEW.match_id) THEN RAISE EXCEPTION 'match writes absent'; END IF;
  RAISE EXCEPTION 'synthetic later match interruption';
 END IF; RETURN NEW; END $$;`+fmt.Sprintf(`CREATE TRIGGER fail_fixture_match_delivery BEFORE INSERT ON deliveries FOR EACH ROW EXECUTE FUNCTION fail_fixture_match_delivery('%s')`, org)); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), "DROP TRIGGER IF EXISTS fail_fixture_match_delivery ON deliveries; DROP FUNCTION IF EXISTS fail_fixture_match_delivery()")
	var sqlError *pgconn.PgError
	if _, err = evaluation.CommitMatches(ctx, group); !errors.As(err, &sqlError) || sqlError.Code != "P0001" || sqlError.Message != "synthetic later match interruption" {
		t.Fatalf("group must reach later Delivery SQL failure: %v", err)
	}
	if got := head(); got != beforeHead {
		t.Fatalf("failed group exposed journal head %d, want %d", got, beforeHead)
	}
	for table, got := range facts() {
		if got != beforeFacts[table] {
			t.Fatalf("failed group committed %s facts: got %d, want %d", table, got, beforeFacts[table])
		}
	}
	if got := count(`SELECT count(*) FROM evaluation_intents WHERE organization=$1 AND record_version_id=$2 AND state='pending'`, org, groupVersion); got != 2 {
		t.Fatalf("failed group left %d pending intents, want both retryable", got)
	}
	activity, err := store.VersionActivity(ctx, org, groupVersion)
	if err != nil || activity.Steps.Evaluated != nil {
		t.Fatalf("failed group recorded evaluation: %+v %v", activity.Steps, err)
	}

	if _, err = pool.Exec(ctx, "DROP TRIGGER fail_fixture_match_delivery ON deliveries; DROP FUNCTION fail_fixture_match_delivery()"); err != nil {
		t.Fatal(err)
	}

	// Retrieval and enrichment can put two intents for one Subscription in
	// the same positive group. Only its first eligible positive creates a
	// Match; both intents must complete before the evaluated step is recorded.
	trigger("record.enrichment_available", a.ID, groupRecord, groupVersion)
	drain()
	var enrichmentSequence int64
	if err = pool.QueryRow(ctx, `SELECT max(sequence) FROM evaluation_intents WHERE organization=$1 AND record_version_id=$2`, org, groupVersion).Scan(&enrichmentSequence); err != nil || enrichmentSequence <= group[0].Intent.Sequence {
		t.Fatalf("distinct enrichment intent sequence %d after retrieval %d: %v", enrichmentSequence, group[0].Intent.Sequence, err)
	}
	if got := count(`SELECT count(*) FROM evaluation_intents WHERE organization=$1 AND record_version_id=$2 AND sequence=$3`, org, groupVersion, enrichmentSequence); got != 2 {
		t.Fatalf("enrichment dispatched %d intents, want 2", got)
	}
	enrichedSecond, enrichedFirst := group[0], group[1]
	enrichedSecond.Intent.Sequence, enrichedFirst.Intent.Sequence = enrichmentSequence, enrichmentSequence
	// Earlier refused and retired inputs for that same Subscription cannot
	// suppress its eligible enrichment decision. The other Subscription's
	// retrieval creates its Match and its enrichment becomes a duplicate.
	refusedEarlier := group[0]
	refusedEarlier.Intent.Sequence = 0 // precedes the Subscription's enabled boundary
	if err = evaluation.Complete(ctx, group[0].Intent, monitoring.OutcomeEvaluatorRetired); err != nil {
		t.Fatal(err)
	}
	beforeHead, beforeFacts = head(), facts()
	disabled := group[0].Intent
	disabled.SubscriptionID, disabled.SubscriptionVersionID = subs[1].ID, subs[1].Current.VersionID
	group = []monitoring.MatchCommit{refusedEarlier, group[0], enrichedSecond, {Intent: disabled, Evidence: evidence}, group[1], enrichedFirst}
	// Both new Matches share transaction time and consecutive journal positions
	// in input order; immutable bodies retain the existing field encoding.
	outcomes, err := evaluation.CommitMatches(ctx, group)
	if err != nil || fmt.Sprint(outcomes) != fmt.Sprint([]string{monitoring.OutcomeSubscriptionDisabled, monitoring.OutcomeEvaluatorRetired, monitoring.OutcomeMatched, monitoring.OutcomeSubscriptionDisabled, monitoring.OutcomeMatched, monitoring.OutcomeDuplicate}) {
		t.Fatalf("group outcomes: %v %v", outcomes, err)
	}
	if got := head(); got != beforeHead+2 {
		t.Fatalf("group journal head: got %d, want %d", got, beforeHead+2)
	}
	for table, got := range facts() {
		if got != beforeFacts[table]+2 {
			t.Fatalf("committed group %s facts: got %d, want %d", table, got, beforeFacts[table]+2)
		}
	}
	if got := count(`SELECT count(*) FROM evaluation_intents WHERE organization=$1 AND record_version_id=$2 AND state='done' AND outcome='matched'`, org, groupVersion); got != 2 {
		t.Fatalf("committed group completed %d intents, want 2", got)
	}
	if got := count(`SELECT count(*) FROM evaluation_intents WHERE organization=$1 AND record_version_id=$2 AND state='done' AND outcome='duplicate'`, org, groupVersion); got != 1 {
		t.Fatalf("second positive intent completed %d duplicates, want 1", got)
	}
	if got := count(`SELECT count(*) FROM evaluation_intents WHERE organization=$1 AND record_version_id=$2 AND state='done' AND outcome='evaluator_retired'`, org, groupVersion); got != 1 {
		t.Fatalf("late positive changed retirement: got %d retired intents, want 1", got)
	}
	if got := count(`SELECT count(*) FROM evaluation_intents WHERE organization=$1 AND record_version_id=$2 AND state='pending'`, org, groupVersion); got != 0 {
		t.Fatalf("group left %d retrieval/enrichment intents pending, want none", got)
	}
	activity, err = store.VersionActivity(ctx, org, groupVersion)
	if err != nil || activity.Steps.Evaluated == nil {
		t.Fatalf("committed group omitted evaluation: %+v %v", activity.Steps, err)
	}
	window, err := store.ReadChanges(ctx, org, a.ID, beforeHead, 10, time.Hour)
	if err != nil || len(window.Events) != 2 {
		t.Fatalf("group feed: %+v %v", window, err)
	}
	for i, match := range []monitoring.MatchCommit{group[2], group[4]} {
		in := match.Intent
		id := content.StableID("match", org, in.SubscriptionVersionID, groupVersion)
		deliveryID := content.StableID("delivery", org, id, "dest", monitoring.NoticeCreated)
		eventID := content.StableID("event", org, monitoring.NoticeCreated, "match", id)
		m, err := store.Match(ctx, org, id)
		if err != nil || m.Position != beforeHead+int64(i)+1 || m.SavedQueryID != q.ID || m.SavedQueryVersionID != q.Current.VersionID || m.PreviousMatchID != "" {
			t.Fatalf("group Match %s: %+v %v", id, m, err)
		}
		e := window.Events[i]
		if e.ID != eventID || e.Type != monitoring.NoticeCreated || e.ResourceID != id || e.ResourceKind != "match" || e.Monitoring == nil || e.Monitoring.DeliveryID != deliveryID || !e.OccurredAt.Equal(window.Events[0].OccurredAt) {
			t.Fatalf("group feed event %d: %+v", i, e)
		}
		var parent string
		if err := pool.QueryRow(ctx, "SELECT trace_context FROM delivery_outbox WHERE organization=$1 AND delivery_id=$2", org, deliveryID).Scan(&parent); err != nil || parent != telemetry.Encode(ctx) {
			t.Fatalf("batch match lost durable delivery parent: %q %v", parent, err)
		}
		delivery, err := store.Delivery(ctx, org, deliveryID)
		if err != nil || delivery.State != "pending" || delivery.AttemptCount != 0 || !delivery.Admission.Allowed {
			t.Fatalf("group Delivery %s: %+v %v", deliveryID, delivery, err)
		}
		wantBody, err := json.Marshal(monitoring.Notice{EventID: eventID, Type: monitoring.NoticeCreated, SchemaVersion: "1", OccurredAt: e.OccurredAt.UTC(), References: monitoring.NoticeReferences{MatchID: id, RecordID: groupRecord, RecordVersionID: groupVersion, SubscriptionID: in.SubscriptionID, SubscriptionVersionID: in.SubscriptionVersionID, DeliveryID: deliveryID}})
		if err != nil || string(delivery.Event) != string(wantBody) {
			t.Fatalf("group notice bytes: got %s, want %s (%v)", delivery.Event, wantBody, err)
		}
	}
	if outcomes, err = evaluation.CommitMatches(ctx, group); err != nil || fmt.Sprint(outcomes) != fmt.Sprint([]string{monitoring.OutcomeSubscriptionDisabled, monitoring.OutcomeEvaluatorRetired, monitoring.OutcomeDuplicate, monitoring.OutcomeSubscriptionDisabled, monitoring.OutcomeDuplicate, monitoring.OutcomeDuplicate}) || head() != beforeHead+2 {
		t.Fatalf("group replay: %v %v, head %d", outcomes, err, head())
	}

	// Once no enabled Subscription covers the Corpus, a new Version has no
	// evaluation coming; an evaluated one keeps its evaluated step.
	for i, sub := range []monitoring.Subscription{subs[0], subs[2]} {
		if _, err = service.DisableSubscription(ctx, scope, fmt.Sprint("disable-all-", i), sub.ID); err != nil {
			t.Fatal(err)
		}
	}
	_, unwatched := searchable(a.ID, "unwatched")
	drain()
	if got := evaluationOf(unwatched); got != content.EvaluationNotApplicable {
		t.Fatalf("every Subscription disabled: evaluation %q", got)
	}
	if got := evaluationOf(groupVersion); got != content.EvaluationApplicable {
		t.Fatalf("evaluated Version: evaluation %q", got)
	}
}

// TestEvaluationFanOutEventBudgetAndPrefixRollback owns the per-call event
// budget and atomic multi-event dispatch boundary; pagination eligibility is
// covered by TestEvaluationDispatchAndAtomicMatchCommit.
func TestEvaluationFanOutEventBudgetAndPrefixRollback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	first := newCorrectionFixture(t, ctx, "adapter-dispatch-a-")
	second := newCorrectionFixture(t, ctx, "adapter-dispatch-b-")
	fixtures := []*correctionFixture{first, second}
	for _, f := range fixtures {
		f.subscribe("s")
	}
	evaluation := postgres.EvaluationStore{Pool: first.pool}
	// Initialize both real checkpoints before any trigger can be dispatched.
	if _, err := evaluation.FanOut(ctx); err != nil {
		t.Fatal(err)
	}
	checkpoint := func(f *correctionFixture) int64 {
		t.Helper()
		var position int64
		var after string
		if err := f.pool.QueryRow(ctx, `SELECT position,subscription_after FROM monitoring_checkpoints WHERE organization=$1`, f.org).Scan(&position, &after); err != nil || after != "" {
			t.Fatalf("checkpoint for %s: position %d after %q, err %v", f.org, position, after, err)
		}
		return position
	}
	initial := map[string]int64{}
	for _, f := range fixtures {
		initial[f.org] = checkpoint(f)
		record, version := f.publish("r", "r-1", "Neutral dispatch fixture")
		// One real promotion plus 52 subsequent trigger events exercises the
		// 50-event call boundary without 53 copies of publication setup.
		if _, err := f.pool.Exec(ctx, `WITH positions AS (
 UPDATE organization_journals SET last_sequence=last_sequence+52 WHERE organization=$1 RETURNING last_sequence
) INSERT INTO change_events(organization,sequence,event_id,corpus_id,event_type,resource_type,resource_id,record_version_id)
 SELECT $1,p.last_sequence-52+n,$2||n,$3,'record.retrieval_ready','record',$4,$5
 FROM positions p CROSS JOIN generate_series(1,52) n`, f.org, "event_dispatch_"+f.run+"_", f.corpusID, record, version); err != nil {
			t.Fatal(err)
		}
	}
	var failSequence int64
	if err := first.pool.QueryRow(ctx, `SELECT sequence FROM change_events WHERE organization=$1 AND event_type='record.retrieval_ready' ORDER BY sequence LIMIT 1 OFFSET 1`, first.org).Scan(&failSequence); err != nil {
		t.Fatal(err)
	}
	// Fail a real later intent INSERT after its earlier sibling was attempted.
	if _, err := first.pool.Exec(ctx, `CREATE FUNCTION fail_fixture_dispatch_intent() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.organization=TG_ARGV[0] AND NEW.sequence=TG_ARGV[1]::bigint THEN RAISE EXCEPTION 'synthetic later dispatch interruption'; END IF;
 RETURN NEW; END $$;`+fmt.Sprintf(`CREATE TRIGGER fail_fixture_dispatch_intent BEFORE INSERT ON evaluation_intents FOR EACH ROW EXECUTE FUNCTION fail_fixture_dispatch_intent('%s','%d')`, first.org, failSequence)); err != nil {
		t.Fatal(err)
	}
	defer first.pool.Exec(context.Background(), "DROP TRIGGER IF EXISTS fail_fixture_dispatch_intent ON evaluation_intents; DROP FUNCTION IF EXISTS fail_fixture_dispatch_intent()")
	var sqlError *pgconn.PgError
	if _, err := evaluation.FanOut(ctx); !errors.As(err, &sqlError) || sqlError.Code != "P0001" || sqlError.Message != "synthetic later dispatch interruption" {
		t.Fatalf("dispatch must reach later-event SQL failure: %v", err)
	}
	for _, f := range fixtures {
		if got := checkpoint(f); got != initial[f.org] {
			t.Fatalf("failed prefix advanced %s checkpoint to %d, want %d", f.org, got, initial[f.org])
		}
		if n := f.count(`SELECT count(*) FROM evaluation_intents WHERE organization=$1`, f.org); n != 0 {
			t.Fatalf("failed prefix exposed %d intents for %s", n, f.org)
		}
	}
	if _, err := first.pool.Exec(ctx, "DROP TRIGGER fail_fixture_dispatch_intent ON evaluation_intents; DROP FUNCTION fail_fixture_dispatch_intent()"); err != nil {
		t.Fatal(err)
	}
	for call, want := range []int{50, 53, 53} {
		if _, err := evaluation.FanOut(ctx); err != nil {
			t.Fatal(err)
		}
		for _, f := range fixtures {
			if n := f.count(`SELECT count(*) FROM evaluation_intents WHERE organization=$1`, f.org); n != want {
				t.Fatalf("call %d dispatched %d events for %s, want %d", call+1, n, f.org, want)
			}
			var through int64
			if err := f.pool.QueryRow(ctx, `SELECT sequence FROM change_events WHERE organization=$1 AND event_type='record.retrieval_ready' ORDER BY sequence LIMIT 1 OFFSET $2`, f.org, want-1).Scan(&through); err != nil {
				t.Fatal(err)
			}
			if got := checkpoint(f); got != through {
				t.Fatalf("call %d checkpoint for %s = %d, want %d", call+1, f.org, got, through)
			}
		}
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
