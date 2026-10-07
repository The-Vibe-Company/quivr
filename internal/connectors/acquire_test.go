package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

type fakeRuns struct {
	target      Target
	checkpoints []string
	items       []bool
	progress    []Progress
	finished    []*RunError
}

func (f *fakeRuns) LoadRun(context.Context, string, string) (Target, error) { return f.target, nil }
func (f *fakeRuns) CommitCheckpoint(_ context.Context, _, _ string, run int64, p Progress) (bool, error) {
	if run != f.target.RunSequence {
		return false, nil
	}
	f.checkpoints = append(f.checkpoints, string(p.Checkpoint))
	f.items = append(f.items, p.Items)
	f.progress = append(f.progress, p)
	f.target.Checkpoint = p.Checkpoint
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

func (f *fakeIngest) TrustedAccept(_ context.Context, org, corpusID string, c content.Command) (content.Receipt, error) {
	if f.fail != nil {
		return content.Receipt{}, f.fail
	}
	f.scopes = append(f.scopes, corpus.Scope{Organization: org, Corpora: []string{corpusID}})
	f.accepted = append(f.accepted, c)
	// Like the real store, a replayed idempotency key reserves no new revision.
	if f.keys == nil {
		f.keys = map[string]bool{}
	}
	fresh := !f.keys[c.Key]
	f.keys[c.Key] = true
	return content.Receipt{ID: "r", NewRevision: fresh}, nil
}
func (f *fakeIngest) TrustedWithdraw(_ context.Context, org, corpusID string, w content.Withdrawal) (content.Receipt, error) {
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
	if scope.Organization != "org_a" || len(scope.Corpora) != 1 || scope.Corpora[0] != "corpus_1" {
		t.Fatalf("scope %+v", scope)
	}
	if !IsConnectorKey(first.Key) || ingest.withdrawn[0].Key == first.Key || !IsConnectorKey(ingest.withdrawn[0].Key) {
		t.Fatalf("keys %q %q", first.Key, ingest.withdrawn[0].Key)
	}
	if len(runs.checkpoints) != 1 || runs.checkpoints[0] != `{"step":1}` || len(runs.finished) != 1 || runs.finished[0] != nil {
		t.Fatalf("checkpoints %v finished %v", runs.checkpoints, runs.finished)
	}
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
	a, runs, ingest := newAcquirer(t, config, "")
	for i := 0; i < 4; i++ {
		if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
			t.Fatal(err)
		}
	}
	// A re-fetched identical item replays its key and revision; changed content
	// is a new revision (a correction) under a new key.
	k := ingest.accepted
	if k[0].Key != k[1].Key || k[0].Revision != k[1].Revision {
		t.Fatal("identical re-fetched item must replay the same key and revision")
	}
	if k[2].Key == k[0].Key || k[2].Revision == k[0].Revision {
		t.Fatal("changed content must be a new revision (correction)")
	}
	// New item, replayed duplicate, correction, withdrawal (no new Version).
	if want := []bool{true, false, true, false}; fmt.Sprint(runs.items) != fmt.Sprint(want) {
		t.Fatalf("last_item_at advanced %v, want %v", runs.items, want)
	}
}

type skippingConnector struct{ Fixture }

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

// stubConnector serves scripted pages or errors and records every request.
type stubConnector struct {
	pages    []Page
	err      error
	requests []FetchRequest
}

func (s *stubConnector) Fetch(_ context.Context, r FetchRequest) (Page, error) {
	s.requests = append(s.requests, r)
	if len(s.requests) > len(s.pages) {
		return Page{}, s.err
	}
	return s.pages[len(s.requests)-1], nil
}

func TestASealedCredentialThatCannotBeOpenedIsAnAccessErrorNotACrash(t *testing.T) {
	keyless, _ := NewKeylessSealer(testCursorKey)
	rotated, _ := NewSealer("another-test-credential-key-0123456789abcdef")
	for name, sealer := range map[string]Sealer{"key absent": keyless, "key changed": rotated} {
		t.Run(name, func(t *testing.T) {
			a, runs, ingest := newAcquirer(t, `{"script":[{"items":[{"record_key":"a","text":"Alpha"}]}]}`, `{"token":"fixture-test-secret-acquire"}`)
			a.Sealer = sealer
			if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
				t.Fatal(err)
			}
			if len(runs.finished) != 1 || runs.finished[0] == nil || runs.finished[0].Class != ClassAccess || runs.finished[0].Code != "credential_unreadable" {
				t.Fatalf("finished %+v", runs.finished)
			}
			if len(ingest.accepted) != 0 || len(runs.checkpoints) != 0 {
				t.Fatal("acquired without an openable credential")
			}
		})
	}
}

