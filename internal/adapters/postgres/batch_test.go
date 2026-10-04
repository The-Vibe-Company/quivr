package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Each batch entry is its own acceptance transaction: an entry interrupted at
// its last write leaves no Receipt, outbox row, Record or journal event, while
// the peers around it stay committed, and replaying the batch converges.
func TestBatchEntryFailureLeavesNoTraceAndKeepsPeers(t *testing.T) {
	path := os.Getenv("QUIVR_ADAPTER_CONFIG")
	if path == "" {
		t.Skip("real PostgreSQL suite runs inside make verify")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		DatabaseURL string `json:"database_url"`
	}
	if err = json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	scope := corpus.Scope{Organization: "adapter-batch", Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "adapter-batch", Name: "Adapter batch"})
	if err != nil {
		t.Fatal(err)
	}
	store := contentStores(pool)
	const apiKey = "adapter-batch-key-0123456789abcdef0123"
	handler, err := httpapi.New(postgres.Store{Pool: pool}, content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store, BlobSource: store}, retrieval.Service{}, uploads.Service{}, map[string]corpus.Scope{apiKey: scope}, []byte("adapter-cursor-key-0123456789abcdef0123"))
	if err != nil {
		t.Fatal(err)
	}
	entry := func(key string) map[string]any {
		return map[string]any{"idempotency_key": key, "source": map[string]any{"corpus_id": c.ID, "namespace": "tests", "record_key": key}, "content": map[string]any{"kind": "text", "text": "Entrée " + key}}
	}
	items := []any{entry("batch-a"), entry("batch-b"), entry("batch-c")}
	submit := func() []map[string]any {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"items": items})
		req := httptest.NewRequest("POST", "/v0/records/batch", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		var body struct {
			Items []map[string]any `json:"items"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || len(body.Items) != len(items) {
			t.Fatalf("batch got %d: %s", rec.Code, rec.Body.String())
		}
		return body.Items
	}
	count := func(query string, args ...any) int64 {
		t.Helper()
		var n int64
		if err := pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	journal := func() int64 {
		return count("SELECT coalesce(max(last_sequence),0) FROM organization_journals WHERE organization=$1", scope.Organization)
	}
	receiptOf := func(item map[string]any) string {
		t.Helper()
		r, ok := item["receipt"].(map[string]any)
		if !ok {
			t.Fatalf("entry rejected: %v", item)
		}
		return r["receipt_id"].(string)
	}

	// Interrupt only the middle entry, at its final write inside the acceptance transaction.
	failing := content.StableID("receipt", scope.Organization, "ingestion", "batch-b")
	_, err = pool.Exec(ctx, `CREATE FUNCTION fail_batch_entry() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.organization='adapter-batch' AND NEW.receipt_id='`+failing+`' THEN RAISE EXCEPTION 'synthetic acceptance interruption'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_batch_entry BEFORE INSERT ON ingestion_outbox FOR EACH ROW EXECUTE FUNCTION fail_batch_entry()`)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), "DROP TRIGGER IF EXISTS fail_batch_entry ON ingestion_outbox; DROP FUNCTION IF EXISTS fail_batch_entry()")
	before := journal()
	first := submit()
	a, cReceipt := receiptOf(first[0]), receiptOf(first[2])
	if e, ok := first[1]["error"].(map[string]any); !ok || e["code"] != "content_unavailable" || e["retryable"] != true {
		t.Fatalf("interrupted entry: %v", first[1])
	}
	if n := count("SELECT count(*) FROM ingestion_receipts WHERE organization=$1", scope.Organization); n != 2 {
		t.Fatalf("%d Receipts committed, want the 2 peers", n)
	}
	if n := count("SELECT count(*) FROM ingestion_outbox WHERE organization=$1", scope.Organization); n != 2 {
		t.Fatalf("%d outbox rows committed, want the 2 peers", n)
	}
	if n := count("SELECT count(*) FROM records WHERE organization=$1 AND record_key='batch-b'", scope.Organization); n != 0 {
		t.Fatal("interrupted entry left its Record identity")
	}
	// Each accepted entry appends exactly receipt.pending and record.accepted.
	if delta := journal() - before; delta != 4 || count("SELECT count(*) FROM change_events WHERE organization=$1", scope.Organization) != journal() {
		t.Fatalf("journal advanced by %d, want 4 with no gap", delta)
	}

	if _, err = pool.Exec(ctx, "DROP TRIGGER fail_batch_entry ON ingestion_outbox; DROP FUNCTION fail_batch_entry()"); err != nil {
		t.Fatal(err)
	}
	before = journal()
	replayed := submit()
	if receiptOf(replayed[0]) != a || receiptOf(replayed[2]) != cReceipt || receiptOf(replayed[1]) != failing {
		t.Fatal("replay did not converge on one Receipt per key")
	}
	if n := count("SELECT count(*) FROM ingestion_receipts WHERE organization=$1", scope.Organization); n != 3 {
		t.Fatalf("%d Receipts after replay, want 3", n)
	}
	if delta := journal() - before; delta != 2 {
		t.Fatalf("replay appended %d events, want only the recovered entry's 2", delta)
	}
}
