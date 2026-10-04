package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/monitoring"
)

var fail503 = monitoring.AttemptOutcome{Outcome: monitoring.AttemptRetryableError, HTTPStatus: 503, ErrorCode: "webhook_http_status", ErrorMessage: "receiver returned HTTP 503"}

// TestDeliveryRetryWindowAndDisableAwareAdmission proves retry scheduling on
// the database clock: every wait is capped at the window end, a failure after
// the window exhausts, an admitted attempt finishes after disable while its
// retry is refused, disable/re-enable never extends the window, and the
// backlog gauges count only admissible scheduled work.
func TestDeliveryRetryWindowAndDisableAwareAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := newDeliveryFixture(t, ctx, adapterPool(t, ctx), "adapter-retry-", 4)
	window := time.Minute
	f.leaseAll()

	// A wait beyond the remaining window (here a two-hour Retry-After) is
	// capped at the window end: one final attempt at the edge.
	d0 := f.deliveries[0]
	a, refused := f.admit(d0, window, f.configured)
	if refused != "" {
		t.Fatal(refused)
	}
	var created time.Time
	if err := f.pool.QueryRow(ctx, `SELECT created_at FROM deliveries WHERE organization=$1 AND id=$2`, f.org, d0).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if err := f.ds.Record(ctx, a, fail503, monitoring.Retry{Delay: 2 * time.Hour, Window: window}); err != nil {
		t.Fatal(err)
	}
	if at, ok := f.outbox(d0); !ok || !at.Equal(created.Add(window)) || f.read(d0).State != "pending" {
		t.Fatalf("wait not capped at window end: at=%v created=%v", at, created)
	}
	// The edge attempt fails after the window: exhausted, work removed,
	// history kept, last error visible.
	f.rewind(d0, window)
	a, refused, err := f.ds.Admit(ctx, f.claimDue(d0), window, f.configured)
	if err != nil || refused != "" || a.Number != 2 {
		t.Fatalf("edge attempt %+v %q %v", a, refused, err)
	}
	if err := f.ds.Record(ctx, a, fail503, monitoring.Retry{Delay: time.Second, Window: window}); err != nil {
		t.Fatal(err)
	}
	d := f.read(d0)
	if _, queued := f.outbox(d0); queued || d.State != "exhausted" || d.AttemptCount != 2 || d.LastErrorCode != "webhook_http_status" || d.Admission.Reason != "terminal" || d.NextAttemptAt != nil ||
		f.exhaustedReason(d0) != "window_elapsed" || f.attempts(d0) != 2 {
		t.Fatalf("window exhaustion %+v", d)
	}

	// An already admitted attempt finishes after disable and records its
	// outcome; the retry it schedules is then refused without an attempt.
	d1 := f.deliveries[1]
	a, _ = f.admit(d1, window, f.configured)
	if _, err := f.service.DisableSubscription(ctx, f.scope, "disable-s1", f.subs[1].ID); err != nil {
		t.Fatal(err)
	}
	if err := f.ds.Record(ctx, a, fail503, monitoring.Retry{Delay: time.Second, Window: window}); err != nil {
		t.Fatal(err)
	}
	page, err := f.store.Attempts(ctx, f.org, d1, 0, 10)
	if err != nil || len(page) != 1 || page[0].Outcome != monitoring.AttemptRetryableError {
		t.Fatal("admitted attempt must finish", page, err)
	}
	if _, refused = f.admit(d1, window, f.configured); refused != "subscription_disabled" || f.attempts(d1) != 1 || !f.parked(d1) {
		t.Fatal("retry after disable", refused)
	}
	// Disabled work stays pending once its window passes (disabling does not
	// fabricate a transport outcome) and is excluded from the backlog.
	f.age(d1, window)
	if d = f.read(d1); d.State != "pending" || d.Admission.Reason != "subscription_disabled" || d.NextAttemptAt != nil {
		t.Fatalf("disabled past window %+v", d)
	}
	// Re-enabling after the window never extends it: the worker exhausts the
	// Delivery without a new attempt.
	if _, err = f.pool.Exec(ctx, `UPDATE subscriptions SET enabled=true WHERE organization=$1 AND id=$2`, f.org, f.subs[1].ID); err != nil {
		t.Fatal(err)
	}
	before := f.updates(d1)
	if _, refused = f.admit(d1, window, f.configured); refused != "window_elapsed" || f.attempts(d1) != 1 {
		t.Fatal("re-enabled after window", refused, f.attempts(d1))
	}
	if _, queued := f.outbox(d1); queued || f.read(d1).State != "exhausted" || f.exhaustedReason(d1) != "window_elapsed" || f.updates(d1) != before+1 {
		t.Fatalf("window_elapsed exhaustion %+v", f.read(d1))
	}

	// Backlog gauges count admissible scheduled work only: d2 is due, d3 is
	// parked by a disabled Subscription; d0/d1 are terminal.
	d2, d3 := f.deliveries[2], f.deliveries[3]
	if _, err = f.service.DisableSubscription(ctx, f.scope, "disable-s3", f.subs[3].ID); err != nil {
		t.Fatal(err)
	}
	if _, refused = f.admit(d3, window, f.configured); refused != "subscription_disabled" {
		t.Fatal(refused)
	}
	f.age(d3, 10*time.Hour)
	f.unlease(d2)
	f.age(d2, 30*time.Second)
	b, err := f.ds.DeliveryBacklog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Other Organizations may share the database: this fixture contributes
	// d2 (≥30 s old) and never d3's ten-hour-old parked work.
	var mine int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM delivery_outbox o JOIN deliveries d ON (d.organization,d.id)=(o.organization,o.delivery_id)
WHERE o.organization=$1 AND o.available_at<'infinity' AND d.state IN ('pending','delivering')`, f.org).Scan(&mine); err != nil {
		t.Fatal(err)
	}
	if mine != 1 || b.Pending < 1 || b.OldestAge < 30*time.Second {
		t.Fatalf("backlog %+v mine=%d", b, mine)
	}
	var parkedAge float64
	if err = f.pool.QueryRow(ctx, `SELECT extract(epoch FROM now()-created_at) FROM deliveries WHERE organization=$1 AND id=$2`, f.org, d3).Scan(&parkedAge); err != nil {
		t.Fatal(err)
	}
	if b.OldestAge.Seconds() >= parkedAge {
		t.Fatalf("parked work inflated the oldest pending age: %v >= %vs", b.OldestAge, parkedAge)
	}
}
