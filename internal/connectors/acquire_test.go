package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

type fakeRuns struct {
	target      Target
	checkpoints []string
	items       []bool
	finished    []*RunError
}

func (f *fakeRuns) LoadRun(context.Context, string, string) (Target, error) { return f.target, nil }
func (f *fakeRuns) CommitCheckpoint(_ context.Context, _, _ string, run int64, cp json.RawMessage, items bool) (bool, error) {
	if run != f.target.RunSequence {
		return false, nil
	}
	f.checkpoints = append(f.checkpoints, string(cp))
	f.items = append(f.items, items)
	f.target.Checkpoint = cp
	return true, nil
}
func (f *fakeRuns) FinishRun(_ context.Context, _, _ string, _ int64, failure *RunError) error {
	f.finished = append(f.finished, failure)
	return nil
}

type fakeIngest struct {
	accepted  []content.Command
	withdrawn []content.Withdrawal
	fail      error
	scopes    []corpus.Scope
	keys      map[string]bool
}

func (f *fakeIngest) Accept(_ context.Context, s corpus.Scope, c content.Command) (content.Receipt, error) {
	if f.fail != nil {
		return content.Receipt{}, f.fail
	}
	f.scopes = append(f.scopes, s)
	f.accepted = append(f.accepted, c)
	// Like the real store, a replayed idempotency key reserves no new revision.
	if f.keys == nil {
		f.keys = map[string]bool{}
	}
	fresh := !f.keys[c.Key]
	f.keys[c.Key] = true
	return content.Receipt{ID: "r", NewRevision: fresh}, nil
}
func (f *fakeIngest) Withdraw(_ context.Context, s corpus.Scope, w content.Withdrawal) (content.Receipt, error) {
	f.withdrawn = append(f.withdrawn, w)
	return content.Receipt{ID: "w"}, nil
}

func newAcquirer(t *testing.T, config string, secret string) (Acquirer, *fakeRuns, *fakeIngest) {
	t.Helper()
	registry, err := NewRegistry(Fixture{})
	if err != nil {
		t.Fatal(err)
	}
	sealer, _ := NewSealer(testDeploymentKey)
	target := Target{Instance: Instance{Organization: "org_a", ID: "connector_1", CorpusID: "corpus_1", Namespace: "wire", Kind: "fixture", Config: json.RawMessage(config), Enabled: true}, RunSequence: 3}
	if secret != "" {
		sealed, _ := sealer.Seal("org_a", "connector_1", []byte(secret))
		target.Sealed = &sealed
	}
	runs := &fakeRuns{target: target}
	ingest := &fakeIngest{}
	return Acquirer{Store: runs, Registry: registry, Sealer: sealer, Ingest: ingest}, runs, ingest
}

func TestAcquisitionIngestsThroughTheCommandPathThenCommitsTheCheckpoint(t *testing.T) {
	a, runs, ingest := newAcquirer(t, `{"script":[{"items":[{"record_key":"a","text":"Alpha"},{"record_key":"b","text":"Beta","revision":"b1"},{"record_key":"c","withdraw":true}]}]}`, "")
	if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
		t.Fatal(err)
	}
	if len(ingest.accepted) != 2 || len(ingest.withdrawn) != 1 {
		t.Fatalf("accepted %d withdrawn %d", len(ingest.accepted), len(ingest.withdrawn))
	}
	first := ingest.accepted[0]
	if first.Source != (content.Source{CorpusID: "corpus_1", Namespace: "wire", RecordKey: "a"}) || first.Revision == "" || first.Content.Text != "Alpha" {
		t.Fatalf("command %+v", first)
	}
	if first.Provenance["producer"] != "connector_1" || first.Provenance["producer_version"] != "fixture/v1" {
		t.Fatalf("provenance %+v", first.Provenance)
	}
	if ingest.accepted[1].Revision != "b1" {
		t.Fatalf("explicit revision lost: %+v", ingest.accepted[1])
	}
	scope := ingest.scopes[0]
	if scope.Organization != "org_a" || len(scope.Corpora) != 1 || scope.Corpora[0] != "corpus_1" || !scope.Allows("content:write") {
		t.Fatalf("scope %+v", scope)
	}
	if !IsConnectorKey(first.Key) || ingest.withdrawn[0].Key == first.Key || !IsConnectorKey(ingest.withdrawn[0].Key) {
		t.Fatalf("keys %q %q", first.Key, ingest.withdrawn[0].Key)
	}
	if len(runs.checkpoints) != 1 || runs.checkpoints[0] != `{"step":1}` || len(runs.finished) != 1 || runs.finished[0] != nil {
		t.Fatalf("checkpoints %v finished %v", runs.checkpoints, runs.finished)
	}
}

func TestReFetchedItemsReuseTheirIdempotencyKeys(t *testing.T) {
	config := `{"script":[{"items":[{"record_key":"a","text":"Alpha"}]},{"items":[{"record_key":"a","text":"Alpha"}]},{"items":[{"record_key":"a","text":"Alpha v2"}]}]}`
	a, runs, ingest := newAcquirer(t, config, "")
	for i := 0; i < 3; i++ {
		if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
			t.Fatal(err)
		}
	}
	k := ingest.accepted
	if k[0].Key != k[1].Key || k[0].Revision != k[1].Revision {
		t.Fatal("identical re-fetched item must replay the same key and revision")
	}
	if k[2].Key == k[0].Key || k[2].Revision == k[0].Revision {
		t.Fatal("changed content must be a new revision (correction)")
	}
	_ = runs
}

