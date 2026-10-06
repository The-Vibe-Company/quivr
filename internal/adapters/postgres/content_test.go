package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/jackc/pgx/v5/pgxpool"
)

// publication assembles a canonical single-body publication for adapter fixtures.
func publication(text, manifest content.Blob) content.Publication {
	return content.Publication{Normalized: text, Manifest: manifest, Parts: []content.PartBlob{{Key: "body", Role: "body", Blob: text}}}
}

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
	repository := contentStores(pool)
	service := content.Service{Submissions: repository, Receipts: repository, RecordStore: repository, Versions: repository, Materialization: repository}
	cmd := content.Command{Key: "rollback", Source: content.Source{CorpusID: c.ID, Namespace: "tests", RecordKey: "rollback"}, Content: content.Text{Kind: "text", Text: "Test rollback"}}
	receipt, err := service.Accept(ctx, scope, cmd)
	if err != nil {
		t.Fatal(err)
	}
	// Acceptance and its journal facts commit together, including a batch that
	// returns its row before the protocol's final transaction result is read.
	durable := func() [6]int64 {
		t.Helper()
		var counts [6]int64
		err := pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM records WHERE organization=$1),
 (SELECT count(*) FROM accepted_revisions WHERE organization=$1),
 (SELECT count(*) FROM ingestion_receipts WHERE organization=$1),
 (SELECT count(*) FROM ingestion_outbox WHERE organization=$1),
 (SELECT count(*) FROM change_events WHERE organization=$1),
 (SELECT last_sequence FROM organization_journals WHERE organization=$1)`, scope.Organization).
			Scan(&counts[0], &counts[1], &counts[2], &counts[3], &counts[4], &counts[5])
		if err != nil {
			t.Fatal(err)
		}
		return counts
	}
	beforeReplay := durable()
	replay, err := service.Accept(ctx, scope, cmd)
	if err != nil || replay.ID != receipt.ID || replay.NewRevision {
		t.Fatalf("first acceptance replay = %+v, err %v", replay, err)
	}
	changed := cmd
	changed.Source.RecordKey = "conflicting-record"
	if _, err = service.Accept(ctx, scope, changed); !errors.Is(err, content.ErrConflict) {
		t.Fatalf("changed request key should conflict without creating a Record: %v", err)
	}
	missing := cmd
	missing.Key = "missing-corpus"
	missing.Source.CorpusID = "absent-corpus"
	if _, err = service.Accept(ctx, scope, missing); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatalf("missing Corpus acceptance = %v", err)
	}
	if got := durable(); got != beforeReplay {
		t.Fatalf("replay/refusal changed durable facts: before %v, after %v", beforeReplay, got)
	}
	interrupted := cmd
	interrupted.Key = "event-rollback"
	interrupted.Source.RecordKey = "event-rollback"
	_, err = pool.Exec(ctx, `CREATE FUNCTION fail_fixture_acceptance_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.organization='adapter-transactions' AND NEW.event_type='record.accepted'
 THEN RAISE EXCEPTION 'synthetic second acceptance event interruption'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER fail_fixture_acceptance_event BEFORE INSERT ON change_events FOR EACH ROW EXECUTE FUNCTION fail_fixture_acceptance_event()`)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), "DROP TRIGGER IF EXISTS fail_fixture_acceptance_event ON change_events; DROP FUNCTION IF EXISTS fail_fixture_acceptance_event()")
	if _, err = service.Accept(ctx, scope, interrupted); err == nil {
		t.Fatal("second acceptance event failure was not injected")
	}
	if got := durable(); got != beforeReplay {
		t.Fatalf("failed acceptance leaked facts/journal head: before %v, after %v", beforeReplay, got)
	}
	if _, err = pool.Exec(ctx, "DROP TRIGGER fail_fixture_acceptance_event ON change_events; DROP FUNCTION fail_fixture_acceptance_event()"); err != nil {
		t.Fatal(err)
	}
	if retried, err := service.Accept(ctx, scope, interrupted); err != nil || !retried.NewRevision {
		t.Fatalf("rolled-back acceptance was not retryable: %+v, %v", retried, err)
	}
	// Simultaneous requests see the preceding journal holder's committed receipt.
	concurrent := cmd
	concurrent.Key = "concurrent-request"
	concurrent.Source.RecordKey = "concurrent-record"
	beforeConcurrent := durable()
	start := make(chan struct{})
	type acceptanceResult struct {
		receipt content.Receipt
		err     error
	}
	results := make(chan acceptanceResult, 2)
	for range 2 {
		go func() {
			<-start
			r, err := service.Accept(ctx, scope, concurrent)
			results <- acceptanceResult{r, err}
		}()
	}
	close(start)
	left, right := <-results, <-results
	if left.err != nil || right.err != nil || left.receipt.ID != right.receipt.ID || left.receipt.NewRevision == right.receipt.NewRevision {
		t.Fatalf("concurrent same-key acceptance did not converge: %+v / %+v", left, right)
	}
	wantConcurrent := beforeConcurrent
	for i, increment := range [6]int64{1, 1, 1, 1, 2, 2} {
		wantConcurrent[i] += increment
	}
	if got := durable(); got != wantConcurrent {
		t.Fatalf("concurrent acceptance duplicated/lost facts: got %v, want %v", got, wantConcurrent)
	}
	var routed bool
	if err = pool.QueryRow(ctx, `SELECT NOT legacy_workflow AND lease_until='infinity'::timestamptz
 FROM ingestion_outbox WHERE organization=$1 AND receipt_id=$2`, scope.Organization, left.receipt.ID).Scan(&routed); err != nil || !routed {
		t.Fatalf("new acceptance lost batch/API dispatch fence: %v, %v", routed, err)
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
	if err = repository.Publish(ctx, work, publication(text, manifest)); err == nil {
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
	if err = repository.Publish(ctx, work, publication(text, manifest)); err != nil {
		t.Fatal(err)
	}
	if err = repository.Publish(ctx, work, publication(text, manifest)); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM version_parts WHERE organization=$1", scope.Organization).Scan(&parts); err != nil || parts != 1 {
		t.Fatal("retry duplicated or lost Part", err)
	}
	// A Version read carries when its revision was accepted, not when it was published.
	accepted := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if _, err = pool.Exec(ctx, "UPDATE ingestion_receipts SET accepted_at=$3 WHERE organization=$1 AND id=$2", scope.Organization, receipt.ID, accepted); err != nil {
		t.Fatal(err)
	}
	published, err := repository.Version(ctx, scope.Organization, work.RecordID, work.VersionID)
	if err != nil || published.AcceptedAt == nil || !published.AcceptedAt.Equal(accepted) {
		t.Fatalf("Version %s accepted_at = %v, want %v (err %v)", work.VersionID, published.AcceptedAt, accepted, err)
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
	contenderConfig := pool.Config()
	contenderConfig.MaxConns = 1
	contenderConfig.ConnConfig.RuntimeParams["application_name"] = "adapter-journal-contender"
	contender, err := pgxpool.NewWithConfig(ctx, contenderConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Close()
	go func() {
		next := cmd
		next.Key = "ordered"
		next.Source.RecordKey = "ordered"
		_, err := (postgres.SubmissionStore{Pool: contender}).Accept(ctx, scope, next)
		done <- err
	}()
	for {
		select {
		case err := <-done:
			t.Fatalf("writer escaped uncommitted journal boundary: %v", err)
		default:
		}
		var blocked bool
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE application_name='adapter-journal-contender' AND wait_event_type='Lock')`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
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
	// Only an acceptance that reserves a revision reports a new Version; a
	// repeated revision or a replayed key does not.
	replayed, err := service.Accept(ctx, scope, firstCommand)
	if err != nil || replayed.ID != firstReceipt.ID {
		t.Fatal("replay", err)
	}
	if !firstReceipt.NewRevision || !secondReceipt.NewRevision || duplicateReceipt.NewRevision || replayed.NewRevision {
		t.Fatalf("new revision flags %v %v %v %v", firstReceipt.NewRevision, secondReceipt.NewRevision, duplicateReceipt.NewRevision, replayed.NewRevision)
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
	if err = repository.Publish(ctx, b, publication(text, manifest)); err != nil {
		t.Fatal(err)
	}
	if err = repository.Publish(ctx, duplicateWork, publication(text, manifest)); err != nil {
		t.Fatal(err)
	}
	if err = repository.Publish(ctx, a, publication(text, manifest)); err != nil {
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
