package postgres_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	blobs "github.com/The-Vibe-Company/quivr/internal/adapters/s3"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type acquisitionWork struct{ Statements, Exchanges, Begins, Commits, Rollbacks int }

type acquisitionTrace struct {
	mu   sync.Mutex
	work acquisitionWork
}

func (p *acquisitionTrace) record(sqls []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.work.Exchanges++
	p.work.Statements += len(sqls)
	for _, sql := range sqls {
		switch strings.ToLower(strings.TrimSpace(sql)) {
		case "begin":
			p.work.Begins++
		case "commit":
			p.work.Commits++
		case "rollback":
			p.work.Rollbacks++
		}
	}
}
func (p *acquisitionTrace) reset() { p.mu.Lock(); defer p.mu.Unlock(); p.work = acquisitionWork{} }
func (p *acquisitionTrace) snapshot() acquisitionWork {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.work
}
func (p *acquisitionTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	p.record([]string{d.SQL})
	return ctx
}
func (*acquisitionTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func (p *acquisitionTrace) TraceBatchStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceBatchStartData) context.Context {
	sqls := make([]string, 0, d.Batch.Len())
	for _, q := range d.Batch.QueuedQueries {
		sqls = append(sqls, q.SQL)
	}
	p.record(sqls)
	return ctx
}
func (*acquisitionTrace) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {}
func (*acquisitionTrace) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData)     {}

type archiveBlobRoutes struct{}

func (archiveBlobRoutes) Routed(context.Context, string) bool { return true }