func TestCheckpointIsNotAdvancedWhenIngestionIsUnavailable(t *testing.T) {
	a, runs, ingest := newAcquirer(t, `{"script":[{"items":[{"record_key":"a","text":"Alpha"}]}]}`, "")
	ingest.fail = errors.New("database down")
	if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
		t.Fatal(err)
	}
	if len(runs.checkpoints) != 0 {
		t.Fatalf("checkpoint advanced past unaccepted items: %v", runs.checkpoints)
	}
	if f := runs.finished[0]; f == nil || f.Class != ClassTransient || f.Code != "ingestion_unavailable" {
		t.Fatalf("finish %+v", f)
	}
}

func TestAccessFailuresAreRecordedWithoutFetching(t *testing.T) {
	config := `{"requires_credential":true,"script":[{"items":[{"record_key":"a","text":"Alpha"}]}]}`
	a, runs, ingest := newAcquirer(t, config, `{"token":"fixture-revoked-test-token"}`)
	if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
		t.Fatal(err)
	}
	if f := runs.finished[0]; f == nil || f.Class != ClassAccess || f.Code != "unauthorized" || len(ingest.accepted) != 0 {
		t.Fatalf("finish %+v", f)
	}
	a, runs, ingest = newAcquirer(t, config, `{"token":"fixture-test-token"}`)
	past := time.Now().Add(-time.Minute)
	runs.target.Sealed.ExpiresAt = &past
	if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
		t.Fatal(err)
	}
	if f := runs.finished[0]; f == nil || f.Code != "credential_expired" || f.Class != ClassAccess || len(ingest.accepted) != 0 {
		t.Fatalf("expired credential %+v", f)
	}
}

func TestStaleOrDisabledRunsDoNothing(t *testing.T) {
	a, runs, ingest := newAcquirer(t, `{"script":[{"items":[{"record_key":"a","text":"Alpha"}]}]}`, "")
	if err := a.Run(context.Background(), "org_a", "connector_1", 2); err != nil {
		t.Fatal(err)
	}
	runs.target.Enabled = false
	if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
		t.Fatal(err)
	}
	if len(ingest.accepted) != 0 || len(runs.checkpoints) != 0 || len(runs.finished) != 0 {
		t.Fatalf("stale run acted: %v %v %v", ingest.accepted, runs.checkpoints, runs.finished)
	}
}

func TestARejectedItemIsReportedWithoutStallingTheSource(t *testing.T) {
	a, runs, ingest := newAcquirer(t, `{"script":[{"items":[{"record_key":"a","text":"Alpha"}]}]}`, "")
	ingest.fail = content.ErrInvalid
	if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
		t.Fatal(err)
	}
	if len(runs.checkpoints) != 1 {
		t.Fatalf("an item that can never be accepted must not block the checkpoint: %v", runs.checkpoints)
	}
	if f := runs.finished[0]; f == nil || f.Code != "item_rejected" || !f.Completed {
		t.Fatalf("finish %+v", f)
	}
}

func TestOnlyNewVersionsAdvanceLastItem(t *testing.T) {
	config := `{"script":[{"items":[{"record_key":"a","text":"Alpha"}]},{"items":[{"record_key":"a","text":"Alpha"}]},{"items":[{"record_key":"a","text":"Alpha v2"}]},{"items":[{"record_key":"b","withdraw":true}]}]}`
	a, runs, _ := newAcquirer(t, config, "")
	for i := 0; i < 4; i++ {
		if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
			t.Fatal(err)
		}
	}
	// New item, replayed duplicate, correction, withdrawal (no new Version).
	if want := []bool{true, false, true, false}; fmt.Sprint(runs.items) != fmt.Sprint(want) {
		t.Fatalf("last_item_at advanced %v, want %v", runs.items, want)
	}
}

type skippingConnector struct{ Fixture }

func (skippingConnector) Kind() string { return "skipping" }
func (skippingConnector) Fetch(context.Context, FetchRequest) (Page, error) {
	return Page{}, ErrNotDue
}

func TestANotDueRunFinishesAsSkippedWithoutPolling(t *testing.T) {
	a, runs, ingest := newAcquirer(t, `{"script":[]}`, "")
	registry, err := NewRegistry(skippingConnector{})
	if err != nil {
		t.Fatal(err)
	}
	a.Registry = registry
	runs.target.Kind = "skipping"
	if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
		t.Fatal(err)
	}
	if len(ingest.accepted) != 0 || len(runs.checkpoints) != 0 {
		t.Fatalf("skipped run acted: %v %v", ingest.accepted, runs.checkpoints)
	}
	if f := runs.finished[0]; f == nil || !f.Skipped || f.Code != "" || f.Completed {
		t.Fatalf("finish %+v", f)
	}
}