func stubAcquirer(t *testing.T, stub *stubConnector, readsToday int64) (Acquirer, *fakeRuns) {
	t.Helper()
	registry, err := NewRegistry(stub)
	if err != nil {
		t.Fatal(err)
	}
	sealer, _ := NewSealer(testDeploymentKey)
	runs := &fakeRuns{target: Target{Instance: Instance{Organization: "org_a", ID: "connector_1", CorpusID: "corpus_1", Namespace: "wire", Kind: "stub", Config: json.RawMessage(`{}`), Enabled: true}, RunSequence: 1, ReadsToday: readsToday}}
	return Acquirer{Store: runs, Registry: registry, Sealer: sealer, Ingest: &fakeIngest{}}, runs
}

func TestARateLimitedSourceDefersTheNextRunUntilItsReset(t *testing.T) {
	stub := &stubConnector{err: &Error{Class: ClassTransient, Code: "rate_limited", RetryAfter: 7 * time.Minute}}
	a, runs := stubAcquirer(t, stub, 0)
	if err := a.Run(context.Background(), "org_a", "connector_1", 1); err != nil {
		t.Fatal(err)
	}
	if f := runs.finished[0]; f == nil || f.Code != "rate_limited" || f.Class != ClassTransient || f.RetryAfter != 7*time.Minute {
		t.Fatalf("finish %+v", f)
	}
}

