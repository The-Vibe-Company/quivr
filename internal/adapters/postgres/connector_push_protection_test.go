package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/observability"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This adapter journey owns push protection: real durable storage, HTTP
// dispatch, ingestion and audit/rollup reads; only the remote plugin is replaced.
// Time advances by aging persisted timestamps, never by sleeping. Losing the
// shared bucket or key lock invokes the plugin twice or admits an extra push.
func TestConnectorPushProtectionAcrossReplicas(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	basePool := adapterPool(t, ctx)
	poolConfig := basePool.Config().Copy()
	poolConfig.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	scope := corpus.Scope{Organization: fmt.Sprintf("adapter-push-%d", time.Now().UnixNano()), Actions: []string{"corpora:write", "connectors:write", "connectors:admin", "connector:push", "observability:read"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "c", Name: "Push protection"})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	var refuse atomic.Bool
	var pin *plugins.Pin
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/discovery" {
			_ = json.NewEncoder(w).Encode(map[string]any{"plugin_api": pin.PluginAPI(), "plugin": map[string]string{"id": pin.Manifest.ID, "version": pin.Manifest.Version}, "manifest_digest": pin.ManifestDigest, "contributions": pin.Manifest.Contributions.Names()})
			return
		}
		n := calls.Add(1)
		if refuse.Load() {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(plugins.ConnectorDelivery{Verdict: "refused", Response: plugins.ReceiveAnswer{Status: 422, ContentType: "application/json", Body: `{"reason":"unrecognized event"}`}})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(plugins.ConnectorDelivery{Verdict: "accepted", Response: plugins.ReceiveAnswer{Status: 204}, Items: []plugins.ConnectorItem{{RecordKey: fmt.Sprint(n), Revision: "1", Content: json.RawMessage(`{"kind":"text","text":"An incoming event"}`)}}})
	}))
	defer remote.Close()
	pin, err = plugins.LoadPinManifest([]byte(`id: example-push
version: 1.0.0
compatibility: {engine: ">=0.1.0 <0.3.0", plugin_api: ">=0.12.0 <0.13.0"}
contributions:
  connector:
    kinds:
      echo:
        config_schema: {type: object, additionalProperties: false}
        default_interval_seconds: 300
        modes: [pull, push]
        api:
          routes:
            - {name: push, method: POST, path: events, auth: quivr_key}
            - {name: token, method: POST, path: token-events, auth: instance_token}
            - {name: signed, method: POST, path: receive, auth: signature, signature: {header: X-Signature, timestamp_header: X-Sent-At, window_seconds: 300}}
`), "push test", plugins.PinConfig{Endpoint: remote.URL})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := connectors.NewRegistry(pluginhttp.Connector{Pin: pin, Name: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.ConnectorStore{Pool: pool}
	sealer, _ := connectors.NewSealer("adapter-push-test-key-0123456789abcdef")
	service := connectors.Service{Store: store, Tokens: store, Registry: registry, Sealer: sealer}
	instance, err := service.Create(ctx, scope, connectors.CreateInput{Key: "one", CorpusID: c.ID, Namespace: "events", Kind: "echo", Config: json.RawMessage(`{}`), PushPolicy: &connectors.PushPolicy{RatePerSecond: 1, Burst: 1, AllowedCIDRs: []string{"192.0.2.0/24"}}})
	if err != nil {
		t.Fatal(err)
	}
	if instance.PushPolicy == nil || instance.PushPolicy.Burst != 1 {
		t.Fatalf("policy not persisted: %+v", instance.PushPolicy)
	}
	newHandler := func() http.Handler {
		h, err := httpapi.New(nil, content.Service{Submissions: postgres.SubmissionStore{Pool: store.Pool}, Receipts: postgres.ReceiptStore{Pool: store.Pool}, RecordStore: postgres.RecordStore{Pool: store.Pool}, Versions: postgres.VersionStore{Pool: store.Pool}, Materialization: postgres.MaterializationStore{Pool: store.Pool}}, retrieval.Service{}, uploads.Service{}, map[string]corpus.Scope{"push": scope}, []byte("push-cursor-key-0123456789abcdef"),
			httpapi.WithRelay(connectors.Relay{Store: store, Tokens: service, Registry: registry, Ingest: content.Service{Submissions: postgres.SubmissionStore{Pool: store.Pool}, Receipts: postgres.ReceiptStore{Pool: store.Pool}, RecordStore: postgres.RecordStore{Pool: store.Pool}, Versions: postgres.VersionStore{Pool: store.Pool}, Materialization: postgres.MaterializationStore{Pool: store.Pool}}, Protection: store, Replays: store}),
			httpapi.WithTrustedPushProxies([]string{"10.0.0.0/8"}),
			httpapi.WithObservability(nil, observability.Reader{Store: postgres.ObservabilityStore{Pool: pool}}))
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	first, second := newHandler(), newHandler()
	path := "/v0/connectors/" + instance.ID + "/api/events"
	send := func(h http.Handler, key, peer, forwarded, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(`{}`)).WithContext(ctx)
		req.RemoteAddr = peer
		req.Header.Set("X-Forwarded-For", forwarded)
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		return out
	}
	for _, tc := range []struct {
		peer, forwarded, token string
		status                 int
	}{
		{"192.0.2.7:1234", "", "", 401},
		{"198.51.100.7:1234", "192.0.2.7", "push", 403},
		{"10.0.0.1:1234", "192.0.2.7, 198.51.100.7", "push", 403},
		{"10.0.0.1:1234", "invalid", "push", 403},
	} {
		out := send(first, "", tc.peer, tc.forwarded, tc.token)
		if out.Code != tc.status || calls.Load() != 0 || out.Header().Get("Quivr-Response-Origin") != "" || out.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("refusal: status=%d body=%s calls=%d", out.Code, out.Body.String(), calls.Load())
		}
	}
	out := send(first, "delivery-1", "10.0.0.1:1234", "192.0.2.7, 10.0.0.2", "push")
	if out.Code != 202 || !strings.Contains(out.Body.String(), "receipt_id") || calls.Load() != 1 {
		t.Fatalf("accepted: %d %s calls=%d", out.Code, out.Body.String(), calls.Load())
	}
	replay := send(second, "delivery-1", "192.0.2.7:1234", "", "push")
	if replay.Code != 202 || !bytes.Equal(out.Body.Bytes(), replay.Body.Bytes()) || calls.Load() != 1 {
		t.Fatalf("replay: %d %s calls=%d", replay.Code, replay.Body.String(), calls.Load())
	}
	limited := send(second, "delivery-2", "192.0.2.7:1234", "", "push")
	if limited.Code != 429 || limited.Header().Get("Retry-After") != "1" || calls.Load() != 1 || limited.Header().Get("Quivr-Response-Origin") != "" || !strings.Contains(limited.Body.String(), `"retryable":true`) {
		t.Fatalf("rate: %d %s retry=%s calls=%d", limited.Code, limited.Body.String(), limited.Header().Get("Retry-After"), calls.Load())
	}
	// Refill and expire the completed key without a clock wait.
	if _, err = pool.Exec(ctx, `UPDATE connector_push_buckets SET updated_at=updated_at-interval '2 seconds' WHERE connector_id=$1`, instance.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE connector_push_answers SET expires_at=now()-interval '1 second' WHERE connector_id=$1`, instance.ID); err != nil {
		t.Fatal(err)
	}
	// Two simultaneous replicas with the same key must return the same new receipt.
	var wg sync.WaitGroup
	answers := make([]*httptest.ResponseRecorder, 2)
	start := make(chan struct{})
	for i, h := range []http.Handler{first, second} {
		wg.Add(1)
		go func(i int, h http.Handler) {
			defer wg.Done()
			<-start
			answers[i] = send(h, "delivery-1", "192.0.2.7:1234", "", "push")
		}(i, h)
	}
	close(start)
	wg.Wait()
	if calls.Load() != 2 || answers[0].Code != 202 || answers[1].Code != 202 || !bytes.Equal(answers[0].Body.Bytes(), answers[1].Body.Bytes()) {
		t.Fatalf("concurrent replay: calls=%d responses=%d %s / %d %s", calls.Load(), answers[0].Code, answers[0].Body.String(), answers[1].Code, answers[1].Body.String())
	}
	var received, refused int
	if err = pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE event_type='connector.push.received'),count(*) FILTER (WHERE event_type='connector.push.refused') FROM change_events WHERE organization=$1`, scope.Organization).Scan(&received, &refused); err != nil {
		t.Fatal(err)
	}
	if received != 4 || refused != 5 {
		t.Fatalf("audit events received=%d refused=%d; want 4 and 5", received, refused)
	}
	req := httptest.NewRequest("GET", "/v0/admin/stats/connector-pushes?connector_id="+instance.ID, nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer push")
	stats := httptest.NewRecorder()
	second.ServeHTTP(stats, req)
	var report struct {
		Total int `json:"total"`
		Items []struct {
			ConnectorID string `json:"connector_id"`
			Outcome     string `json:"outcome"`
			Count       int    `json:"count"`
		} `json:"items"`
	}
	if err = json.Unmarshal(stats.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, item := range report.Items {
		if item.ConnectorID != instance.ID {
			t.Fatalf("foreign stats: %+v", item)
		}
		counts[item.Outcome] = item.Count
	}
	if stats.Code != 200 || report.Total != 9 || counts["received"] != 4 || counts["refused"] != 5 {
		t.Fatalf("stats: %d %s", stats.Code, stats.Body.String())
	}
	req = httptest.NewRequest("GET", "/v0/admin/stats/connector-pushes?limit=1", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer push")
	stats = httptest.NewRecorder()
	second.ServeHTTP(stats, req)
	if err = json.Unmarshal(stats.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if stats.Code != 200 || report.Total != 9 || len(report.Items) != 1 || report.Items[0].Count != 5 {
		t.Fatalf("bounded stats lost total: %d %s", stats.Code, stats.Body.String())
	}
	// A database constraint scoped to this test Organization rejects every
	// counter write independent of clock buckets. Event and response must both
	// reflect the failed transaction, with no production testing hook.
	constraint := "push_audit_" + fmt.Sprint(time.Now().UnixNano())
	dropConstraint := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanup, "ALTER TABLE observability_rollups DROP CONSTRAINT IF EXISTS "+constraint)
	}
	if _, err = pool.Exec(ctx, "ALTER TABLE observability_rollups ADD CONSTRAINT "+constraint+" CHECK (organization <> '"+scope.Organization+"') NOT VALID"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dropConstraint)
	failed := send(first, "", "192.0.2.7:1234", "", "")
	var failure map[string]any
	_ = json.Unmarshal(failed.Body.Bytes(), &failure)
	if failed.Code != 503 || failed.Header().Get("Content-Type") != "application/json" || failure["code"] != "connectors_unavailable" || failed.Header().Get("X-Request-ID") == "" {
		t.Fatalf("audit unavailable: %d %s", failed.Code, failed.Body.String())
	}
	var journalRefused int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM change_events WHERE organization=$1 AND event_type='connector.push.refused'`, scope.Organization).Scan(&journalRefused); err != nil {
		t.Fatal(err)
	}
	if journalRefused != 5 {
		t.Fatalf("failed audit committed event: refused=%d", journalRefused)
	}
	dropConstraint()
	// The same key on another instance has its own bucket and answer.
	other, err := service.Create(ctx, scope, connectors.CreateInput{Key: "two", CorpusID: c.ID, Namespace: "other-events", Kind: "echo", Config: json.RawMessage(`{}`), PushPolicy: &connectors.PushPolicy{RatePerSecond: 1, Burst: 1}})
	if err != nil {
		t.Fatal(err)
	}
	path = "/v0/connectors/" + other.ID + "/api/events"
	independent := send(second, "delivery-1", "198.51.100.1:1234", "", "push")
	if independent.Code != 202 || calls.Load() != 3 || bytes.Equal(independent.Body.Bytes(), answers[0].Body.Bytes()) {
		t.Fatalf("instance isolation: %d %s calls=%d", independent.Code, independent.Body.String(), calls.Load())
	}
	if _, err = pool.Exec(ctx, `UPDATE connector_push_buckets SET updated_at=updated_at-interval '2 seconds' WHERE connector_id=$1`, other.ID); err != nil {
		t.Fatal(err)
	}
	refuse.Store(true)
	refusedOnce := send(first, "refusal-1", "198.51.100.1:1234", "", "push")
	refusedTwice := send(second, "refusal-1", "198.51.100.1:1234", "", "push")
	if refusedOnce.Code != 422 || refusedTwice.Code != 422 || refusedOnce.Header().Get("Quivr-Response-Origin") != "plugin" || refusedTwice.Header().Get("Quivr-Response-Origin") != "plugin" || !bytes.Equal(refusedOnce.Body.Bytes(), refusedTwice.Body.Bytes()) || calls.Load() != 4 {
		t.Fatalf("refusal replay: %d %s / %d %s calls=%d", refusedOnce.Code, refusedOnce.Body.String(), refusedTwice.Code, refusedTwice.Body.String(), calls.Load())
	}
	// Token authentication must precede protection and replay on both handlers:
	// invalid credentials cannot consume admission, and revocation cannot be
	// bypassed by a completed idempotency answer.
	protected, err := service.Create(ctx, scope, connectors.CreateInput{Key: "token", CorpusID: c.ID, Namespace: "token-events", Kind: "echo", Config: json.RawMessage(`{}`), PushPolicy: &connectors.PushPolicy{RatePerSecond: 1, Burst: 1, AllowedCIDRs: []string{"192.0.2.0/24"}}})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.CreateToken(ctx, scope, protected.ID)
	if err != nil {
		t.Fatal(err)
	}
	path = "/v0/connectors/" + protected.ID + "/api/token-events"
	wrongByte := byte('A')
	if issued.Secret[37] == wrongByte {
		wrongByte = 'B'
	}
	wrong := issued.Secret[:37] + string(wrongByte) + issued.Secret[38:]
	for _, tc := range []struct {
		token, peer, code string
		status            int
	}{
		{wrong, "198.51.100.1:1234", "invalid_instance_token", 401},
		{"push", "198.51.100.1:1234", "invalid_instance_token", 401},
		{issued.Secret, "198.51.100.1:1234", "ip_not_allowed", 403},
	} {
		refused := send(first, "token-delivery", tc.peer, "", tc.token)
		var envelope struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(refused.Body.Bytes(), &envelope)
		if refused.Code != tc.status || envelope.Code != tc.code || refused.Header().Get("Content-Type") != "application/json" || refused.Header().Get("Quivr-Response-Origin") != "" || calls.Load() != 4 {
			t.Fatalf("token auth/protection refusal: status=%d code=%s calls=%d", refused.Code, envelope.Code, calls.Load())
		}
	}
	// A malformed replay key still cannot precede durable token auth.
	for _, tc := range []struct {
		token, code string
		status      int
	}{
		{wrong, "invalid_instance_token", 401},
		{issued.Secret, "invalid_idempotency_key", 400},
	} {
		invalidKey := send(first, strings.Repeat("k", 257), "192.0.2.7:1234", "", tc.token)
		var envelope struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(invalidKey.Body.Bytes(), &envelope)
		if invalidKey.Code != tc.status || envelope.Code != tc.code || invalidKey.Header().Get("Content-Type") != "application/json" || invalidKey.Header().Get("Quivr-Response-Origin") != "" || calls.Load() != 4 {
			t.Fatalf("idempotency validation preceded token auth: status=%d code=%s calls=%d", invalidKey.Code, envelope.Code, calls.Load())
		}
	}
	refuse.Store(false)
	acceptedToken := send(first, "token-delivery", "192.0.2.7:1234", "", issued.Secret)
	replayedToken := send(second, "token-delivery", "192.0.2.7:1234", "", issued.Secret)
	if acceptedToken.Code != 202 || replayedToken.Code != 202 || !bytes.Equal(acceptedToken.Body.Bytes(), replayedToken.Body.Bytes()) || calls.Load() != 5 {
		t.Fatalf("token replay: statuses=%d/%d calls=%d", acceptedToken.Code, replayedToken.Code, calls.Load())
	}
	limitedToken := send(second, "next-token-delivery", "192.0.2.7:1234", "", issued.Secret)
	if limitedToken.Code != 429 || limitedToken.Header().Get("Retry-After") != "1" || limitedToken.Header().Get("Content-Type") != "application/json" || limitedToken.Header().Get("Quivr-Response-Origin") != "" || !strings.Contains(limitedToken.Body.String(), `"retryable":true`) || calls.Load() != 5 {
		t.Fatalf("token rate: status=%d calls=%d", limitedToken.Code, calls.Load())
	}
	if _, err = service.RevokeToken(ctx, scope, protected.ID, issued.Token.ID); err != nil {
		t.Fatal(err)
	}
	revoked := send(second, "token-delivery", "192.0.2.7:1234", "", issued.Secret)
	if revoked.Code != 401 || !strings.Contains(revoked.Body.String(), `"code":"invalid_instance_token"`) || revoked.Header().Get("Content-Type") != "application/json" || revoked.Header().Get("Quivr-Response-Origin") != "" || calls.Load() != 5 {
		t.Fatalf("revoked token replay admitted: status=%d calls=%d", revoked.Code, calls.Load())
	}
	req = httptest.NewRequest("GET", "/v0/admin/stats/connector-pushes?connector_id="+protected.ID, nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer push")
	stats = httptest.NewRecorder()
	second.ServeHTTP(stats, req)
	if err = json.Unmarshal(stats.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	counts = map[string]int{}
	for _, item := range report.Items {
		counts[item.Outcome] = item.Count
	}
	if stats.Code != 200 || report.Total != 9 || counts["received"] != 2 || counts["refused"] != 7 {
		t.Fatalf("token audit counts: %d %s", stats.Code, stats.Body.String())
	}
	// Signature freshness/replay owns authentication before push admission;
	// the legacy alias must share the bucket, IP policy and audit counters.
	signed, err := service.Create(ctx, scope, connectors.CreateInput{Key: "signed", CorpusID: c.ID, Namespace: "signed-events", Kind: "echo", Config: json.RawMessage(`{}`), PushPolicy: &connectors.PushPolicy{RatePerSecond: 1, Burst: 1, AllowedCIDRs: []string{"192.0.2.0/24"}}})
	if err != nil {
		t.Fatal(err)
	}
	signedPath := "/v0/connectors/" + signed.ID + "/api/receive"
	legacyPath := "/v0/connector-webhooks/" + signed.ID
	sendSigned := func(h http.Handler, address, signature, key, peer, stamp string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", address, strings.NewReader(`{}`)).WithContext(ctx)
		r.RemoteAddr = peer
		r.Header.Set("X-Signature", signature)
		r.Header.Set("X-Sent-At", stamp)
		r.Header.Set("Idempotency-Key", key)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, r)
		return out
	}
	stamp := fmt.Sprint(time.Now().Unix())
	staleStamp := fmt.Sprint(time.Now().Add(-time.Hour).Unix())
	assertSigned := func(out *httptest.ResponseRecorder, status int, code string, expectedCalls int64) {
		t.Helper()
		if out.Code != status || calls.Load() != expectedCalls || (code != "" && (!strings.Contains(out.Body.String(), `"code":"`+code+`"`) || out.Header().Get("Content-Type") != "application/json" || out.Header().Get("Quivr-Response-Origin") != "")) {
			t.Fatalf("signed pipeline: want status=%d code=%s calls=%d; got status=%d body=%s calls=%d", status, code, expectedCalls, out.Code, out.Body.String(), calls.Load())
		}
	}
	assertSigned(sendSigned(first, signedPath, "sig-1", "signed-1", "198.51.100.1:1234", staleStamp), 401, "invalid_signature", 5)
	assertSigned(sendSigned(first, signedPath, "sig-1", "signed-1", "198.51.100.1:1234", stamp), 403, "ip_not_allowed", 5)
	assertSigned(sendSigned(first, signedPath, "sig-1", "signed-1", "192.0.2.7:1234", stamp), 202, "", 6)
	assertSigned(sendSigned(second, legacyPath, "sig-1", "signed-1", "192.0.2.7:1234", stamp), 409, "push_replayed", 6)
	limitedSigned := sendSigned(second, legacyPath, "sig-2", "signed-2", "192.0.2.7:1234", stamp)
	assertSigned(limitedSigned, 429, "rate_limited", 6)
	if limitedSigned.Header().Get("Retry-After") != "1" || !strings.Contains(limitedSigned.Body.String(), `"retryable":true`) {
		t.Fatalf("signed rate response: %s %s", limitedSigned.Header().Get("Retry-After"), limitedSigned.Body.String())
	}
	assertSigned(sendSigned(first, signedPath, "sig-3", strings.Repeat("k", 257), "192.0.2.7:1234", staleStamp), 401, "invalid_signature", 6)
	assertSigned(sendSigned(first, signedPath, "sig-3", strings.Repeat("k", 257), "192.0.2.7:1234", stamp), 400, "invalid_idempotency_key", 6)
	if _, err = pool.Exec(ctx, `UPDATE connector_push_buckets SET updated_at=updated_at-interval '2 seconds' WHERE connector_id=$1`, signed.ID); err != nil {
		t.Fatal(err)
	}
	assertSigned(sendSigned(second, legacyPath, "sig-2", "signed-2", "192.0.2.7:1234", stamp), 202, "", 7)
	assertSigned(sendSigned(first, signedPath, "sig-4", "signed-2", "192.0.2.7:1234", stamp), 409, "push_replayed", 7)
	assertSigned(sendSigned(second, legacyPath, "sig-5", "signed-5", "198.51.100.1:1234", stamp), 403, "ip_not_allowed", 7)
	oversizedAlias := httptest.NewRequest("POST", legacyPath, strings.NewReader(strings.Repeat("x", (1<<20)+1))).WithContext(ctx)
	oversizedAlias.RemoteAddr = "192.0.2.7:1234"
	aliasRefusal := httptest.NewRecorder()
	second.ServeHTTP(aliasRefusal, oversizedAlias)
	assertSigned(aliasRefusal, 413, "request_too_large", 7)
	// A response cache must not outlive signature authentication. Once the
	// guard window expires, a new request must reach provider verification
	// even if it names an earlier key and that provider now refuses it.
	if _, err = pool.Exec(ctx, `UPDATE connector_signature_replays SET expires_at=now()-interval '1 second' WHERE connector_id=$1`, signed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE connector_push_buckets SET updated_at=updated_at-interval '2 seconds' WHERE connector_id=$1`, signed.ID); err != nil {
		t.Fatal(err)
	}
	refuse.Store(true)
	providerRefused := sendSigned(second, signedPath, "new-unverified-signature", "signed-1", "192.0.2.7:1234", stamp)
	if providerRefused.Code != 422 || providerRefused.Header().Get("Quivr-Response-Origin") != "plugin" || calls.Load() != 8 {
		t.Fatalf("response cache bypassed provider verification: status=%d calls=%d", providerRefused.Code, calls.Load())
	}
	refuse.Store(false)
	req = httptest.NewRequest("GET", "/v0/admin/stats/connector-pushes?connector_id="+signed.ID, nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer push")
	stats = httptest.NewRecorder()
	second.ServeHTTP(stats, req)
	if err = json.Unmarshal(stats.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	counts = map[string]int{}
	for _, item := range report.Items {
		counts[item.Outcome] = item.Count
	}
	if stats.Code != 200 || report.Total != 12 || counts["received"] != 2 || counts["refused"] != 10 {
		t.Fatalf("signed/alias audit counts: %d %s", stats.Code, stats.Body.String())
	}
	// A cleanup cycle must drain more than a single batch, rather than imposing
	// a fixed 1000-keys/minute ceiling on a configured source rate.
	if _, err = pool.Exec(ctx, `INSERT INTO connector_push_answers(organization,connector_id,key_hash,answer,expires_at) SELECT $1,$2,'expired-'||n,'{}'::jsonb,now()-interval '1 second' FROM generate_series(1,1001) n`, scope.Organization, other.ID); err != nil {
		t.Fatal(err)
	}
	// Cleanup retires expired completed answers; a new key may later be admitted.
	if _, err = pool.Exec(ctx, `UPDATE connector_push_answers SET expires_at=now()-interval '1 second' WHERE organization=$1`, scope.Organization); err != nil {
		t.Fatal(err)
	}
	if err = store.PrunePushAnswers(ctx); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM connector_push_answers WHERE organization=$1`, scope.Organization).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("expired keys retained: %d", remaining)
	}

}