// Owns deterministic acquisition work budgets at the real upload/accept stores.
// The only fake is the remote object store: actual S3 verification hashes the
// uploaded bytes. SQL executes on PostgreSQL, with no time threshold or delay.
// A regression to per-write database exchanges or repeated acceptance
// transactions fails this test even on a fast machine.
func TestArchiveAcquisitionWorkBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	org := fmt.Sprintf("adapter-archive-acquisition-%d", time.Now().UnixNano())
	c, _, err := (postgres.Store{Pool: pool}).Create(ctx, org, corpus.CreateInput{Key: "archive", Name: "Archive acquisition", Resolved: corpus.Retrieval{}})
	if err != nil {
		t.Fatal(err)
	}
	probe := &acquisitionTrace{}
	cfg := pool.Config()
	cfg.ConnConfig.Tracer = probe
	// Count application Query/SendBatch exchanges, including BEGIN/COMMIT.
	// Connection setup and first-use prepared-statement descriptions are outside
	// this budget; no wall-clock measurement is asserted.
	traced, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()
	if err := traced.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	objects := map[string][]byte{}
	gets, heads, puts := 0, 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case "PUT":
			puts++
			data, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(500)
				return
			}
			objects[r.URL.Path] = data
		case "GET", "HEAD":
			if r.Method == "GET" {
				gets++
			} else {
				heads++
			}
			data, ok := objects[r.URL.Path]
			if !ok {
				w.WriteHeader(404)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			if r.Method == "GET" {
				_, _ = w.Write(data)
			}
		default:
			w.WriteHeader(405)
		}
	}))
	defer srv.Close()
	storage := blobs.New(blobs.Config{Endpoint: srv.URL, Bucket: "archive-acquisition", AccessKey: "fixture-access", SecretKey: "fixture-secret"})
	uploadStore := postgres.UploadStore{Pool: traced}
	uploader := uploads.Service{Store: uploadStore, Transfer: storage}
	ingest := content.Service{Submissions: postgres.SubmissionStore{Pool: traced}, BlobSource: uploadStore, Routes: archiveBlobRoutes{}}
	receipts := postgres.ReceiptStore{Pool: traced}
	ctx = workqueue.WithClass(ctx, workqueue.Bulk)
	const pageItems = 31
	// Numeric source position, rather than archive arrival order, owns currentness.
	// Document totals add six ordered statements to the existing observation
	// batch; they add no exchange or acceptance transaction. Keep those budgets
	// literal so a new per-write round trip still fails this owner.
	for _, page := range []struct {
		name, position string
		accept         acquisitionWork
	}{
		{"new-records", "12", acquisitionWork{21, 5, 1, 1, 0}},
		{"corrections", "13", acquisitionWork{28, 7, 1, 1, 0}},
		{"older-position", "3", acquisitionWork{27, 7, 1, 1, 0}},
	} {
		t.Run(page.name, func(t *testing.T) {
			totals := map[string]acquisitionWork{}
			for i := 0; i < pageItems; i++ {
				data := []byte(fmt.Sprintf("Archive member %d position %s. ", i, page.position) + strings.Repeat("Harbour report. ", 2048))
				key := fmt.Sprintf("member-%d-position-%s", i, page.position)
				measure := func(stage string, want acquisitionWork, action func() error) {
					t.Helper()
					probe.reset()
					if err := action(); err != nil {
						t.Fatal(stage, err)
					}
					got := probe.snapshot()
					sum := totals[stage]
					sum.Statements += got.Statements
					sum.Exchanges += got.Exchanges
					sum.Begins += got.Begins
					sum.Commits += got.Commits
					sum.Rollbacks += got.Rollbacks
					totals[stage] = sum
					if got != want {
						t.Errorf("%s item %d work=%+v want=%+v", stage, i, got, want)
					}
				}
				measure("receipt", acquisitionWork{1, 1, 0, 0, 0}, func() error {
					known, err := receipts.HasReceipt(ctx, org, key)
					if known {
						return fmt.Errorf("fresh command unexpectedly already accepted")
					}
					return err
				})
				var grant uploads.Session
				measure("grant", acquisitionWork{3, 3, 0, 0, 0}, func() error {
					var err error
					grant, err = uploader.Grant(ctx, org, uploads.Request{Key: key, SizeBytes: int64(len(data)), SHA256: content.Hash(data), MediaType: "application/xml"})
					return err
				})
				req, err := http.NewRequestWithContext(ctx, "PUT", grant.UploadURL, bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				for k, v := range grant.UploadHeaders {
					req.Header.Set(k, v)
				}
				response, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				if response.StatusCode != 200 {
					t.Fatalf("PUT: %d", response.StatusCode)
				}
				var verified uploads.Session
				measure("confirm", acquisitionWork{4, 4, 0, 0, 0}, func() error { var err error; verified, err = uploader.Confirm(ctx, org, grant.ID); return err })
				if verified.State != "verified" {
					t.Fatalf("verification: %+v", verified)
				}
				command := content.Command{Key: key, Source: content.Source{CorpusID: c.ID, Namespace: "archive-acquisition", RecordKey: fmt.Sprintf("member-%d", i)}, Revision: page.position, Position: page.position, Content: content.Text{Kind: "blob", BlobID: verified.BlobID, MediaType: "application/xml"}}
				measure("accept", page.accept, func() error {
					r, err := ingest.TrustedAccept(ctx, org, c.ID, command)
					if err == nil && !r.NewRevision {
						return fmt.Errorf("expected new revision")
					}
					return err
				})
				measure("replay-probe", acquisitionWork{1, 1, 0, 0, 0}, func() error {
					known, err := receipts.HasReceipt(ctx, org, key)
					if err == nil && !known {
						return fmt.Errorf("accepted receipt missing")
					}
					return err
				})
			}
			for stage, sum := range totals {
				t.Logf("page %s %s (%d members): %+v", page.name, stage, pageItems, sum)
			}
		})
	}
	mu.Lock()
	defer mu.Unlock()
	if gets != 93 || puts != 93 || heads != 0 {
		t.Fatalf("storage work PUT=%d verification GET=%d HEAD=%d; want 93/93/0", puts, gets, heads)
	}
	var n, events int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM ingestion_receipts WHERE organization=$1", org).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM change_events WHERE organization=$1", org).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if n != 93 || events != 186 {
		t.Fatalf("receipts/events %d/%d, want 93/186", n, events)
	}
	var position string
	if err := pool.QueryRow(ctx, "SELECT desired_position FROM records WHERE organization=$1 AND record_key='member-0'", org).Scan(&position); err != nil {
		t.Fatal(err)
	}
	if position != "13" {
		t.Fatalf("older archive member replaced current position: %s", position)
	}
}
