package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Withdrawal must commit its Tombstone, fence, Record event and Receipt as one
// atomic fact, even before the accepted Version is materialized.
func TestWithdrawalFenceAtomicityAndGuards(t *testing.T) {
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
	scope := corpus.Scope{Organization: "adapter-withdrawal", Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "withdrawal", Name: "Withdrawal"})
	if err != nil {
		t.Fatal(err)
	}
	store := contentStores(pool)
	service := content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store}
	source := content.Source{CorpusID: c.ID, Namespace: "adapter", RecordKey: "pending"}
	receipt, err := service.Accept(ctx, scope, content.Command{Key: "pending", Source: source, Content: content.Text{Kind: "text", Text: "Withdraw before materialization"}})
	if err != nil {
		t.Fatal(err)
	}
	withdrawal := content.Withdrawal{Key: "wd-1", Source: source}
	if _, err = pool.Exec(ctx, `CREATE FUNCTION fail_fixture_withdrawal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.organization='adapter-withdrawal' AND NEW.event_type='record.withdrawn' THEN RAISE EXCEPTION 'synthetic withdrawal interruption'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_fixture_withdrawal BEFORE INSERT ON change_events FOR EACH ROW EXECUTE FUNCTION fail_fixture_withdrawal()`); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Withdraw(ctx, scope, withdrawal); err == nil {
		t.Fatal("withdrawal event failure was not injected")
	}
	var tombstones, withdrawalReceipts int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM tombstones WHERE organization=$1", scope.Organization).Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM ingestion_receipts WHERE organization=$1 AND route_family='withdrawal'", scope.Organization).Scan(&withdrawalReceipts); err != nil {
		t.Fatal(err)
	}
	if tombstones != 0 || withdrawalReceipts != 0 {
		t.Fatalf("partial withdrawal committed: tombstones=%d receipts=%d", tombstones, withdrawalReceipts)
	}
	if _, err = pool.Exec(ctx, "DROP TRIGGER fail_fixture_withdrawal ON change_events; DROP FUNCTION fail_fixture_withdrawal()"); err != nil {
		t.Fatal(err)
	}
	resolved, err := service.Withdraw(ctx, scope, withdrawal)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.State != "resolved" || resolved.Outcome != "withdrawal_applied" || resolved.RecordID == "" {
		t.Fatalf("unexpected withdrawal Receipt: %+v", resolved)
	}
	replay, err := service.Withdraw(ctx, scope, withdrawal)
	if err != nil || replay.ID != resolved.ID {
		t.Fatalf("withdrawal replay diverged: %+v %v", replay, err)
	}
	changed := withdrawal
	changed.Reason = "different reason"
	if _, err = service.Withdraw(ctx, scope, changed); !errors.Is(err, content.ErrConflict) {
		t.Fatalf("changed withdrawal under the same key did not conflict: %v", err)
	}
	record, err := service.Record(ctx, scope, resolved.RecordID)
	if err != nil || !record.Withdrawn {
		t.Fatalf("Record not withdrawn: %+v %v", record, err)
	}
	second := withdrawal
	second.Key = "wd-2"
	again, err := service.Withdraw(ctx, scope, second)
	if err != nil || again.ID == resolved.ID {
		t.Fatalf("repeat withdrawal with a new key did not yield a new Receipt: %+v %v", again, err)
	}
	var events int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM change_events WHERE organization=$1 AND event_type='record.withdrawn'", scope.Organization).Scan(&events); err != nil || events != 1 {
		t.Fatalf("withdrawal event duplicated or absent: %d %v", events, err)
	}
	// A late worker cannot publish the accepted Version after the fence.
	work, _, err := store.Work(ctx, scope.Organization, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Publish(ctx, work, publication(content.Blob{Key: "fixtures/withdrawal/text", SHA256: "withdrawal-text", Size: 30}, content.Blob{Key: "fixtures/withdrawal/manifest", SHA256: "withdrawal-manifest", Size: 2})); err != nil {
		t.Fatal(err)
	}
	published, err := store.Receipt(ctx, scope.Organization, receipt.ID)
	if err != nil || published.Outcome != "conflict" {
		t.Fatalf("late publication did not resolve as conflict: %+v %v", published, err)
	}
	if _, err = store.Version(ctx, scope.Organization, work.RecordID, work.VersionID); err == nil {
		t.Fatal("withdrawn identity published a Version")
	}
	// A later ordinary ingestion of the same identity is a terminal conflict.
	if _, err = service.Accept(ctx, scope, content.Command{Key: "after-withdrawal", Source: source, Content: content.Text{Kind: "text", Text: "resurrect"}}); !errors.Is(err, content.ErrConflict) {
		t.Fatalf("withdrawn identity accepted a new submission: %v", err)
	}
	readOnly := scope
	readOnly.Actions = []string{"content:read"}
	if _, err = service.Withdraw(ctx, readOnly, withdrawal); !errors.Is(err, corpus.ErrForbidden) {
		t.Fatalf("read-only scope withdrew: %v", err)
	}
	other := scope
	other.Corpora = []string{"ungranted"}
	if _, err = service.Withdraw(ctx, other, withdrawal); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatalf("ungranted Corpus withdrew: %v", err)
	}
	if _, err = service.Withdraw(ctx, scope, content.Withdrawal{Key: "", Source: source}); !errors.Is(err, content.ErrInvalid) {
		t.Fatalf("invalid withdrawal accepted: %v", err)
	}
}
