package postgres_test

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPublicationRollbackAndCommitOrderedJournal(t *testing.T) {
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
	scope := corpus.Scope{Organization: "adapter-transactions", Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "adapter", Name: "Adapter"})
	if err != nil {
		t.Fatal(err)
	}
	repository := postgres.ContentStore{Pool: pool}
	service := content.Service{Repository: repository}
	cmd := content.Command{Key: "rollback", Source: content.Source{CorpusID: c.ID, Namespace: "tests", RecordKey: "rollback"}, Content: content.Text{Kind: "text", Text: "Test rollback"}}
	receipt, err := service.Accept(ctx, scope, cmd)
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := repository.Work(ctx, scope.Organization, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	// This adapter fixture forces failure after the Version row, during its Part publication.
	_, err = pool.Exec(ctx, `CREATE FUNCTION fail_fixture_part() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.organization='adapter-transactions' THEN RAISE EXCEPTION 'synthetic publication interruption'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_fixture_part BEFORE INSERT ON version_parts FOR EACH ROW EXECUTE FUNCTION fail_fixture_part()`)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), "DROP TRIGGER IF EXISTS fail_fixture_part ON version_parts; DROP FUNCTION IF EXISTS fail_fixture_part()")
	text := content.Blob{Key: "fixture/text", SHA256: "fixture-text", Size: 13}
	manifest := content.Blob{Key: "fixture/manifest", SHA256: "fixture-manifest", Size: 2}
	if err = repository.Publish(ctx, work, text, manifest); err == nil {
		t.Fatal("publication failure was not injected")
	}
	still, err := repository.Receipt(ctx, scope.Organization, receipt.ID)
	if err != nil || still.State != "pending" {
		t.Fatal("Receipt resolved before atomic publication", err)
	}
	if _, err = repository.Version(ctx, scope.Organization, work.RecordID, work.VersionID); err == nil {
		t.Fatal("partial Version visible")
	}
	var blobs, parts int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM content_blobs WHERE organization=$1", scope.Organization).Scan(&blobs); err != nil {
		t.Fatal(err)
	}
	if blobs != 0 {
		t.Fatal("rolled-back publication left canonical Blob references")
	}
	_, err = pool.Exec(ctx, "DROP TRIGGER fail_fixture_part ON version_parts; DROP FUNCTION fail_fixture_part()")
	if err != nil {
		t.Fatal(err)
	}
	if err = repository.Publish(ctx, work, text, manifest); err != nil {
		t.Fatal(err)
	}
	if err = repository.Publish(ctx, work, text, manifest); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM version_parts WHERE organization=$1", scope.Organization).Scan(&parts); err != nil || parts != 1 {
		t.Fatal("retry duplicated or lost Part", err)
	}
	// Hold one Organization's journal transaction open. Its uncommitted sequence must not be skipped by another committing writer.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var before int64
	if err = tx.QueryRow(ctx, "SELECT last_sequence FROM organization_journals WHERE organization=$1 FOR UPDATE", scope.Organization).Scan(&before); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	var started sync.WaitGroup
	started.Add(1)
	go func() {
		next := cmd
		next.Key = "ordered"
		next.Source.RecordKey = "ordered"
		started.Done()
		_, err := service.Accept(ctx, scope, next)
		done <- err
	}()
	started.Wait()
	select {
	case err := <-done:
		t.Fatalf("writer escaped uncommitted journal boundary: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	var last, count int64
	if err = pool.QueryRow(ctx, "SELECT last_sequence FROM organization_journals WHERE organization=$1", scope.Organization).Scan(&last); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM change_events WHERE organization=$1", scope.Organization).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if last != count || last <= before {
		t.Fatal("journal allocation escaped committed facts", last, count, before)
	}
	// First-acceptance lineage is independent of activity completion and duplicate receipt order.
	firstCommand := content.Command{Key: "lineage-a", Source: content.Source{CorpusID: c.ID, Namespace: "tests", RecordKey: "lineage"}, Revision: "A", Content: content.Text{Kind: "text", Text: "A"}}
	firstReceipt, err := service.Accept(ctx, scope, firstCommand)
	if err != nil {
		t.Fatal(err)
	}
	secondCommand := firstCommand
	secondCommand.Key = "lineage-b"
	secondCommand.Revision = "B"
	secondCommand.Content.Text = "B"
	secondReceipt, err := service.Accept(ctx, scope, secondCommand)
	if err != nil {
		t.Fatal(err)
	}
	duplicateCommand := firstCommand
	duplicateCommand.Key = "lineage-a-repeat"
	duplicateReceipt, err := service.Accept(ctx, scope, duplicateCommand)
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := repository.Work(ctx, scope.Organization, firstReceipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := repository.Work(ctx, scope.Organization, secondReceipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	duplicateWork, _, err := repository.Work(ctx, scope.Organization, duplicateReceipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b.PredecessorID != a.VersionID || duplicateWork.Order != a.Order {
		t.Fatal("reservation lost original revision order/lineage")
	}
	var desired string
	if err = pool.QueryRow(ctx, "SELECT desired_version_id FROM records WHERE organization=$1 AND id=$2", scope.Organization, a.RecordID).Scan(&desired); err != nil {
		t.Fatal(err)
	}
	if desired != b.VersionID {
		t.Fatal("late duplicate moved desired revision backward")
	}
	// B finishes first; A's duplicate finishes next. B must still point to A, not to whichever worker won.
	if err = repository.Publish(ctx, b, text, manifest); err != nil {
		t.Fatal(err)
	}
	if err = repository.Publish(ctx, duplicateWork, text, manifest); err != nil {
		t.Fatal(err)
	}
	if err = repository.Publish(ctx, a, text, manifest); err != nil {
		t.Fatal(err)
	}
	var predecessor string
	var originalOrder int64
	if err = pool.QueryRow(ctx, "SELECT predecessor_id FROM record_versions WHERE organization=$1 AND id=$2", scope.Organization, b.VersionID).Scan(&predecessor); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "SELECT acceptance_order FROM record_versions WHERE organization=$1 AND id=$2", scope.Organization, a.VersionID).Scan(&originalOrder); err != nil {
		t.Fatal(err)
	}
	if predecessor != a.VersionID || originalOrder != a.Order {
		t.Fatal("completion order changed immutable history")
	}
	// Source Position takes precedence over the arrival of a new older revision.
	newer := firstCommand
	newer.Key = "position-new"
	newer.Source.RecordKey = "position"
	newer.Revision = "new"
	newer.Position = "20"
	newReceipt, err := service.Accept(ctx, scope, newer)
	if err != nil {
		t.Fatal(err)
	}
	newWork, _, err := repository.Work(ctx, scope.Organization, newReceipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	older := newer
	older.Key = "position-old"
	older.Revision = "old"
	older.Position = "10"
	if _, err = service.Accept(ctx, scope, older); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "SELECT desired_version_id FROM records WHERE organization=$1 AND id=$2", scope.Organization, newWork.RecordID).Scan(&desired); err != nil {
		t.Fatal(err)
	}
	if desired != newWork.VersionID {
		t.Fatal("late lower Source Position replaced desired Version")
	}

}
