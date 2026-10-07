package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This owns digest-only request arbitration and the optional full audit copy.
// A SQL assertion is necessary: successful replay alone cannot show that the
// adapter stopped retaining the request. Work/Receipt reads prove it remains usable.
func TestCompactReceiptDigestAndOptionalAudit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	if err := postgres.ActivateCompactStorage(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, detail := range []bool{false, true} {
		t.Run(fmt.Sprint(detail), func(t *testing.T) {
			org := fmt.Sprintf("compact-receipt-%d-%v", time.Now().UnixNano(), detail)
			scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
			c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "one", Name: "Compact receipts"})
			if err != nil {
				t.Fatal(err)
			}
			store := postgres.SubmissionStore{Pool: pool, RetainImportAuditDetail: detail}
			cmd := content.Command{Key: "submit", Source: content.Source{CorpusID: c.ID, Namespace: "example", RecordKey: "one"}, Content: content.Text{Kind: "text", Text: "Durable input"}}
			r, err := store.Accept(ctx, scope, cmd)
			if err != nil {
				t.Fatal(err)
			}
			var copies bool
			var digest []byte
			if err = pool.QueryRow(ctx, `SELECT octet_length(canonical_request)>0 OR command<>'{}'::jsonb,request_digest FROM ingestion_receipts WHERE organization=$1 AND id=$2`, org, r.ID).Scan(&copies, &digest); err != nil {
				t.Fatal(err)
			}
			if copies != detail || len(digest) != 32 {
				t.Fatalf("audit copies=%v digest bytes=%d; want %v/32", copies, len(digest), detail)
			}
			work, _, err := (postgres.MaterializationStore{Pool: pool}).Work(ctx, org, r.ID)
			if err != nil || work.Command.Content.Text != "Durable input" {
				t.Fatalf("work input lost: %+v %v", work, err)
			}
			objects := &objectMemory{objects: map[string][]byte{}}
			if err = (content.Service{Materialization: postgres.MaterializationStore{Pool: pool}, Blobs: objects}).Materialize(ctx, org, r.ID); err != nil {
				t.Fatal(err)
			}
			normalizer := postgres.NormalizationStore{Pool: pool, Blobs: objects, RetainImportAuditDetail: detail}
			n := content.Normalized{Manifest: content.Blob{Key: "manifest", SHA256: "digest", Size: 1}, Provenance: content.Normalization{PluginID: "example.normalize", PluginVersion: "1.0.0", InvocationID: "call", IdempotencyKey: plugins.NormalizerKey("plan-1", "normalizer", org, work.VersionID, content.Hash([]byte("Durable input")))}, Extensions: content.Extensions{"example.normalize.metadata": {SchemaVersion: "1", Data: map[string]any{"heading": "Preserved"}}}}

			var activation *activateAfterRoundtrip
			if !detail {
				if _, err = pool.Exec(ctx, `UPDATE storage_state SET compact=false`); err != nil {
					t.Fatal(err)
				}
				activation = &activateAfterRoundtrip{activate: func() error { return postgres.ActivateCompactStorage(ctx, pool) }}
				cfg := pool.Config()
				cfg.ConnConfig.Tracer = activation
				racedPool, err := pgxpool.NewWithConfig(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer racedPool.Close()
				normalizer.Pool = racedPool
			}
			stored, err := normalizer.SaveNormalized(ctx, org, work.VersionID, n)
			if activation != nil && activation.err != nil {
				t.Fatal(activation.err)
			}
			if err != nil || stored.Extensions["example.normalize.metadata"].Data["heading"] != "Preserved" {
				t.Fatalf("normalization outcome %+v %v", stored, err)
			}
			if stored.Outcome != content.OutcomeNormalized {
				t.Errorf("default outcome lost: %q", stored.Outcome)
			}
			if err = normalizer.RecordConflict(ctx, org, work.VersionID, content.NormalizationConflict{InvocationID: "later-call", ManifestSHA256: "different"}); err != nil {
				t.Fatal(err)
			}
			version, err := (postgres.VersionStore{Pool: pool}).Version(ctx, org, work.RecordID, work.VersionID)
			if err != nil || len(version.Diagnostics) != 1 || version.Diagnostics[0].Code != content.CodeNormalizerConflict {
				t.Errorf("public version diagnostics: %+v %v", version.Diagnostics, err)
			}
			if (stored.Provenance.InvocationID != "") != detail || stored.Provenance.IdempotencyKey != n.Provenance.IdempotencyKey {
				t.Fatalf("optional invocation ledger %+v", stored.Provenance)
			}
			var outcomeKey string
			var extensionsSize int
			if err = pool.QueryRow(ctx, `SELECT coalesce(outcome_key,''),octet_length(extensions::text) FROM normalizations WHERE organization=$1 AND version_id=$2`, org, work.VersionID).Scan(&outcomeKey, &extensionsSize); err != nil {
				t.Fatal(err)
			}
			if !detail && (outcomeKey == "" || extensionsSize != 2) {
				t.Fatalf("bulky outcome retained in default row key=%q extension bytes=%d", outcomeKey, extensionsSize)
			}

			replayed, err := store.Accept(ctx, scope, cmd)
			if err != nil || replayed.ID != r.ID || replayed.Source != cmd.Source {
				t.Fatalf("digest replay: %+v %v", replayed, err)
			}
			changed := cmd
			changed.Position = "2"
			if _, err = store.Accept(ctx, scope, changed); !errors.Is(err, content.ErrConflict) {
				t.Fatalf("same content with changed envelope must conflict: %v", err)
			}
			w := content.Withdrawal{Key: cmd.Key, Source: cmd.Source}
			withdrawn, err := store.Withdraw(ctx, scope, w)
			if err != nil || withdrawn.ID == r.ID {
				t.Fatalf("route-family scope: %+v %v", withdrawn, err)
			}
			replayed, err = store.Withdraw(ctx, scope, w)
			if err != nil || replayed.ID != withdrawn.ID || replayed.Source != w.Source {
				t.Fatalf("withdrawal replay: %+v %v", replayed, err)
			}
			w.Source.RecordKey = "other"
			if _, err = store.Withdraw(ctx, scope, w); !errors.Is(err, content.ErrConflict) {
				t.Fatalf("withdrawal digest conflict: %v", err)
			}
			var execution map[string]json.RawMessage
			var encoded []byte
			if err = pool.QueryRow(ctx, `SELECT command FROM accepted_revisions WHERE organization=$1 AND record_id=$2`, org, work.RecordID).Scan(&encoded); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(encoded, &execution); err != nil {
				t.Fatal(err)
			}
			if !detail && len(execution["idempotency_key"]) > 0 {
				t.Fatal("execution input retained audit request key")
			}
		})
	}
}

// The external SQL driver completes one roundtrip before activation. The store
// must fence and recheck its candidate format even when that first read is stale.
type activateAfterRoundtrip struct {
	once     sync.Once
	activate func() error
	err      error
}

func (a *activateAfterRoundtrip) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}
func (a *activateAfterRoundtrip) TraceQueryEnd(_ context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if data.Err == nil {
		a.once.Do(func() { a.err = a.activate() })
	}
}
