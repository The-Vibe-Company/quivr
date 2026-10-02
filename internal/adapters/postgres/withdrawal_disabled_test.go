package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
)

// TestWithdrawalNoticeSurvivesDisableAndReenable proves THE-696 against the
// real journal. A Subscription disabled when the withdrawal is dispatched still
// gets its match.withdrawn notice and Delivery, once, whatever the retries,
// re-dispatch and re-enables. Admission parks it without an attempt while the
// Subscription is disabled. Re-enable is idempotent, makes the parked work
// claimable so the notice is admitted, and resumes evaluation from its own
// journal position only. The notice's delivery window opens at the re-enable,
// so a pause longer than the window still delivers it, while a Delivery that
// was already admissible before the disable keeps its original window.
func TestWithdrawalNoticeSurvivesDisableAndReenable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := newCorrectionFixture(t, ctx, "adapter-reenable-")
	sub := f.subscribe("s")
	evaluation := postgres.EvaluationStore{ContentStore: f.store.ContentStore}
	ds := postgres.DeliveryStore{ContentStore: f.store.ContentStore, Organization: f.org}
	configured := func(string, string) bool { return true }
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
	// admitAll claims every due delivery of the Organization once and returns
	// the admission refusal by Delivery ("" when an attempt was admitted),
	// keeping each admitted attempt in attempts.
	attempts := map[string]monitoring.AdmittedAttempt{}
	admitAll := func() map[string]string {
		t.Helper()
		out := map[string]string{}
		for {
			w, err := ds.ClaimDelivery(ctx, time.Minute)
			if errors.Is(err, monitoring.ErrNoWork) {
				return out
			}
			if err != nil {
				t.Fatal(err)
			}
			var a monitoring.AdmittedAttempt
			if a, out[w.DeliveryID], err = ds.Admit(ctx, w, time.Hour, configured); err != nil {
				t.Fatal(err)
			}
			attempts[w.DeliveryID] = a
		}
	}
	withdrawals := func() []monitoring.Intent {
		t.Helper()
		rows, err := f.pool.Query(ctx, `SELECT subscription_id,subscription_version_id,sequence,corpus_id,record_id,record_version_id FROM evaluation_intents WHERE organization=$1 AND kind='withdrawal'`, f.org)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []monitoring.Intent
		for rows.Next() {
			in := monitoring.Intent{Kind: monitoring.IntentWithdrawal, Organization: f.org}
			if err = rows.Scan(&in.SubscriptionID, &in.SubscriptionVersionID, &in.Sequence, &in.CorpusID, &in.RecordID, &in.VersionID); err != nil {
				t.Fatal(err)
			}
			out = append(out, in)
		}
		return out
	}
	notices := func() int {
		return f.count(`SELECT count(*) FROM monitoring_notices WHERE organization=$1 AND kind='match.withdrawn'`, f.org)
	}
	enabledEvents := func() int {
		return f.count(`SELECT count(*) FROM change_events WHERE organization=$1 AND event_type='subscription.enabled' AND resource_id=$2`, f.org, sub.ID)
	}

	// Before the pause: a Match whose match.created notice was delivered, and
	// one on another Record whose notice is still pending.
	record, v1 := f.publish("r", "r-1", "Dépêche r v1")
	kept, keptV := f.publish("k", "k-1", "Dépêche k v1")
	for _, m := range [][2]string{{record, v1}, {kept, keptV}} {
		f.commit(monitoring.OutcomeMatched, func() (string, error) {
			return evaluation.CommitMatch(ctx, f.intent(sub, m[0], m[1]), f.evidence(sub))
		})
	}
	drain()
	if _, err := f.pool.Exec(ctx, `UPDATE evaluation_intents SET state='done',outcome='test' WHERE organization=$1 AND kind='evaluation'`, f.org); err != nil {
		t.Fatal(err)
	}
	created, _ := f.noticeOf(f.matchOf(sub, v1), monitoring.NoticeCreated)
	if _, err := f.pool.Exec(ctx, `UPDATE deliveries SET state='delivered' WHERE organization=$1 AND id=$2`, f.org, created.ID); err != nil {
		t.Fatal(err)
	}

	// Disable, then withdraw: the withdrawal is dispatched for the disabled
	// Subscription and its notice committed once, whatever the retries.
	if _, err := f.service.DisableSubscription(ctx, f.scope, "disable-1", sub.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.contents.Withdraw(ctx, f.scope, content.Withdrawal{Key: "withdraw-r", Source: content.Source{CorpusID: f.corpusID, Namespace: "corrections", RecordKey: "r"}}); err != nil {
		t.Fatal(err)
	}
	drain()
	intents := withdrawals()
	if len(intents) != 1 || intents[0].SubscriptionID != sub.ID || intents[0].VersionID != v1 {
		t.Fatalf("withdrawal intents %+v", intents)
	}
	for _, want := range []string{monitoring.OutcomeWithdrawalNotified, monitoring.OutcomeDuplicate, monitoring.OutcomeDuplicate} {
		f.commit(want, func() (string, error) { return evaluation.CommitWithdrawal(ctx, intents[0]) })
	}
	if n := notices(); n != 1 {
		t.Fatalf("withdrawal notices while disabled: %d", n)
	}
	withdrawn, notice := f.noticeOf(f.matchOf(sub, v1), monitoring.NoticeWithdrawn)
	want := monitoring.NoticeReferences{MatchID: f.matchOf(sub, v1), RecordID: record, RecordVersionID: v1, SubscriptionID: sub.ID, SubscriptionVersionID: sub.Current.VersionID, DeliveryID: withdrawn.ID}
	if notice.References != want || withdrawn.State != "pending" || withdrawn.Admission.Reason != "subscription_disabled" {
		t.Fatalf("match.withdrawn while disabled: %+v %s %+v", notice.References, withdrawn.State, withdrawn.Admission)
	}
	// Before its first claim parks it, the dormant notice is not backlog work
	// and never yields a negative age.
	if b, err := ds.DeliveryBacklog(ctx); err != nil || b.OldestAge < 0 {
		t.Fatalf("backlog with a dormant notice: %+v %v", b, err)
	}
	// While disabled, admission parks both without an attempt.
	pending, _ := f.noticeOf(f.matchOf(sub, keptV), monitoring.NoticeCreated)
	if got := admitAll(); got[withdrawn.ID] != "subscription_disabled" || got[pending.ID] != "subscription_disabled" {
		t.Fatalf("admission while disabled %v", got)
	}
	if n := f.count(`SELECT count(*) FROM delivery_attempts WHERE organization=$1`, f.org); n != 0 {
		t.Fatalf("attempts while disabled: %d", n)
	}
	if got := admitAll(); len(got) != 0 {
		t.Fatalf("parked work claimed again %v", got)
	}

	// A Version published during the pause is never evaluated, even though
	// it is only dispatched after the re-enable.
	paused, pausedV := f.publish("p", "p-1", "Dépêche p pendant la pause")

	// The pause outlasts the one-hour window used below.
	if _, err := f.pool.Exec(ctx, `UPDATE deliveries SET created_at=created_at-interval '2 hours' WHERE organization=$1`, f.org); err != nil {
		t.Fatal(err)
	}

	// Re-enable once; replay and a repeat with another key commit nothing more.
	reenabled, err := f.service.EnableSubscription(ctx, f.scope, "enable-1", sub.ID)
	if err != nil || !reenabled.Enabled {
		t.Fatalf("enable %+v %v", reenabled, err)
	}
	for _, key := range []string{"enable-1", "enable-2"} {
		if s, err := f.service.EnableSubscription(ctx, f.scope, key, sub.ID); err != nil || !s.Enabled {
			t.Fatalf("repeat enable %s: %+v %v", key, s, err)
		}
	}
	if _, err = f.service.DisableSubscription(ctx, f.scope, "disable-1", sub.ID); err != nil {
		t.Fatal(err) // the original disable replayed: still enabled
	}
	if s, _ := f.service.Subscription(ctx, f.scope, sub.ID); !s.Enabled {
		t.Fatal("replayed disable disabled the re-enabled Subscription")
	}
	if n := enabledEvents(); n != 1 {
		t.Fatalf("subscription.enabled events: %d", n)
	}

	// The paused Version is not evaluated; the parked notice is admitted.
	drain()
	if n := f.count(`SELECT count(*) FROM evaluation_intents WHERE organization=$1 AND kind='evaluation' AND record_id=$2`, f.org, paused); n != 0 {
		t.Fatalf("paused Version dispatched: %d intents", n)
	}
	// An intent from before the re-enable is refused at commit as well.
	stale := f.intent(sub, paused, pausedV)
	f.commit(monitoring.OutcomeSubscriptionDisabled, func() (string, error) { return evaluation.CommitMatch(ctx, stale, f.evidence(sub)) })
	// The withdrawal notice's window opened at the re-enable: it is admitted.
	// The notice already admissible before the pause keeps its window: elapsed.
	if got := admitAll(); len(got) != 2 || got[withdrawn.ID] != "" || got[pending.ID] != "window_elapsed" {
		t.Fatalf("admission after re-enable %v", got)
	}
	if pending, _ = f.noticeOf(f.matchOf(sub, keptV), monitoring.NoticeCreated); pending.State != "exhausted" || pending.AttemptCount != 0 {
		t.Fatalf("pre-pause notice after re-enable: %s, %d attempts", pending.State, pending.AttemptCount)
	}
	if withdrawn, _ = f.noticeOf(f.matchOf(sub, v1), monitoring.NoticeWithdrawn); withdrawn.State != "delivering" || withdrawn.AttemptCount != 1 {
		t.Fatalf("withdrawal notice after re-enable: %s, %d attempts", withdrawn.State, withdrawn.AttemptCount)
	}
	// Its window is one window from the re-enable: a failed attempt's retry is
	// capped there, not left open-ended.
	if err = ds.Record(ctx, attempts[withdrawn.ID], monitoring.AttemptOutcome{Outcome: monitoring.AttemptRetryableError, HTTPStatus: 503}, monitoring.Retry{Delay: 2 * time.Hour, Window: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if withdrawn, _ = f.noticeOf(f.matchOf(sub, v1), monitoring.NoticeWithdrawn); withdrawn.State != "pending" || withdrawn.NextAttemptAt == nil || withdrawn.NextAttemptAt.After(time.Now().Add(90*time.Minute)) {
		t.Fatalf("retry of the re-enabled withdrawal notice: %s, next %v", withdrawn.State, withdrawn.NextAttemptAt)
	}

	// Later disable/enable cycles, retries and a repeated withdrawal commit no second notice.
	if _, err = f.service.DisableSubscription(ctx, f.scope, "disable-2", sub.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.EnableSubscription(ctx, f.scope, "enable-3", sub.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.contents.Withdraw(ctx, f.scope, content.Withdrawal{Key: "withdraw-r-again", Source: content.Source{CorpusID: f.corpusID, Namespace: "corrections", RecordKey: "r"}}); err != nil {
		t.Fatal(err)
	}
	drain()
	f.commit(monitoring.OutcomeDuplicate, func() (string, error) { return evaluation.CommitWithdrawal(ctx, intents[0]) })
	if n, m := notices(), len(withdrawals()); n != 1 || m != 1 {
		t.Fatalf("after re-enable cycles: %d notices, %d intents", n, m)
	}
	if n := enabledEvents(); n != 2 {
		t.Fatalf("subscription.enabled events after second cycle: %d", n)
	}

	// A Version published after the re-enable is evaluated again.
	fresh, _ := f.publish("n", "n-1", "Dépêche n après la reprise")
	drain()
	if n := f.count(`SELECT count(*) FROM evaluation_intents WHERE organization=$1 AND kind='evaluation' AND record_id=$2 AND subscription_id=$3`, f.org, fresh, sub.ID); n != 1 {
		t.Fatalf("post re-enable Version intents: %d", n)
	}
}
