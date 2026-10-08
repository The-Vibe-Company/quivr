package postgres_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Queue observations share the round trip of the event
// they follow. Every other writer waits while the Organization journal is
// locked, so an observation-only round trip delays the whole Organization.
func TestJournalEventsCarryQueueObservationsInTheirRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	scope := corpus.Scope{Organization: fmt.Sprintf("adapter-journal-trips-%d", time.Now().UnixNano())}
	c, _, err := (postgres.Store{Pool: pool}).Create(ctx, scope.Organization, corpus.CreateInput{Key: "trips", Name: "Trips", Resolved: corpus.Retrieval{}})
	if err != nil {
		t.Fatal(err)
	}
	trips := &lockedRoundTrips{locked: map[*pgx.Conn]bool{}}
	cfg := pool.Config()
	cfg.ConnConfig.Tracer = trips
	traced, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()
	store := contentStores(traced)
	// The first revision appends in its fence batch; a later revision of the
	// same Record appends after its guarded reads.
	for i, text := range []string{"first harbour text", "second harbour text"} {
		if _, err = store.Accept(ctx, scope, content.Command{Key: fmt.Sprintf("revision-%d", i), Source: content.Source{CorpusID: c.ID, Namespace: "trips", RecordKey: "record"}, Content: content.Text{Kind: "text", Text: text}}); err != nil {
			t.Fatal(err)
		}
	}
	trips.mu.Lock()
	extra := trips.observationOnly
	trips.mu.Unlock()
	if len(extra) > 0 {
		t.Errorf("round trips under the journal lock carried only queue observation work: %q", extra)
	}
	var head int64
	var observed bool
	if err = pool.QueryRow(ctx, `SELECT last_sequence,EXISTS(SELECT 1 FROM queue_enrichment_records r WHERE r.organization=$1)
 FROM organization_journals WHERE organization=$1`, scope.Organization).Scan(&head, &observed); err != nil {
		t.Fatal(err)
	}
	if head != 4 || !observed {
		t.Fatalf("journal head %d (want four events), Record observed %v", head, observed)
	}
}

// lockedRoundTrips records each round trip sent between acquiring the
// Organization journal row and ending the transaction.
type lockedRoundTrips struct {
	mu              sync.Mutex
	locked          map[*pgx.Conn]bool
	observationOnly []string
}

func journalLock(sql string) bool {
	return strings.Contains(sql, "FROM organization_journals WHERE organization=$1 FOR UPDATE")
}

func (r *lockedRoundTrips) trip(c *pgx.Conn, sqls []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.locked[c] {
		observation, event := false, false
		for _, sql := range sqls {
			observation = observation || strings.Contains(sql, "queue_enrichment_records")
			event = event || strings.Contains(sql, "INSERT INTO change_events")
		}
		if observation && !event {
			r.observationOnly = append(r.observationOnly, strings.Join(strings.Fields(sqls[0]), " "))
		}
	}
	for _, sql := range sqls {
		if journalLock(sql) {
			r.locked[c] = true
		}
		if s := strings.TrimSpace(strings.ToLower(sql)); s == "commit" || s == "rollback" {
			delete(r.locked, c)
		}
	}
}

func (r *lockedRoundTrips) TraceQueryStart(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	r.trip(c, []string{d.SQL})
	return ctx
}
func (r *lockedRoundTrips) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func (r *lockedRoundTrips) TraceBatchStart(ctx context.Context, c *pgx.Conn, d pgx.TraceBatchStartData) context.Context {
	sqls := make([]string, 0, d.Batch.Len())
	for _, q := range d.Batch.QueuedQueries {
		sqls = append(sqls, q.SQL)
	}
	r.trip(c, sqls)
	return ctx
}
func (r *lockedRoundTrips) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {}
func (r *lockedRoundTrips) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData)     {}