func TestPagesReportReadsAndDiagnosticsWithTheirCheckpoint(t *testing.T) {
	for _, tc := range []struct {
		name, diagnostic string
		timing           bool
	}{
		{"progress", `{"d":2}`, true},
		{"omitted diagnostics", "", false},
		{"null diagnostics", "null", false},
		{"kind owns acquisition", `{"d":2,"acquisition":{"source":"retained"}}`, false},
		{"full object", `{"d":2,"padding":"` + strings.Repeat("x", 16*1024-30) + `"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubConnector{pages: []Page{
				{Checkpoint: json.RawMessage(`{"n":1}`), Reads: 40, Diagnostics: json.RawMessage(`{"d":1}`), More: true},
				{Checkpoint: json.RawMessage(`{"n":2}`), Reads: 2, Diagnostics: json.RawMessage(tc.diagnostic)},
			}}
			a, runs := stubAcquirer(t, stub, 100)
			if err := a.Run(context.Background(), "org_a", "connector_1", 1); err != nil {
				t.Fatal(err)
			}
			if len(runs.progress) != 2 {
				t.Fatalf("progress %+v", runs.progress)
			}
			var diagnostics map[string]json.RawMessage
			if tc.diagnostic == "" || tc.diagnostic == "null" {
				if string(runs.progress[1].Diagnostics) != tc.diagnostic {
					t.Fatal("omitted diagnostics must retain prior kind progress")
				}
				return
			}
			if err := json.Unmarshal(runs.progress[1].Diagnostics, &diagnostics); err != nil {
				t.Fatal(err)
			}
			if runs.progress[0].Reads != 40 || runs.progress[1].Reads != 2 || string(diagnostics["d"]) != "2" || len(runs.progress[1].Diagnostics) > 16<<10 {
				t.Fatalf("progress %+v", runs.progress)
			}
			if tc.timing {
				if diagnostics["acquisition"] == nil {
					t.Fatal("engine timing missing")
				}
			} else if string(runs.progress[1].Diagnostics) != tc.diagnostic {
				t.Fatal("kind diagnostics changed when timing would overwrite or exceed its bound")
			}
			if stub.requests[0].ReadsToday != 100 || stub.requests[1].ReadsToday != 140 || stub.requests[0].PageInRun != 0 || stub.requests[1].PageInRun != 1 {
				t.Fatalf("requests %+v", stub.requests)
			}
			if runs.finished[0] != nil {
				t.Fatalf("finish %+v", runs.finished)
			}
		})
	}
}

func TestAPageNoticeCompletesTheRunWithADiagnostic(t *testing.T) {
	stub := &stubConnector{pages: []Page{{Checkpoint: json.RawMessage(`{}`), Notice: "daily_read_cap_reached"}}}
	a, runs := stubAcquirer(t, stub, 0)
	if err := a.Run(context.Background(), "org_a", "connector_1", 1); err != nil {
		t.Fatal(err)
	}
	if f := runs.finished[0]; f == nil || f.Code != "daily_read_cap_reached" || f.Class != ClassSource || !f.Completed {
		t.Fatalf("finish %+v", f)
	}
}

// checkingConnector is a plugin-like kind: it checks credentials and owns an
// extension namespace.
type checkingConnector struct {
	stubConnector
	checkErr error
	checks   []CredentialRequest
	ctxs     []context.Context
}

func (c *checkingConnector) CheckCredential(_ context.Context, r CredentialRequest) error {
	c.checks = append(c.checks, r)
	return c.checkErr
}
func (c *checkingConnector) Fetch(ctx context.Context, r FetchRequest) (Page, error) {
	c.ctxs = append(c.ctxs, ctx)
	return c.stubConnector.Fetch(ctx, r)
}

func checkingAcquirer(t *testing.T, c *checkingConnector, deposited time.Time, lastSuccess *time.Time) (Acquirer, *fakeRuns) {
	t.Helper()
	a, runs := stubAcquirer(t, &c.stubConnector, 0)
	registry, err := NewRegistry(c)
	if err != nil {
		t.Fatal(err)
	}
	a.Registry = registry
	sealed, _ := a.Sealer.Seal("org_a", "connector_1", []byte(`{"token":"fixture-test-secret-check"}`))
	runs.target.Sealed = &sealed
	runs.target.Credential = &CredentialInfo{Version: 1, DepositedAt: deposited}
	runs.target.Health.LastSuccessAt = lastSuccess
	return a, runs
}

// A credential deposited after the last successful poll is checked before
// fetching; an access refusal ends the run as access_error without a fetch.
// A credential that already worked is not checked again.
func TestANewCredentialIsCheckedBeforeFetching(t *testing.T) {
	deposited := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	earlier, later := deposited.Add(-time.Hour), deposited.Add(time.Hour)
	for name, tc := range map[string]struct {
		lastSuccess *time.Time
		checkErr    error
		checked     bool
		fetched     bool
		class       ErrorClass
	}{
		"never succeeded":         {nil, nil, true, true, ""},
		"rotated after last poll": {&earlier, nil, true, true, ""},
		"worked since deposit":    {&later, AccessError("token_rejected"), false, true, ""},
		"refused by the source":   {nil, AccessError("token_rejected"), true, false, ClassAccess},
		"plugin unreachable":      {nil, TransientError("plugin_unavailable"), true, false, ClassTransient},
	} {
		t.Run(name, func(t *testing.T) {
			c := &checkingConnector{stubConnector: stubConnector{pages: []Page{{Checkpoint: json.RawMessage(`{}`)}}}, checkErr: tc.checkErr}
			a, runs := checkingAcquirer(t, c, deposited, tc.lastSuccess)
			if err := a.Run(context.Background(), "org_a", "connector_1", 1); err != nil {
				t.Fatal(err)
			}
			if got := len(c.checks) == 1; got != tc.checked {
				t.Fatalf("checked %v, want %v", got, tc.checked)
			}
			if tc.checked && (string(c.checks[0].Credential) != `{"token":"fixture-test-secret-check"}` || c.checks[0].InstanceID != "connector_1" || c.checks[0].Organization != "org_a") {
				t.Fatalf("check request %+v", c.checks[0])
			}
			if got := len(c.requests) > 0; got != tc.fetched {
				t.Fatalf("fetched %v, want %v", got, tc.fetched)
			}
			f := runs.finished[0]
			if tc.class == "" && f != nil || tc.class != "" && (f == nil || f.Class != tc.class) {
				t.Fatalf("finish %+v, want class %q", f, tc.class)
			}
		})
	}
}

// A plugin kind fetches on behalf of its plugin, so its items may write the
// namespaces that plugin owns.
func TestAPluginKindWritesItsOwnExtensionNamespaces(t *testing.T) {
	c := &checkingConnector{stubConnector: stubConnector{pages: []Page{{Checkpoint: json.RawMessage(`{}`)}}}}
	later := time.Now()
	a, _ := checkingAcquirer(t, c, later.Add(-time.Hour), &later)
	if err := a.Run(context.Background(), "org_a", "connector_1", 1); err != nil {
		t.Fatal(err)
	}
	registry := content.NewExtensionRegistry()
	if err := registry.Own("acme.source", "acme.source"); err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate(c.ctxs[0], content.Extensions{"acme.source": {SchemaVersion: "1"}}); err != nil {
		t.Fatalf("the fetch context does not carry the owning plugin: %v", err)
	}
	if c.requests[0].Organization != "org_a" || c.requests[0].InstanceID != "connector_1" {
		t.Fatalf("fetch request %+v", c.requests[0])
	}
}

func (c skippingConnector) Descriptor() Descriptor {
	d := c.Fixture.Descriptor()
	d.Kind = "skipping"
	return d
}

func (c *stubConnector) Descriptor() Descriptor {
	d := Descriptor{}
	d.Kind = "stub"
	d.ConfigSchema = []byte(`{"type":"object"}`)
	d.CredentialSchema = nil
	d.DefaultInterval = time.Minute
	return d
}

func (c *checkingConnector) Descriptor() Descriptor {
	d := c.stubConnector.Descriptor()
	d.CredentialSchema = []byte(`{"type":"object"}`)
	d.ExtensionOwner = "acme.source"
	d.Credentials = c
	return d
}

// These calls block at the ingestion boundary, so the test can observe the
// Acquirer's concurrency without relying on machine speed or wall-clock waits.
type heldSubmission struct {
	command content.Command
	release chan struct{}
}

type heldIngest struct {
	active  atomic.Int32
	maximum atomic.Int32
	calls   chan heldSubmission
	failKey string
	failure error
}

func (f *heldIngest) TrustedAccept(ctx context.Context, _, _ string, command content.Command) (content.Receipt, error) {
	active := f.active.Add(1)
	defer f.active.Add(-1)
	for previous := f.maximum.Load(); active > previous; previous = f.maximum.Load() {
		if f.maximum.CompareAndSwap(previous, active) {
			break
		}
	}
	call := heldSubmission{command: command, release: make(chan struct{})}
	select {
	case f.calls <- call:
	case <-ctx.Done():
		return content.Receipt{}, ctx.Err()
	}
	select {
	case <-call.release:
	case <-ctx.Done():
		return content.Receipt{}, ctx.Err()
	}
	if command.Source.RecordKey == f.failKey {
		return content.Receipt{}, f.failure
	}
	return content.Receipt{NewRevision: true}, nil
}

func (*heldIngest) TrustedWithdraw(context.Context, string, string, content.Withdrawal) (content.Receipt, error) {
	panic("the test page has no withdrawals")
}

type observedRuns struct {
	*fakeRuns
	ingest    *heldIngest
	premature bool
}

func (f *observedRuns) CommitCheckpoint(ctx context.Context, org, id string, run int64, progress Progress) (bool, error) {
	f.premature = f.premature || f.ingest.active.Load() != 0
	return f.fakeRuns.CommitCheckpoint(ctx, org, id, run, progress)
}

func (f *observedRuns) FinishRun(ctx context.Context, org, id string, run int64, failure *RunError) error {
	f.premature = f.premature || f.ingest.active.Load() != 0
	return f.fakeRuns.FinishRun(ctx, org, id, run, failure)
}

func TestPageSubmissionConcurrencyAndFailureResume(t *testing.T) {
	for _, tc := range []struct {
		name               string
		hint, simultaneous int
		duplicate          bool
		failure            error
	}{
		{name: "default serial", simultaneous: 1},
		{name: "explicit serial", hint: 1, simultaneous: 1},
		{name: "parallel", hint: 2, simultaneous: 2},
		{name: "engine cap", hint: 100, simultaneous: 32},
		{name: "repeated key remains ordered", hint: 2, simultaneous: 1, duplicate: true},
		{name: "transient page replay", hint: 2, simultaneous: 2, failure: errors.New("ingestion unavailable")},
		{name: "permanent item rejected", hint: 2, simultaneous: 2, failure: content.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			items := make([]Item, 36)
			for index := range items {
				items[index] = Item{RecordKey: fmt.Sprintf("record-%d", index), Revision: fmt.Sprintf("revision-%d", index), Content: content.Text{Kind: "text", Text: "body"}}
			}
			if tc.duplicate {
				items[1].RecordKey = items[0].RecordKey
			}
			page := Page{Items: items, Checkpoint: json.RawMessage(`{"member":36}`), SubmissionConcurrency: tc.hint}
			stub := &stubConnector{pages: []Page{page}}
			a, runs := stubAcquirer(t, stub, 0)
			ingest := &heldIngest{calls: make(chan heldSubmission, len(items)), failKey: items[0].RecordKey, failure: tc.failure}
			a.Ingest = ingest
			observed := &observedRuns{fakeRuns: runs, ingest: ingest}
			a.Store = observed
			done := make(chan error, 1)
			go func() { done <- a.Run(ctx, "org_a", "connector_1", 1) }()
			first := make([]heldSubmission, tc.simultaneous)
			for index := range first {
				select {
				case first[index] = <-ingest.calls:
				case <-ctx.Done():
					t.Fatalf("expected %d simultaneous submissions, got %d", tc.simultaneous, index)
				}
			}
			select {
			case call := <-ingest.calls:
				t.Fatalf("started %q beyond concurrency limit %d", call.command.Source.RecordKey, tc.simultaneous)
			case err := <-done:
				t.Fatalf("run finished before submissions completed: %v", err)
			default:
			}
			before := map[string]string{}
			for _, call := range first {
				before[call.command.Source.RecordKey] = call.command.Key
				close(call.release)
			}
			count := len(first)
			await := func() {
				for {
					select {
					case call := <-ingest.calls:
						before[call.command.Source.RecordKey] = call.command.Key
						count++
						close(call.release)
					case err := <-done:
						if err != nil {
							t.Fatal(err)
						}
						return
					case <-ctx.Done():
						t.Fatal("acquisition did not complete after releasing ingestion")
					}
				}
			}
			await()
			if observed.premature || int(ingest.maximum.Load()) != tc.simultaneous {
				t.Fatalf("maximum concurrent submissions=%d, want %d; checkpoint/finish with inflight work=%v", ingest.maximum.Load(), tc.simultaneous, observed.premature)
			}
			if tc.failure != nil && !errors.Is(tc.failure, content.ErrInvalid) {
				if len(runs.checkpoints) != 0 || len(runs.finished) != 1 || runs.finished[0] == nil || runs.finished[0].Code != "ingestion_unavailable" {
					t.Fatalf("failed page advanced or did not report failure: checkpoints=%v finished=%v", runs.checkpoints, runs.finished)
				}
				// A restart fetches the same checkpoint; every resubmitted item
				// must keep the original ingestion idempotency key.
				ingest.failure = nil
				stub.pages = []Page{page}
				stub.requests = nil
				replayed := 0
				go func() { done <- a.Run(ctx, "org_a", "connector_1", 1) }()
				for {
					select {
					case call := <-ingest.calls:
						replayed++
						if key, exists := before[call.command.Source.RecordKey]; exists && call.command.Key != key {
							t.Fatalf("replay changed key for %q: %q to %q", call.command.Source.RecordKey, key, call.command.Key)
						}
						close(call.release)
					case err := <-done:
						if err != nil {
							t.Fatal(err)
						}
						if len(runs.checkpoints) != 1 || replayed != len(items) {
							t.Fatalf("replayed %d/%d items; checkpoints %v", replayed, len(items), runs.checkpoints)
						}
						return
					case <-ctx.Done():
						t.Fatal("replay did not complete")
					}
				}
			}
			if count != len(items) || len(runs.checkpoints) != 1 || !runs.items[0] {
				t.Fatalf("submitted=%d checkpoints=%v activity=%v", count, runs.checkpoints, runs.items)
			}
			if tc.failure != nil && (runs.finished[0] == nil || runs.finished[0].Code != "item_rejected") {
				t.Fatalf("permanent item failure did not report rejection: %v", runs.finished)
			}
		})
	}
}

// Attachment-only content is raw input for normalization, not an already
// normalized Manifest. The ingestion command is the owning boundary for this
// routing decision; explicit text+attachment Manifests are covered in
// TestAttachmentBytesReachStorageOnlyThroughVerifiedGrants.
func TestAttachmentOnlyInputSubmitsAVerifiedSourceBlob(t *testing.T) {
	const sourceBytes = "<document>Hello</document>"
	for _, tc := range []struct {
		name         string
		change       func(*Item)
		reject, skip bool
	}{
		{name: "source blob"},
		{name: "multiple sources", reject: true, change: func(item *Item) {
			at := item.Attachments[0]
			at.Key = "other"
			item.Attachments = append(item.Attachments, at)
		}},
		{name: "source key is canonical", reject: true, change: func(item *Item) { item.Attachments[0].Key = "attachment" }},
		{name: "source role is canonical", reject: true, change: func(item *Item) { item.Attachments[0].Role = "attachment" }},
		{name: "relations cannot be lost", reject: true, change: func(item *Item) {
			item.Manifest.Relations = []content.Relation{{Type: "derived_from", Target: content.Source{RecordKey: "related"}}}
		}},
		{name: "parent cannot be lost", reject: true, change: func(item *Item) {
			item.Attachments[0].ParentKey = "parent"
		}},
		{name: "part extensions cannot be lost", reject: true, change: func(item *Item) {
			item.Attachments[0].Extensions = content.Extensions{"record.fixture": {SchemaVersion: "1", Data: map[string]any{"label": "source"}}}
		}},
		{name: "skipped source cannot publish empty manifest", skip: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			size := int64(len(sourceBytes))
			item := Item{RecordKey: "document.xml", Revision: "revision-1", Position: "3",
				Manifest:    &content.Manifest{Kind: "manifest"},
				Extensions:  content.Extensions{"record.fixture": {SchemaVersion: "1", Data: map[string]any{"label": "document"}}},
				Attachments: []Attachment{{Key: "source", Role: "source", MediaType: "application/xml", Ref: "raw:member", SizeBytes: &size, SHA256: content.Hash([]byte(sourceBytes))}}}
			if tc.change != nil {
				tc.change(&item)
			}
			a, runs, ingest, src := exchangingAcquirer(t, func() []Item { return []Item{item} })
			src.bytes["raw:member"] = sourceBytes
			if tc.skip {
				src.skip = &AttachmentDescription{Skip: "source_not_available"}
				item.Attachments[0].SHA256 = ""
			}
			if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
				t.Fatal(err)
			}
			if tc.reject || tc.skip {
				if len(ingest.accepted) != 0 || len(runs.checkpoints) != 1 || runs.finished[0] == nil || runs.finished[0].Code != "item_rejected" {
					t.Fatalf("raw input was accepted or did not report rejection: commands=%v checkpoints=%v finished=%v", ingest.accepted, runs.checkpoints, runs.finished)
				}
				if src.uploads != 0 || len(src.storage.sessions) != 0 || len(src.storage.blobs) != 0 {
					t.Fatalf("rejected input spent uploads: uploads=%d grants=%d verified blobs=%d", src.uploads, len(src.storage.sessions), len(src.storage.blobs))
				}
				return
			}
			if len(ingest.accepted) != 1 || len(runs.checkpoints) != 1 || runs.finished[0] != nil {
				t.Fatalf("accepted=%v checkpoints=%v finished=%v", ingest.accepted, runs.checkpoints, runs.finished)
			}
			command := ingest.accepted[0]
			if command.Content.Kind != "blob" || command.Content.MediaType != "application/xml" || command.Manifest != nil || src.storage.blobs[command.Content.BlobID] != sourceBytes {
				t.Fatalf("normalizer did not receive the verified source Blob: command=%+v verified bytes=%v", command, src.storage.blobs)
			}
			if command.Source != (content.Source{CorpusID: "corpus_1", Namespace: "wire", RecordKey: "document.xml"}) || command.Revision != "revision-1" || command.Position != "3" || !IsConnectorKey(command.Key) {
				t.Fatalf("source identity lost: %+v", command)
			}
			if command.Provenance["producer"] != "connector_1" || command.Provenance["producer_version"] != "fixture/v1" || command.Extensions["record.fixture"].Data["label"] != "document" {
				t.Fatalf("producer or record extensions lost: provenance=%v extensions=%v", command.Provenance, command.Extensions)
			}
		})
	}
}
