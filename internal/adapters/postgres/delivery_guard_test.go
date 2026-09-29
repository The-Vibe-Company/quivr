package postgres_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
)

// TestDeliveryWorkerRefusesPrivateDestinations runs the real delivery worker
// loop and hardened HTTP client, without the private-address allowance,
// against the real store: a receiver on loopback, addressed by IP or by a
// name resolving to it, gets no request; the attempt is a permanent
// destination_address_refused; the Delivery ends exhausted; and no address
// appears in the public reads.
func TestDeliveryWorkerRefusesPrivateDestinations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := newDeliveryFixture(t, ctx, adapterPool(t, ctx), "adapter-delivery-guard-", 2)
	var hits atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(receiver.URL, "http://"))
	// Park every Delivery; each case releases exactly one.
	if _, err := f.pool.Exec(ctx, `UPDATE delivery_outbox SET available_at='infinity' WHERE organization=$1`, f.org); err != nil {
		t.Fatal(err)
	}
	for i, target := range []string{receiver.URL + "/hook", "http://localhost:" + port + "/hook"} {
		id := f.deliveries[i]
		f.unlease(id)
		run, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			monitoring.Deliverer{Store: f.ds, Destinations: map[string]monitoring.Destination{"dest": {Organization: f.org, URL: target, Secret: "whsec_AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="}},
				Workers: 1, Timeout: 2 * time.Second, Poll: 10 * time.Millisecond, MaxPoll: 50 * time.Millisecond}.Run(run)
		}()
		deadline := time.Now().Add(20 * time.Second)
		for f.read(id).State != "exhausted" && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		stop()
		<-done

		d, err := f.service.Delivery(ctx, f.scope, id)
		if err != nil || d.State != "exhausted" || d.AttemptCount != 1 || d.LastOutcome != monitoring.AttemptPermanentError || d.LastErrorCode != "destination_address_refused" || f.exhaustedReason(id) != "permanent_error" {
			t.Fatalf("%s: delivery %+v %v", target, d, err)
		}
		attempts, err := f.service.Attempts(ctx, f.scope, id, 0, 10)
		if err != nil || len(attempts) != 1 || attempts[0].Outcome != monitoring.AttemptPermanentError || attempts[0].ErrorCode != "destination_address_refused" || attempts[0].HTTPStatus != 0 {
			t.Fatalf("%s: attempts %+v %v", target, attempts, err)
		}
		public, _ := json.Marshal([]any{d, attempts})
		for _, leak := range []string{"127.0.0.1", "localhost", "[::1]", ":" + port} {
			if strings.Contains(string(public), leak) {
				t.Fatalf("public read leaks the destination address %q: %s", leak, public)
			}
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("%d requests reached a private receiver", n)
	}
}
