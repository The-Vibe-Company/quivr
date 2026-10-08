package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/monitoring"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
)

// This owns the cross-Version transaction boundary. Single-Version refusal,
// retirement, replay and rollback remain owned by the atomic match test.
func TestMatchGroupSharesJournalAcrossRecordVersions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := newCorrectionFixture(t, ctx, "adapter-match-group-")
	sub := f.subscribe("s")
	evaluation := postgres.EvaluationStore{Pool: f.pool}
	intent := func(record, version string) monitoring.Intent {
		t.Helper()
		for {
			n, err := evaluation.FanOut(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if n == 0 {
				break
			}
		}
		in := f.intent(sub, record, version)
		if err := f.pool.QueryRow(ctx, `SELECT sequence FROM evaluation_intents WHERE organization=$1 AND record_version_id=$2 AND subscription_version_id=$3 ORDER BY sequence DESC LIMIT 1`, f.org, version, sub.Current.VersionID).Scan(&in.Sequence); err != nil {
			t.Fatal(err)
		}
		return in
	}
	head := func() int64 {
		t.Helper()
		var n int64
		if err := f.pool.QueryRow(ctx, `SELECT last_sequence FROM organization_journals WHERE organization=$1`, f.org).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	r1, v1 := f.publish("first", "first", "First article")
	r2, v2 := f.publish("second", "second", "Second article")
	group := []monitoring.MatchCommit{{Intent: intent(r1, v1), Evidence: f.evidence(sub)}, {Intent: intent(r2, v2), Evidence: f.evidence(sub)}}
	for i, id := range []string{"11111111111111111111111111111111", "33333333333333333333333333333333"} {
		group[i].Context = telemetry.Extract(ctx, http.Header{"Traceparent": {"00-" + id + "-2222222222222222-01"}})
	}
	// A repeated positive for the first Version must not suppress the same
	// Subscription's positive on another Version in this transaction.
	group = append(group, group[0])
	before := head()
	outcomes, err := evaluation.CommitMatches(ctx, group)
	if err != nil || fmt.Sprint(outcomes) != "[matched matched duplicate]" {
		t.Fatalf("cross-Version outcomes: %v %v", outcomes, err)
	}
	window, err := f.store.ReadChanges(ctx, f.org, f.corpusID, before, 10, time.Hour)
	if err != nil || len(window.Events) != 2 || head() != before+2 {
		t.Fatalf("cross-Version feed: %+v %v head=%d", window, err, head())
	}
	for i, version := range []string{v1, v2} {
		id := f.matchOf(sub, version)
		match, err := f.store.Match(ctx, f.org, id)
		if err != nil || match.RecordVersionID != version || match.Position != before+int64(i)+1 {
			t.Fatalf("cross-Version Match: %+v %v", match, err)
		}
		delivery, notice := f.noticeOf(id, monitoring.NoticeCreated)
		if notice.References.RecordVersionID != version || delivery.MatchID != id || window.Events[i].ResourceID != id || !notice.OccurredAt.Equal(window.Events[0].OccurredAt) {
			t.Fatalf("cross-Version notice: %+v, event %+v", notice, window.Events[i])
		}
		var eventTrace, deliveryTrace string
		if err := f.pool.QueryRow(ctx, `SELECT e.trace_context,d.trace_context FROM change_events e JOIN delivery_outbox d ON d.organization=e.organization AND d.delivery_id=$3 WHERE e.organization=$1 AND e.event_id=$2`, f.org, window.Events[i].ID, delivery.ID).Scan(&eventTrace, &deliveryTrace); err != nil || eventTrace != telemetry.Encode(group[i].Context) || deliveryTrace != eventTrace {
			t.Fatalf("member %d lost its durable trace: event=%q delivery=%q err=%v", i, eventTrace, deliveryTrace, err)
		}
	}
	if f.count(`SELECT count(*) FROM matches WHERE organization=$1`, f.org) != 2 || f.count(`SELECT count(*) FROM deliveries WHERE organization=$1`, f.org) != 2 || f.count(`SELECT count(*) FROM monitoring_notices WHERE organization=$1`, f.org) != 2 {
		t.Fatal("cross-Version group lost or duplicated durable facts")
	}
	if outcomes, err = evaluation.CommitMatches(ctx, group); err != nil || fmt.Sprint(outcomes) != "[duplicate duplicate duplicate]" || head() != before+2 {
		t.Fatalf("cross-Version replay: %v %v", outcomes, err)
	}

	// A prior Match on one Record forces the established correction path for
	// the whole group, while an unrelated new Record still commits in order.
	r3, v3 := f.publish("third", "third", "Third article")
	_, correction := f.publish("first", "first-correction", "Corrected first article")
	group = []monitoring.MatchCommit{{Intent: intent(r3, v3), Evidence: f.evidence(sub)}, {Intent: intent(r1, correction), Evidence: f.evidence(sub)}}
	before = head()
	outcomes, err = evaluation.CommitMatches(ctx, group)
	if err != nil || fmt.Sprint(outcomes) != "[matched matched]" {
		t.Fatalf("cross-Version correction fallback: %v %v", outcomes, err)
	}
	window, err = f.store.ReadChanges(ctx, f.org, f.corpusID, before, 10, time.Hour)
	if err != nil || len(window.Events) != 2 || window.Events[0].Type != monitoring.NoticeCreated || window.Events[1].Type != monitoring.NoticeCorrected || head() != before+2 {
		t.Fatalf("fallback feed: %+v %v", window, err)
	}
	_, notice := f.noticeOf(f.matchOf(sub, correction), monitoring.NoticeCorrected)
	if notice.References.PreviousMatchID != f.matchOf(sub, v1) || notice.References.RecordVersionID != correction {
		t.Fatalf("fallback predecessor: %+v", notice.References)
	}

	r4, v4 := f.publish("canceled", "canceled", "Canceled group")
	r5, v5 := f.publish("healthy", "healthy", "Healthy group")
	member, stop := context.WithCancel(ctx)
	group = []monitoring.MatchCommit{{Context: member, Intent: intent(r4, v4), Evidence: f.evidence(sub)}, {Intent: intent(r5, v5), Evidence: f.evidence(sub)}}
	stop()
	before = head()
	if _, err = evaluation.CommitMatches(ctx, group); !errors.Is(err, context.Canceled) || head() != before || f.count(`SELECT count(*) FROM matches WHERE organization=$1 AND record_version_id=ANY($2::text[])`, f.org, []string{v4, v5}) != 0 {
		t.Fatalf("canceled member exposed a shared commit: %v head=%d", err, head())
	}
	if outcomes, err = evaluation.CommitMatches(ctx, group[1:]); err != nil || fmt.Sprint(outcomes) != "[matched]" {
		t.Fatalf("healthy group could not commit independently: %v %v", outcomes, err)
	}
}
