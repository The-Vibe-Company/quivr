package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/audit"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/jackc/pgx/v5"
)

// The storage owner proves request-wide atomicity over a single-statement
// command and a nested-transaction command, not a fake implementing the audit.
func TestAuditTransactionAndAppendOnlyRetention(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := rebuildAdapterPool(t, ctx)
	org := fmt.Sprintf("adapter-audit-%d", time.Now().UnixNano())
	store := postgres.AuditStore{Pool: pool}
	corpora := postgres.Store{Pool: pool}
	hooks := 0
	var id string
	for _, outcome := range []string{"refused", "accepted"} {
		e := audit.Event{Actor: "key-id", Action: "corpus.create", TargetType: "corpus", Organization: org, Outcome: outcome, RequestID: "req-1", Detail: audit.Detail{Status: 201}}
		err := store.Record(ctx, &e, func(work context.Context) error {
			audit.AfterCommit(work, func(context.Context) { hooks++ })
			c, _, err := corpora.Create(work, org, corpus.CreateInput{Key: outcome, Name: "Audit", Resolved: corpus.Retrieval{}})
			id = c.ID
			e.TargetID = id
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		var exists bool
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM corpora WHERE organization=$1 AND id=$2)`, org, id).Scan(&exists); err != nil || exists != (outcome == "accepted") {
			t.Fatalf("%s action persisted=%v: %v", outcome, exists, err)
		}
	}
	if hooks != 1 {
		t.Fatalf("commit hooks ran for refused action: %d", hooks)
	}
	events, err := store.List(ctx, org, audit.Filter{Limit: 10})
	if err != nil || len(events) != 2 || events[0].ID <= events[1].ID || events[0].Time.IsZero() {
		t.Fatalf("audit entries: %+v %v", events, err)
	}
	// An invalid audit record must roll back the command, even if its own
	// adapter transaction or statement succeeded.
	e := audit.Event{Action: "bad", TargetType: "corpus", Organization: org, Outcome: "invalid"}
	if err = store.Record(ctx, &e, func(work context.Context) error {
		audit.AfterCommit(work, func(context.Context) { hooks++ })
		_, _, err := corpora.Create(work, org, corpus.CreateInput{Key: "must-rollback", Name: "Rollback"})
		return err
	}); err == nil {
		t.Fatal("invalid event committed")
	}
	if hooks != 1 {
		t.Fatalf("commit hook ran after audit failure: %d", hooks)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM corpora WHERE organization=$1 AND request_key='must-rollback'`, org).Scan(&count); err != nil || count != 0 {
		t.Fatalf("action survived audit failure: %d %v", count, err)
	}
	for _, sql := range []string{`UPDATE audit_events SET action='changed' WHERE organization=$1`, `DELETE FROM audit_events WHERE organization=$1`, `TRUNCATE audit_events`} {
		var err error
		if sql == "TRUNCATE audit_events" {
			_, err = pool.Exec(ctx, sql)
		} else {
			_, err = pool.Exec(ctx, sql, org)
		}
		if err == nil {
			t.Fatalf("append-only violation: %s", sql)
		}
	}
	// A withdrawal opens an adapter transaction and reads its own receipt
	// afterward. Both must use the audit transaction, including refusal rollback.
	for _, outcome := range []string{"refused", "accepted"} {
		nested := audit.Event{Actor: "key-id", Action: "record.withdraw", TargetType: "record", Organization: org, Outcome: outcome, RequestID: "nested", Detail: audit.Detail{Status: 202}}
		err = store.Record(ctx, &nested, func(work context.Context) error {
			receipt, err := (postgres.SubmissionStore{Pool: pool}).Withdraw(work, corpus.Scope{Organization: org}, content.Withdrawal{Key: "withdraw-" + outcome, Source: content.Source{CorpusID: id, Namespace: "audit", RecordKey: outcome}})
			nested.TargetID = receipt.RecordID
			if err == nil && (receipt.ID == "" || receipt.RecordID == "") {
				t.Fatal("nested receipt cannot see command writes")
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		var withdrawn bool
		err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM records WHERE organization=$1 AND id=$2 AND withdrawn)`, org, nested.TargetID).Scan(&withdrawn)
		if err != nil || withdrawn != (outcome == "accepted") {
			t.Fatalf("nested %s withdrawal=%v: %v", outcome, withdrawn, err)
		}
	}
	// First-revision acceptance normally uses an implicit batch. A request
	// transaction must own those writes so its refusal cannot leak accepted work.
	for _, outcome := range []string{"refused", "accepted"} {
		event := audit.Event{Action: "receipt.accept", TargetType: "record", Organization: org, Outcome: outcome}
		err = store.Record(ctx, &event, func(work context.Context) error {
			receipt, err := (postgres.SubmissionStore{Pool: pool}).Accept(work, corpus.Scope{Organization: org}, content.Command{
				Key: "accept-" + outcome, Source: content.Source{CorpusID: id, Namespace: "audit", RecordKey: "accept-" + outcome},
				Content: content.Text{Kind: "text", Text: "Audited acceptance"},
			})
			event.TargetID = receipt.RecordID
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		var exists bool
		err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ingestion_receipts WHERE organization=$1 AND request_key=$2)`, org, "accept-"+outcome).Scan(&exists)
		if err != nil || exists != (outcome == "accepted") {
			t.Fatalf("%s acceptance persisted=%v: %v", outcome, exists, err)
		}
	}
	// A dry-run caches a confirmation estimate but produces no audit entry.
	estimate := audit.Event{Action: "operation.backfill", Organization: org}
	err = store.Record(ctx, &estimate, func(work context.Context) error {
		if err := (postgres.BackfillStore{Pool: pool}).RecordEstimate(work, org, id, "dry", []byte(`{}`), operations.BackfillEstimate{Versions: 2}); err != nil {
			return err
		}
		return audit.ErrReadOnly
	})
	if err != nil || estimate.ID != 0 {
		t.Fatalf("dry-run audit: %+v %v", estimate, err)
	}
	_, cached, err := (postgres.BackfillStore{Pool: pool}).BackfillEstimate(ctx, org, id, "dry")
	if err != nil || cached.Versions != 2 {
		t.Fatalf("confirmation estimate lost: %+v %v", cached, err)
	}
	// Query filtering and cross-organization isolation are owned by SQL.
	filtered, err := store.List(ctx, org, audit.Filter{Limit: 10, Action: "record.withdraw", Actor: "key-id", TargetType: "record", Since: events[0].Time.Add(-time.Second)})
	if err != nil || len(filtered) != 2 {
		t.Fatalf("filtered entries: %+v %v", filtered, err)
	}
	other, err := store.List(ctx, "another-org", audit.Filter{Limit: 10})
	if err != nil || len(other) != 0 {
		t.Fatalf("organization isolation: %+v %v", other, err)
	}
	// PUBLIC must not be able to invoke owner-privileged retention. Operators
	// grant a separate runtime role explicitly; the migration owner still works.
	func() {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Release()
		role := pgx.Identifier{fmt.Sprintf("audit_untrusted_%d", time.Now().UnixNano())}.Sanitize()
		if _, err = conn.Exec(ctx, "CREATE ROLE "+role); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_, _ = conn.Exec(context.WithoutCancel(ctx), "RESET ROLE")
			_, _ = conn.Exec(context.WithoutCancel(ctx), "DROP ROLE "+role)
		}()
		if _, err = conn.Exec(ctx, "SET ROLE "+role); err != nil {
			t.Fatal(err)
		}
		if _, err = conn.Exec(ctx, `SELECT prune_audit_events(12,1000)`); err == nil {
			t.Fatal("untrusted role invoked privileged audit pruning")
		}
	}()
	// Seed historical INSERTs instead of sleeping or mutating immutable rows.
	if _, err = pool.Exec(ctx, `INSERT INTO audit_events(occurred_at,actor,action,target_type,target_id,organization,outcome,request_id,detail) VALUES(now()-interval '13 months','key-id','old','corpus','old',$1,'accepted','old','{}')`, org); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{`SELECT prune_audit_events(12,NULL)`, `SELECT prune_audit_events(NULL,1000)`} {
		if _, err := pool.Exec(ctx, sql); err == nil {
			t.Fatalf("NULL retention arguments must refuse: %s", sql)
		}
	}
	if n, err := store.PruneAudit(ctx, 12, 1000); err != nil || n != 1 {
		t.Fatalf("retention: %d %v", n, err)
	}
	events, err = store.List(ctx, org, audit.Filter{Limit: 10})
	if err != nil || len(events) != 6 {
		t.Fatalf("retention removed live entries: %+v %v", events, err)
	}
}
