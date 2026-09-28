package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/changes"
	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/connectors/m365mail"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

// TestConnectorInstancesPersistSecretsSealedAndScheduleOneRunAtATime drives
// the real store: idempotent create, namespace ownership, sealed credentials,
// leased claims, stale-run fencing and transactional health events.
func TestConnectorInstancesPersistSecretsSealedAndScheduleOneRunAtATime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	scope := corpus.Scope{Organization: fmt.Sprintf("adapter-connectors-%d", time.Now().UnixNano()), Actions: []string{"corpora:write", "connectors:write", "connectors:read"}, Corpora: []string{"*"}}
	c, _, err := corpus.Service{Store: postgres.Store{Pool: pool}}.Create(ctx, scope, corpus.CreateInput{Key: "c", Name: "Connectors"})
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.ConnectorStore{ContentStore: postgres.ContentStore{Pool: pool}}
	registry, _ := connectors.NewRegistry(connectors.Fixture{})
	sealer, _ := connectors.NewSealer("adapter-test-credential-key-0123456789")
	service := connectors.Service{Store: store, Registry: registry, Sealer: sealer, MinInterval: time.Second}
	one := 1
	input := connectors.CreateInput{Key: "k1", CorpusID: c.ID, Namespace: "wire", Kind: "fixture", Config: json.RawMessage(`{"script":[]}`), IntervalSeconds: &one, Secret: json.RawMessage(`{"token":"fixture-test-secret-adapter"}`)}
	created, err := service.Create(ctx, scope, input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Credential == nil || created.Credential.Version != 1 || created.Health.State != connectors.HealthActive {
		t.Fatalf("created %+v", created)
	}
	replay, err := service.Create(ctx, scope, input)
	if err != nil || replay.ID != created.ID {
		t.Fatalf("replay %v %+v", err, replay)
	}
	changed := input
	changed.Namespace = "other"
	if _, err = service.Create(ctx, scope, changed); !errors.Is(err, connectors.ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	second := input
	second.Key = "k2"
	if _, err = service.Create(ctx, scope, second); !errors.Is(err, connectors.ErrNamespaceInUse) {
		t.Fatalf("namespace: %v", err)
	}
	var ciphertext []byte
	if err = pool.QueryRow(ctx, "SELECT ciphertext FROM connector_credentials WHERE organization=$1 AND connector_id=$2", scope.Organization, created.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("fixture-test-secret")) {
		t.Fatal("secret stored in plaintext")
	}

	// Exactly one claimant owns a due run; a released lease can be claimed again.
	claim := func() []postgres.ConnectorRun {
		runs, err := store.ClaimConnectorRuns(ctx, time.Minute, 1000)
		if err != nil {
			t.Fatal(err)
		}
		var mine []postgres.ConnectorRun
		for _, r := range runs {
			if r.Organization == scope.Organization {
				mine = append(mine, r)
			} else if err := store.ReleaseConnectorRun(ctx, r); err != nil {
				t.Fatal(err) // never hold another Organization's run
			}
		}
		return mine
	}
	first := claim()
	if len(first) != 1 || first[0].ConnectorID != created.ID {
		t.Fatalf("claim %+v", first)
	}
	if again := claim(); len(again) != 0 {
		t.Fatalf("leased run claimed twice: %+v", again)
	}
	if err = store.ReleaseConnectorRun(ctx, first[0]); err != nil {
		t.Fatal(err)
	}
	run := claim()
	if len(run) != 1 || run[0].Run != first[0].Run {
		t.Fatalf("re-dispatch must target the same run: %+v", run)
	}
	target, err := store.LoadRun(ctx, scope.Organization, created.ID)
	if err != nil || target.Sealed == nil {
		t.Fatalf("load %v %+v", err, target)
	}
	if plain, err := sealer.Open(scope.Organization, created.ID, *target.Sealed); err != nil || string(plain) != `{"token":"fixture-test-secret-adapter"}` {
		t.Fatalf("open %v %s", err, plain)
	}
	if ok, err := store.CommitCheckpoint(ctx, scope.Organization, created.ID, run[0].Run+1, connectors.Progress{Checkpoint: json.RawMessage(`{"step":9}`), Items: true, Reads: 50}); ok || err != nil {
		t.Fatalf("stale checkpoint committed: %v %v", ok, err)
	}
	if fresh, _ := store.ReadConnector(ctx, scope.Organization, created.ID); fresh.Health.Usage != nil || fresh.Health.Diagnostics != nil {
		t.Fatalf("a kind that never reported reads has no usage: %+v", fresh.Health)
	}
	if ok, err := store.CommitCheckpoint(ctx, scope.Organization, created.ID, run[0].Run, connectors.Progress{Checkpoint: json.RawMessage(`{"step":0}`), Reads: 30, Diagnostics: json.RawMessage(`{"window":1}`)}); !ok || err != nil {
		t.Fatalf("checkpoint: %v %v", ok, err)
	}
	if ok, err := store.CommitCheckpoint(ctx, scope.Organization, created.ID, run[0].Run, connectors.Progress{Checkpoint: json.RawMessage(`{"step":1}`), Items: true, Reads: 12}); !ok || err != nil {
		t.Fatalf("checkpoint: %v %v", ok, err)
	}
	// Reads accumulate per UTC day; a page without diagnostics keeps the last ones.
	counted, err := store.LoadRun(ctx, scope.Organization, created.ID)
	if err != nil || counted.ReadsToday != 42 || counted.Health.Usage == nil || counted.Health.Usage.ItemsRead != 42 || counted.Health.Usage.PreviousDayItemsRead != 0 || string(counted.Health.Diagnostics) != `{"window": 1}` {
		t.Fatalf("usage %v %+v %s", err, counted.Health.Usage, counted.Health.Diagnostics)
	}
	// A new UTC day rolls today's reads over to the previous day.
	if _, err = pool.Exec(ctx, "UPDATE connector_instances SET usage_day=usage_day-1 WHERE organization=$1 AND id=$2", scope.Organization, created.ID); err != nil {
		t.Fatal(err)
	}
	if rolled, _ := store.ReadConnector(ctx, scope.Organization, created.ID); rolled.Health.Usage == nil || rolled.Health.Usage.ItemsRead != 0 || rolled.Health.Usage.PreviousDayItemsRead != 42 {
		t.Fatalf("rollover on read %+v", rolled.Health.Usage)
	}
	if ok, err := store.CommitCheckpoint(ctx, scope.Organization, created.ID, run[0].Run, connectors.Progress{Checkpoint: json.RawMessage(`{"step":1}`), Reads: 5}); !ok || err != nil {
		t.Fatalf("checkpoint: %v %v", ok, err)
	}
	if rolled, _ := store.ReadConnector(ctx, scope.Organization, created.ID); rolled.Health.Usage.ItemsRead != 5 || rolled.Health.Usage.PreviousDayItemsRead != 42 {
		t.Fatalf("rollover on commit %+v", rolled.Health.Usage)
	}
	feed := changes.Service{Journal: store.ContentStore, Key: []byte("adapter-cursor-key-0123456789abcdef")}
	start, err := feed.Start(ctx, scope, c.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishRun(ctx, scope.Organization, created.ID, run[0].Run, &connectors.RunError{Class: connectors.ClassAccess, Code: "unauthorized"}); err != nil {
		t.Fatal(err)
	}
	// A duplicate finish of the same run is fenced.
	if err = store.FinishRun(ctx, scope.Organization, created.ID, run[0].Run, nil); err != nil {
		t.Fatal(err)
	}
	after, err := store.LoadRun(ctx, scope.Organization, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.RunSequence != run[0].Run+1 || string(after.Checkpoint) != `{"step": 1}` || after.Health.State != connectors.HealthAccessError || after.Health.LastError == nil || after.Health.LastError.Code != "unauthorized" {
		t.Fatalf("after finish %+v %s", after.Health, after.Checkpoint)
	}
	if len(claim()) != 0 {
		t.Fatal("next run must wait for its interval")
	}
	// A later transient failure keeps the unresolved access error.
	time.Sleep(1100 * time.Millisecond)
	next := claim()
	if len(next) != 1 || next[0].Run != run[0].Run+1 {
		t.Fatalf("next run %+v", next)
	}
	if err = store.FinishRun(ctx, scope.Organization, created.ID, next[0].Run, &connectors.RunError{Class: connectors.ClassTransient, Code: "source_unavailable", RetryAfter: 10 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	// RetryAfter longer than the interval defers the next run past the source's reset.
	var deferredFor float64
	if err = pool.QueryRow(ctx, "SELECT EXTRACT(EPOCH FROM next_run_at-now()) FROM connector_instances WHERE organization=$1 AND id=$2", scope.Organization, created.ID).Scan(&deferredFor); err != nil || deferredFor < 590 {
		t.Fatalf("next run not deferred: %v %v", err, deferredFor)
	}
	if still, _ := store.ReadConnector(ctx, scope.Organization, created.ID); still.Health.State != connectors.HealthAccessError || still.Health.LastError.Code != "source_unavailable" {
		t.Fatalf("transient failure cleared the access error: %+v", still.Health)
	}
	// A skipped run (source not due) polled nothing: no success, no error, no health change.
	if err = store.FinishRun(ctx, scope.Organization, created.ID, next[0].Run+1, &connectors.RunError{Skipped: true}); err != nil {
		t.Fatal(err)
	}
	skipped, err := store.LoadRun(ctx, scope.Organization, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if skipped.RunSequence != next[0].Run+2 || skipped.Health.LastSuccessAt != nil || skipped.Health.State != connectors.HealthAccessError || skipped.Health.LastError.Code != "source_unavailable" {
		t.Fatalf("skipped run changed health: %+v", skipped.Health)
	}
	disabled, err := service.Disable(ctx, scope, created.ID)
	if err != nil || disabled.Enabled || disabled.Health.State != connectors.HealthDisabled {
		t.Fatalf("disable %v %+v", err, disabled)
	}
	page, err := feed.Read(ctx, scope, c.ID, start, 10)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range page.Items {
		kinds = append(kinds, e.Type)
	}
	want := []string{"connector.health_changed", "connector.disabled", "connector.health_changed"}
	if fmt.Sprint(kinds) != fmt.Sprint(want) {
		t.Fatalf("events %v want %v", kinds, want)
	}
	// Disabling released the Source Namespace for a replacement instance.
	replacement, err := service.Create(ctx, scope, second)
	if err != nil {
		t.Fatalf("replacement: %v", err)
	}
	// Leave nothing scheduled behind for the rest of the verification run.
	defer func() {
		if _, err := service.Disable(context.Background(), scope, replacement.ID); err != nil {
			t.Error(err)
		}
	}()
	if _, err = service.ReplaceCredential(ctx, scope, created.ID, connectors.CredentialInput{Key: "r1", Secret: json.RawMessage(`{"token":"fixture-test-token"}`)}); !errors.Is(err, connectors.ErrDisabled) {
		t.Fatalf("replace on disabled: %v", err)
	}
}

// TestReceiptProbeRecognisesOnlyTheAcceptedIdempotencyKey backs the connector
// pre-download skip: a key is known only once its command was accepted.
func TestReceiptProbeRecognisesOnlyTheAcceptedIdempotencyKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	scope := corpus.Scope{Organization: fmt.Sprintf("adapter-probe-%d", time.Now().UnixNano()), Actions: []string{"corpora:write", "content:write"}, Corpora: []string{"*"}}
	c, _, err := corpus.Service{Store: postgres.Store{Pool: pool}}.Create(ctx, scope, corpus.CreateInput{Key: "c", Name: "Probe"})
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.ContentStore{Pool: pool}
	key := connectors.KeyPrefix + "probe-r1"
	if known, err := store.HasReceipt(ctx, scope.Organization, key); err != nil || known {
		t.Fatalf("before acceptance: %v %v", known, err)
	}
	command := content.Command{Key: key, Source: content.Source{CorpusID: c.ID, Namespace: "mail", RecordKey: "<m@example.org>"}, Revision: "r1", Content: content.Text{Kind: "text", Text: "Bonjour"}}
	if _, err = (content.Service{Repository: store, Catalog: store, BlobSource: store}).Accept(ctx, scope, command); err != nil {
		t.Fatal(err)
	}
	if known, err := store.HasReceipt(ctx, scope.Organization, key); err != nil || !known {
		t.Fatalf("after acceptance: %v %v", known, err)
	}
	if known, _ := store.HasReceipt(ctx, scope.Organization, connectors.KeyPrefix+"probe-r2"); known {
		t.Fatal("another revision's key must not be known")
	}
	if known, _ := store.HasReceipt(ctx, "another-org", key); known {
		t.Fatal("receipts are Organization-scoped")
	}
}

// TestBackfillIsBoundedAtCreationOnly: backfill_since is checked against the
// clock when an instance is created, never when its credential is rotated
// later, when the stored window is naturally older than 7 days.
func TestBackfillIsBoundedAtCreationOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	scope := corpus.Scope{Organization: fmt.Sprintf("adapter-backfill-%d", time.Now().UnixNano()), Actions: []string{"corpora:write", "connectors:write", "connectors:read"}, Corpora: []string{"*"}}
	c, _, err := corpus.Service{Store: postgres.Store{Pool: pool}}.Create(ctx, scope, corpus.CreateInput{Key: "c", Name: "Backfill"})
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.ConnectorStore{ContentStore: postgres.ContentStore{Pool: pool}}
	registry, _ := connectors.NewRegistry(m365mail.New("https://login.invalid", "https://graph.invalid/v1.0", nil))
	sealer, _ := connectors.NewSealer("adapter-test-credential-key-0123456789")
	service := connectors.Service{Store: store, Registry: registry, Sealer: sealer, MinInterval: time.Second}
	config := func(since time.Time) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"tenant_id":"00000000-0000-0000-0000-000000000000","mailbox":"monitoring@example.org","backfill_since":%q}`, since.UTC().Format(time.RFC3339)))
	}
	secret := json.RawMessage(`{"client_id":"11111111-1111-1111-1111-111111111111","client_secret":"adapter-secret-not-real"}`)
	if _, err = service.Create(ctx, scope, connectors.CreateInput{Key: "old", CorpusID: c.ID, Namespace: "mail-old", Kind: "m365_mail", Config: config(time.Now().Add(-8 * 24 * time.Hour)), Secret: secret}); !errors.Is(err, connectors.ErrInvalidConfig) {
		t.Fatalf("an 8-day backfill must be refused at creation: %v", err)
	}
	created, err := service.Create(ctx, scope, connectors.CreateInput{Key: "ok", CorpusID: c.ID, Namespace: "mail", Kind: "m365_mail", Config: config(time.Now().Add(-time.Hour)), Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	// Age the stored window past 7 days, as time would.
	if _, err = pool.Exec(ctx, `UPDATE connector_instances SET config=$3 WHERE organization=$1 AND id=$2`, scope.Organization, created.ID, config(time.Now().Add(-30*24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err = service.ReplaceCredential(ctx, scope, created.ID, connectors.CredentialInput{Key: "rotate", Secret: secret}); err != nil {
		t.Fatalf("rotation after the backfill window aged: %v", err)
	}
}
