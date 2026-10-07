package postgres_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The storage owner observes commit order through the feed, using a database
// barrier rather than goroutine completion order or a timing threshold.
func TestConcurrentPublicationsKeepCommittedCursorWindows(t *testing.T) {
	for _, withdraw := range []bool{false, true} {
		t.Run(fmt.Sprintf("withdraw=%v", withdraw), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			pool := adapterPool(t, ctx)
			scope := corpus.Scope{Organization: fmt.Sprintf("adapter-journal-parallel-%d", time.Now().UnixNano())}
			c, _, err := (postgres.Store{Pool: pool}).Create(ctx, scope.Organization, corpus.CreateInput{Key: "parallel", Name: "Parallel", Resolved: corpus.Retrieval{}})
			if err != nil {
				t.Fatal(err)
			}
			store := contentStores(pool)
			const writers = 8
			works := make([]content.Work, writers)
			publications := make([]content.Publication, writers)
			for i := range writers {
				key := fmt.Sprintf("writer-%d", i)
				r, err := store.Accept(ctx, scope, content.Command{Key: key, Source: content.Source{CorpusID: c.ID, Namespace: "parallel", RecordKey: key}, Content: content.Text{Kind: "text", Text: key}})
				if err != nil {
					t.Fatal(err)
				}
				works[i], _, err = store.Work(ctx, scope.Organization, r.ID)
				if err != nil {
					t.Fatal(err)
				}
				publications[i] = publication(content.Blob{Key: key, SHA256: key, Size: 8}, content.Blob{Key: key + "-manifest", SHA256: key + "-manifest", Size: 2})
			}
			head, err := store.ReadChanges(ctx, scope.Organization, c.ID, 0, 100, 24*time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			barrier, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer barrier.Rollback(context.WithoutCancel(ctx))
			// An uncommitted hash insertion makes the first publication wait on the
			// real unique-index arbitration. Unrelated hashes can proceed concurrently.
			b := publications[0].Normalized
			if _, err = barrier.Exec(ctx, `INSERT INTO content_blobs VALUES($1,$2,$3,$4,$5)`, scope.Organization, content.StableID("blob", scope.Organization, b.SHA256), b.Key, b.SHA256, b.Size); err != nil {
				t.Fatal(err)
			}
			cfg := pool.Config()
			cfg.MaxConns = 1
			cfg.ConnConfig.RuntimeParams["application_name"] = scope.Organization
			blockedPool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer blockedPool.Close()
			done := make(chan error, 1)
			go func() {
				done <- (postgres.MaterializationStore{Pool: blockedPool}).Publish(ctx, works[0], publications[0])
			}()
			for {
				var blocked bool
				if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock')`, scope.Organization).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("publication escaped hash barrier: %v", err)
				default:
				}
			}
			results := make(chan error, writers-1)
			// The deadline bounds a condition: all independent publications must
			// commit while the hash barrier remains held. No wall-clock sleep is used.
			independent, stop := context.WithTimeout(ctx, 10*time.Second)
			for i := 1; i < writers; i++ {
				go func(i int) { results <- store.Publish(independent, works[i], publications[i]) }(i)
			}
			var siblingErr error
			for range writers - 1 {
				if err := <-results; err != nil {
					siblingErr = err
				}
			}
			stop()
			if siblingErr != nil {
				barrier.Rollback(ctx)
				<-done
				t.Fatalf("independent publications queued behind the blocked hash writer: %v", siblingErr)
			}
			window, err := store.ReadChanges(ctx, scope.Organization, c.ID, head.Head, 100, 24*time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if len(window.Events) != 2*(writers-1) {
				t.Fatalf("committed sibling events=%d, want %d", len(window.Events), 2*(writers-1))
			}
			for i, event := range window.Events {
				if event.Position != head.Head+int64(i)+1 {
					t.Fatalf("position %d=%d, want %d", i, event.Position, head.Head+int64(i)+1)
				}
				if event.ResourceID == works[0].RecordID || event.ResourceID == works[0].ReceiptID {
					t.Fatalf("uncommitted publication leaked into feed: %+v", event)
				}
			}
			if withdraw {
				if _, err = store.Withdraw(ctx, scope, content.Withdrawal{Key: "withdraw-blocked", Source: works[0].Command.Source}); err != nil {
					t.Fatal(err)
				}
			}
			if err = barrier.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-done; err != nil {
				t.Fatal(err)
			}
			final, err := store.ReadChanges(ctx, scope.Organization, c.ID, head.Head, 100, 24*time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if len(final.Events) != 2*writers || !reflect.DeepEqual(final.Events[:len(window.Events)], window.Events) {
				t.Fatalf("committed cursor window changed: before=%+v after=%+v", window, final)
			}
			for i, event := range final.Events {
				if event.Position != head.Head+int64(i)+1 {
					t.Fatalf("final position %d=%d", i, event.Position)
				}
			}
			if withdraw {
				var blobs, versions int
				if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM content_blobs WHERE organization=$1 AND sha256=$2),(SELECT count(*) FROM record_versions WHERE organization=$1 AND id=$3)`, scope.Organization, publications[0].Normalized.SHA256, works[0].VersionID).Scan(&blobs, &versions); err != nil || blobs != 0 || versions != 0 {
					t.Fatalf("withdrawn preparation escaped rollback: blobs=%d versions=%d err=%v", blobs, versions, err)
				}
				receipt, err := store.Receipt(ctx, scope.Organization, works[0].ReceiptID)
				if err != nil || receipt.Outcome != "conflict" {
					t.Fatalf("fresh withdrawal guard: %+v %v", receipt, err)
				}
			}
			last := final.Events[len(final.Events)-2:]
			if last[0].ResourceID != works[0].RecordID || last[1].ResourceID != works[0].ReceiptID {
				t.Fatalf("last publication did not follow committed siblings: %+v", last)
			}
		})
	}
}
