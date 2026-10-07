package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
)

// exchangingConnector returns the same scripted items on every fetch and
// plays the source side of the attachment exchange: it describes the bytes
// it holds and "uploads" them to the fake storage behind a grant.
type exchangingConnector struct {
	items func() []Item
	src   *fakeSource
	more  bool
}

type fakeSource struct {
	bytes      map[string]string // ref -> bytes
	changed    map[string]int    // ref -> uploads that send other bytes
	describes  int
	uploads    int
	err        error
	skip       *AttachmentDescription
	storage    *fakeGrants
	onDescribe func()
}

func (c exchangingConnector) Fetch(context.Context, FetchRequest) (Page, error) {
	return Page{Items: c.items(), Checkpoint: json.RawMessage(`{}`), More: c.more}, nil
}

func (c exchangingConnector) DescribeAttachment(_ context.Context, r AttachmentRequest) (AttachmentDescription, error) {
	c.src.describes++
	if c.src.onDescribe != nil {
		c.src.onDescribe()
	}
	if c.src.err != nil {
		return AttachmentDescription{}, c.src.err
	}
	if c.src.skip != nil {
		return *c.src.skip, nil
	}
	b := c.src.bytes[r.Attachment.Ref]
	return AttachmentDescription{SizeBytes: int64(len(b)), SHA256: content.Hash([]byte(b))}, nil
}

type archivePollingRuns struct {
	*fakeRuns
	archived bool
}

func (s *archivePollingRuns) BeginPoll(context.Context, string, string, int64) (func(), bool, error) {
	return func() {}, !s.archived, nil
}

// The source exchange owner covers an archive after a page was fetched or
// after its bytes were described; neither may initiate a later source call.
func TestArchivedPageStopsAttachmentSourceExchanges(t *testing.T) {
	for _, after := range []string{"fetch", "describe"} {
		t.Run(after, func(t *testing.T) {
			var held *archivePollingRuns
			item := mailItem("archived")
			item.Attachments = item.Attachments[1:] // requires describe before upload
			a, runs, ingest, src := exchangingAcquirer(t, func() []Item {
				if after == "fetch" {
					held.archived = true
				}
				return []Item{item}
			})
			held = &archivePollingRuns{fakeRuns: runs}
			a.Store = held
			src.onDescribe = func() { held.archived = true }
			if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
				t.Fatal(err)
			}
			wantDescribes := 0
			if after == "describe" {
				wantDescribes = 1
			}
			if src.describes != wantDescribes || src.uploads != 0 || len(ingest.accepted) != 0 || len(runs.checkpoints) != 0 {
				t.Fatalf("archived source exchange continued: describes=%d uploads=%d accepted=%d checkpoints=%d", src.describes, src.uploads, len(ingest.accepted), len(runs.checkpoints))
			}
		})
	}
}

func (c exchangingConnector) UploadAttachment(_ context.Context, r AttachmentRequest, g UploadGrant) error {
	c.src.uploads++
	b := c.src.bytes[r.Attachment.Ref]
	if c.src.changed[r.Attachment.Ref] > 0 {
		c.src.changed[r.Attachment.Ref]--
		b += " (edited)"
	}
	if content.Hash([]byte(b)) != g.SHA256 {
		return SourceError(CodeAttachmentChanged)
	}
	c.src.storage.put(g.URL, b)
	return nil
}

// fakeGrants plays uploads.Service: one session per grant key, a PUT only
// through the grant URL, Confirm that reads back what was stored.
type fakeGrants struct {
	sessions map[string]uploads.Session
	stored   map[string]string // grant URL -> bytes
	blobs    map[string]string // blob id -> bytes
	expected map[string]string // session id -> sha256
	tamper   bool
	fail     error
}

func newFakeGrants() *fakeGrants {
	return &fakeGrants{sessions: map[string]uploads.Session{}, stored: map[string]string{}, blobs: map[string]string{}, expected: map[string]string{}}
}

func (g *fakeGrants) put(url, b string) { g.stored[url] = b }

func (g *fakeGrants) Grant(_ context.Context, org string, req uploads.Request) (uploads.Session, error) {
	if g.fail != nil {
		return uploads.Session{}, g.fail
	}
	id := content.StableID("blob", org, req.SHA256, req.MediaType)
	if _, ok := g.blobs[id]; ok {
		return uploads.Session{State: "verified", BlobID: id}, nil
	}
	s := uploads.Session{ID: "upload-" + req.Key, State: "awaiting_upload", UploadURL: "https://storage.invalid/" + req.Key, SHA256: req.SHA256, MediaType: req.MediaType}
	g.sessions[s.ID] = s
	return s, nil
}

func (g *fakeGrants) Confirm(_ context.Context, org, id string) (uploads.Session, error) {
	s := g.sessions[id]
	b, ok := g.stored[s.UploadURL]
	if g.tamper {
		b += "tampered"
	}
	switch {
	case !ok:
		s.State, s.ErrorCode = "awaiting_upload", "verification_unavailable"
	case content.Hash([]byte(b)) != s.SHA256:
		s.State, s.ErrorCode = "rejected", "verification_failed"
	default:
		s.State, s.BlobID = "verified", content.StableID("blob", org, s.SHA256, s.MediaType)
		g.blobs[s.BlobID] = b
	}
	return s, nil
}

type fakeReceipts struct{ keys map[string]bool }

func (f fakeReceipts) HasReceipt(_ context.Context, _, key string) (bool, error) {
	return f.keys[key], nil
}

const bodyBytes = "<p>Hello</p>"

func mailItem(revision string) Item {
	size := int64(len(bodyBytes))
	return Item{RecordKey: "<m1@example.org>", Revision: revision,
		Manifest:   &content.Manifest{Kind: "manifest", Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: "Hello"}}}},
		Extensions: content.Extensions{"connector.fixture": {SchemaVersion: "1", Data: map[string]any{"skipped": []any{}}}},
		Attachments: []Attachment{
			// The body's bytes are already known: no description is asked for.
			{Key: "original_body", Role: "original_body", MediaType: "text/html", Ref: "body:m1", SizeBytes: &size, SHA256: content.Hash([]byte(bodyBytes))},
			{Key: "attachment-1", Role: "attachment", MediaType: "application/pdf", Ref: "att:m1/a1"},
		}}
}

func exchangingAcquirer(t *testing.T, items func() []Item) (Acquirer, *fakeRuns, *fakeIngest, *fakeSource) {
	t.Helper()
	a, runs, ingest := newAcquirer(t, `{}`, "")
	grants := newFakeGrants()
	src := &fakeSource{bytes: map[string]string{"body:m1": bodyBytes, "att:m1/a1": "%PDF-1.7 bytes"}, changed: map[string]int{}, storage: grants}
	registry, err := NewRegistry(exchangingConnector{items: items, src: src})
	if err != nil {
		t.Fatal(err)
	}
	a.Registry, a.Blobs = registry, grants
	return a, runs, ingest, src
}

func TestAttachmentBytesReachStorageOnlyThroughVerifiedGrants(t *testing.T) {
	a, runs, ingest, src := exchangingAcquirer(t, func() []Item { return []Item{mailItem("sha256:r1")} })
	if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
		t.Fatal(err)
	}
	if len(ingest.accepted) != 1 || runs.finished[0] != nil {
		t.Fatalf("accepted %d finished %+v", len(ingest.accepted), runs.finished)
	}
	c := ingest.accepted[0]
	if c.Revision != "sha256:r1" || len(c.Manifest.Parts) != 3 {
		t.Fatalf("command %+v", c)
	}
	for i, want := range []string{bodyBytes, "%PDF-1.7 bytes"} {
		p := c.Manifest.Parts[i+1]
		if p.Content.Kind != "blob" || src.storage.blobs[p.Content.BlobID] != want || p.Content.MediaType == "" {
			t.Fatalf("part %+v is not the verified Blob of %q", p, want)
		}
	}
	if src.describes != 1 || src.uploads != 2 {
		t.Fatalf("described %d uploaded %d; a descriptor with its exact bytes is not described", src.describes, src.uploads)
	}
}

func TestStoredBytesThatFailVerificationAreNeverAccepted(t *testing.T) {
	a, runs, ingest, src := exchangingAcquirer(t, func() []Item { return []Item{mailItem("sha256:r1")} })
	src.storage.tamper = true
	if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
		t.Fatal(err)
	}
	got := runs.finished[0]
	if got == nil || got.Class != ClassSource || got.Code != "attachment_unverified" || len(ingest.accepted) != 0 || len(runs.checkpoints) != 0 || len(src.storage.blobs) != 0 {
		t.Fatalf("finished %+v accepted %d checkpoints %v blobs %d", got, len(ingest.accepted), runs.checkpoints, len(src.storage.blobs))
	}
}

func TestAnAlreadyAcceptedRevisionIsSkippedBeforeAnyTransfer(t *testing.T) {
	a, _, ingest, src := exchangingAcquirer(t, func() []Item { return []Item{mailItem("sha256:r1")} })
	key := KeyPrefix + content.StableID("item", "connector_1", "<m1@example.org>", "sha256:r1")
	a.Receipts = fakeReceipts{keys: map[string]bool{key: true}}
	if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
		t.Fatal(err)
	}
	if src.describes != 0 || src.uploads != 0 || len(ingest.accepted) != 0 {
		t.Fatalf("described %d uploaded %d accepted %d; an accepted revision must not be read again", src.describes, src.uploads, len(ingest.accepted))
	}
	// A changed revision of the same Record is still read and submitted.
	items := func() []Item { return []Item{mailItem("sha256:r2")} }
	a, _, ingest, src = exchangingAcquirer(t, items)
	a.Receipts = fakeReceipts{keys: map[string]bool{key: true}}
	if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
		t.Fatal(err)
	}
	if src.uploads != 2 || len(ingest.accepted) != 1 || ingest.accepted[0].Revision != "sha256:r2" {
		t.Fatalf("uploaded %d accepted %+v; a changed message must become a correction", src.uploads, ingest.accepted)
	}
}

func TestBytesAlreadyStoredAsABlobAreNotUploadedAgain(t *testing.T) {
	a, _, ingest, src := exchangingAcquirer(t, func() []Item { return []Item{mailItem("sha256:r1")} })
	for ref, b := range src.bytes {
		src.storage.blobs[content.StableID("blob", "org_a", content.Hash([]byte(b)), map[string]string{"body:m1": "text/html", "att:m1/a1": "application/pdf"}[ref])] = b
	}
	if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
		t.Fatal(err)
	}
	if src.uploads != 0 || len(ingest.accepted) != 1 || len(ingest.accepted[0].Manifest.Parts) != 3 {
		t.Fatalf("uploaded %d accepted %+v", src.uploads, ingest.accepted)
	}
}

func TestBytesThatChangeWhileUploadingAreDescribedAgainOnce(t *testing.T) {
	a, runs, ingest, src := exchangingAcquirer(t, func() []Item { return []Item{mailItem("sha256:r1")} })
	src.changed["att:m1/a1"] = 1
	if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
		t.Fatal(err)
	}
	if len(ingest.accepted) != 1 || runs.finished[0] != nil || src.describes != 2 {
		t.Fatalf("accepted %d finished %+v described %d", len(ingest.accepted), runs.finished, src.describes)
	}
	// Bytes that keep changing reject only their item; the source moves on.
	a, runs, ingest, src = exchangingAcquirer(t, func() []Item { return []Item{mailItem("sha256:r1")} })
	src.changed["att:m1/a1"] = 2
	if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
		t.Fatal(err)
	}
	if len(ingest.accepted) != 0 || runs.finished[0] == nil || runs.finished[0].Code != "item_rejected" || !runs.finished[0].Completed || len(runs.checkpoints) != 1 {
		t.Fatalf("finished %+v checkpoints %v", runs.finished, runs.checkpoints)
	}
}

func TestASkippedAttachmentIsLeftOutAndRecordedInTheItemExtensions(t *testing.T) {
	a, runs, ingest, src := exchangingAcquirer(t, func() []Item { return []Item{mailItem("sha256:r1")} })
	recorded := content.Extensions{"connector.fixture": {SchemaVersion: "1", Data: map[string]any{"skipped": []any{"attachment-1"}}}}
	src.skip = &AttachmentDescription{Skip: "too_large", ItemExtensions: recorded}
	if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
		t.Fatal(err)
	}
	if len(ingest.accepted) != 1 || runs.finished[0] != nil {
		t.Fatalf("accepted %d finished %+v", len(ingest.accepted), runs.finished)
	}
	c := ingest.accepted[0]
	for _, p := range c.Manifest.Parts {
		if p.Key == "attachment-1" {
			t.Fatal("a skipped attachment must not become a Part")
		}
	}
	if skipped := c.Extensions["connector.fixture"].Data["skipped"].([]any); len(skipped) != 1 {
		t.Fatalf("extensions %+v", c.Extensions)
	}
}

func TestAttachmentFailuresEndTheRunWithTheirClass(t *testing.T) {
	for name, tc := range map[string]struct {
		source, grant error
		class         ErrorClass
		code          string
	}{
		"source refuses": {source: AccessError("mailbox_access_denied"), class: ClassAccess, code: "mailbox_access_denied"},
		"storage down":   {grant: errors.New("database down"), class: ClassTransient, code: "ingestion_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			a, runs, ingest, src := exchangingAcquirer(t, func() []Item { return []Item{mailItem("sha256:r1")} })
			src.err, src.storage.fail = tc.source, tc.grant
			if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
				t.Fatal(err)
			}
			got := runs.finished[0]
			if got == nil || got.Class != tc.class || got.Code != tc.code || len(ingest.accepted) != 0 || len(runs.checkpoints) != 0 {
				t.Fatalf("finished %+v accepted %d checkpoints %v", got, len(ingest.accepted), runs.checkpoints)
			}
		})
	}
}

func TestAttachmentsNeedAnExchangingConnector(t *testing.T) {
	a, runs, ingest := newAcquirer(t, `{}`, "")
	registry, _ := NewRegistry(plainConnector{items: []Item{mailItem("sha256:r1")}})
	a.Registry, a.Blobs = registry, newFakeGrants()
	if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
		t.Fatal(err)
	}
	if len(ingest.accepted) != 0 || runs.finished[0] == nil || runs.finished[0].Code != "item_rejected" {
		t.Fatalf("finished %+v", runs.finished)
	}
}

// A run keeps starting pages only until its soft time limit; the page that
// crosses it is committed and the next run continues.
type slowCheckpointRuns struct {
	*fakeRuns
	advance func()
}

func (s slowCheckpointRuns) CommitCheckpoint(ctx context.Context, org, id string, run int64, progress Progress) (bool, error) {
	s.advance()
	return s.fakeRuns.CommitCheckpoint(ctx, org, id, run, progress)
}
func TestARunStopsStartingPagesAfterItsSoftLimit(t *testing.T) {
	for _, stage := range []string{"fetch", "checkpoint"} {
		t.Run(stage, func(t *testing.T) {
			a, runs, _, _ := exchangingAcquirer(t, func() []Item { return nil })
			clock := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
			advance := func() { clock = clock.Add(150 * time.Second) }
			registry, _ := NewRegistry(exchangingConnector{items: func() []Item {
				if stage == "fetch" {
					advance()
				}
				return nil
			}, src: &fakeSource{}, more: true})
			a.Registry, a.MaxPages = registry, 10
			a.Now = func() time.Time { return clock }
			if stage == "checkpoint" {
				a.Store = slowCheckpointRuns{runs, advance}
			}
			if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
				t.Fatal(err)
			}
			if len(runs.checkpoints) != 1 || runs.finished[0] != nil {
				t.Fatalf("%d pages committed, finished %+v; no next page may start after fetch or checkpoint crosses the soft limit", len(runs.checkpoints), runs.finished)
			}
		})
	}
}

type plainConnector struct{ items []Item }

func (c plainConnector) Fetch(context.Context, FetchRequest) (Page, error) {
	return Page{Items: c.items, Checkpoint: json.RawMessage(`{}`)}, nil
}

func (c exchangingConnector) Descriptor() Descriptor {
	d := Descriptor{}
	d.Kind = "fixture"
	d.ConfigSchema = []byte(`{"type":"object"}`)
	d.CredentialSchema = nil
	d.DefaultInterval = time.Minute
	d.MaxAttachmentBytes = 1 << 20
	d.Attachments = c
	return d
}

func (c plainConnector) Descriptor() Descriptor {
	d := Descriptor{}
	d.Kind = "fixture"
	d.ConfigSchema = []byte(`{"type":"object"}`)
	d.CredentialSchema = nil
	d.DefaultInterval = time.Minute
	return d
}

// Owns stage attribution at the acquisition boundary. The clock advances only
// when a boundary is crossed; a missing/misattributed stage loses literal time.
type timedExchange struct {
	exchangingConnector
	advance func(time.Duration)
}

func (c timedExchange) Fetch(ctx context.Context, req FetchRequest) (Page, error) {
	c.advance(2 * time.Millisecond)
	p, err := c.exchangingConnector.Fetch(ctx, req)
	p.Diagnostics = json.RawMessage(`{"members_done":1}`)
	return p, err
}
func (c timedExchange) Descriptor() Descriptor {
	d := c.exchangingConnector.Descriptor()
	d.Attachments = c
	return d
}
func (c timedExchange) UploadAttachment(ctx context.Context, req AttachmentRequest, grant UploadGrant) error {
	c.advance(5 * time.Millisecond)
	return c.exchangingConnector.UploadAttachment(ctx, req, grant)
}

type timedGrants struct {
	*fakeGrants
	advance func(time.Duration)
}

func (g timedGrants) Grant(ctx context.Context, org string, req uploads.Request) (uploads.Session, error) {
	g.advance(3 * time.Millisecond)
	return g.fakeGrants.Grant(ctx, org, req)
}
func (g timedGrants) Confirm(ctx context.Context, org, id string) (uploads.Session, error) {
	g.advance(7 * time.Millisecond)
	return g.fakeGrants.Confirm(ctx, org, id)
}

type timedIngest struct {
	*fakeIngest
	advance func(time.Duration)
}

func (f timedIngest) TrustedAccept(ctx context.Context, org, corpus string, command content.Command) (content.Receipt, error) {
	f.advance(11 * time.Millisecond)
	return f.fakeIngest.TrustedAccept(ctx, org, corpus, command)
}

type timedReceipts struct {
	advance func(time.Duration)
}

func (f timedReceipts) HasReceipt(context.Context, string, string) (bool, error) {
	f.advance(17 * time.Millisecond)
	return false, nil
}

func TestPageDiagnosticsAttributeAcquisitionStages(t *testing.T) {
	item := mailItem("r1")
	a, runs, ingest, src := exchangingAcquirer(t, func() []Item { return []Item{item} })
	clock := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	advance := func(d time.Duration) { clock = clock.Add(d) }
	connector := timedExchange{exchangingConnector: exchangingConnector{items: func() []Item { return []Item{item} }, src: src}, advance: advance}
	registry, err := NewRegistry(connector)
	if err != nil {
		t.Fatal(err)
	}
	a.Registry, a.Now = registry, func() time.Time { return clock }
	a.Blobs = timedGrants{src.storage, advance}
	a.Ingest = timedIngest{ingest, advance}
	a.Receipts = timedReceipts{advance}
	src.onDescribe = func() { advance(13 * time.Millisecond) }
	runs.target.Interval = 30 * time.Second
	if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
		t.Fatal(err)
	}
	if len(runs.progress) != 1 {
		t.Fatalf("progress=%+v", runs.progress)
	}
	var diagnostics struct {
		MembersDone int `json:"members_done"`
		Acquisition struct {
			Items      int     `json:"items"`
			FetchMS    float64 `json:"fetch_ms"`
			UploadMS   float64 `json:"upload_ms"`
			GrantMS    float64 `json:"grant_ms"`
			VerifyMS   float64 `json:"verify_ms"`
			AcceptMS   float64 `json:"accept_ms"`
			ReceiptMS  float64 `json:"receipt_ms"`
			DescribeMS float64 `json:"describe_ms"`
			PageMS     float64 `json:"page_ms"`
			RunMS      float64 `json:"run_ms"`
			More       bool    `json:"more"`
			StopReason string  `json:"stop_reason"`
			IntervalMS float64 `json:"interval_ms"`
		} `json:"acquisition"`
	}
	if err := json.Unmarshal(runs.progress[0].Diagnostics, &diagnostics); err != nil {
		t.Fatal(err)
	}
	d := diagnostics.Acquisition
	if diagnostics.MembersDone != 1 || d.Items != 1 || d.FetchMS != 2 || d.GrantMS != 6 || d.UploadMS != 10 || d.VerifyMS != 14 || d.AcceptMS != 11 || d.ReceiptMS != 17 || d.DescribeMS != 13 || d.PageMS != 73 || d.RunMS != 73 || d.More || d.StopReason != "source_drained" || d.IntervalMS != 30000 {
		t.Fatalf("stage timing or source progress lost: %s", runs.progress[0].Diagnostics)
	}
	if len(runs.progress[0].Diagnostics) > 16<<10 || strings.Contains(string(runs.progress[0].Diagnostics), "storage.invalid") {
		t.Fatal("diagnostics exceeded bound or exposed a transfer URL")
	}
}

// Continuation is a scheduling decision after committed progress; the store
// fake records that decision, while PostgreSQL owns actual lease claimability.
type continuingRuns struct {
	*fakeRuns
	continued       int
	afterCheckpoint func()
}

func (f *continuingRuns) CommitCheckpoint(ctx context.Context, org, id string, run int64, p Progress) (bool, error) {
	ok, err := f.fakeRuns.CommitCheckpoint(ctx, org, id, run, p)
	if ok && f.afterCheckpoint != nil {
		f.afterCheckpoint()
	}
	return ok, err
}

func (f *continuingRuns) ContinueRun(context.Context, string, string, int64) error {
	f.continued++
	return nil
}

type pagedExchange struct {
	exchangingConnector
	page    Page
	advance func()
}

func (c pagedExchange) Fetch(context.Context, FetchRequest) (Page, error) {
	if c.advance != nil {
		c.advance()
	}
	return c.page, nil
}

func TestBoundedRunsContinueOnlyAfterCommittedProgress(t *testing.T) {
	for _, tc := range []struct {
		name      string
		bound     string
		empty     bool
		unchanged bool
		drained   bool
		notice    string
		replay    bool
		legacy    bool
		want      bool
	}{
		{name: "page bound", bound: "page", want: true},
		{name: "time bound", bound: "time", want: true},
		{name: "time bound crossed by checkpoint", bound: "checkpoint", want: true},
		{name: "byte bound", bound: "byte", want: true},
		{name: "receipt replay advances cursor", bound: "page", replay: true, want: true},
		{name: "source drained", bound: "page", drained: true},
		{name: "empty pages", bound: "page", empty: true},
		{name: "unchanged checkpoint formatting", bound: "page", unchanged: true},
		{name: "notice", bound: "page", notice: "source_notice"},
		{name: "store without continuation", bound: "page", legacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := mailItem("r1")
			a, runs, ingest, src := exchangingAcquirer(t, func() []Item { return []Item{item} })
			runs.target.Checkpoint = json.RawMessage(`{"step":0,"large":9007199254740993}`)
			page := Page{Items: []Item{item}, Checkpoint: json.RawMessage(`{"step":1,"large":9007199254740993}`), More: !tc.drained, Notice: tc.notice, Diagnostics: json.RawMessage(`{"members_done":1}`)}
			if tc.unchanged {
				page.Checkpoint = json.RawMessage(`{ "large": 9007199254740993, "step": 0 }`)
			}
			if tc.empty {
				page.Items = nil
			}
			clock := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
			connector := pagedExchange{exchangingConnector: exchangingConnector{src: src}, page: page}
			a.MaxPages = 1
			if tc.bound == "time" {
				a.MaxPages = 10
				connector.advance = func() { clock = clock.Add(150 * time.Second) }
			}
			if tc.bound == "byte" {
				a.MaxPages, a.AttachmentBudget = 10, 1
			}
			if tc.replay {
				key := KeyPrefix + content.StableID("item", runs.target.ID, item.RecordKey, item.Revision)
				a.Receipts = fakeReceipts{keys: map[string]bool{key: true}}
			}
			registry, err := NewRegistry(connector)
			if err != nil {
				t.Fatal(err)
			}
			a.Registry, a.Now = registry, func() time.Time { return clock }
			observed := &continuingRuns{fakeRuns: runs}
			if tc.bound == "checkpoint" {
				a.MaxPages = 10
				observed.afterCheckpoint = func() { clock = clock.Add(150 * time.Second) }
			}
			if !tc.legacy {
				a.Store = observed
			}
			if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
				t.Fatal(err)
			}
			wantContinue, wantFinish := 0, 1
			if tc.want {
				wantContinue, wantFinish = 1, 0
			}
			if observed.continued != wantContinue || len(runs.finished) != wantFinish || len(runs.checkpoints) != 1 {
				t.Fatalf("continued=%d finished=%d checkpoints=%v; want continue=%d finish=%d after one committed page", observed.continued, len(runs.finished), runs.checkpoints, wantContinue, wantFinish)
			}
			if tc.replay && (len(ingest.accepted) != 0 || runs.items[0]) {
				t.Fatal("replayed cursor progress must continue without a fresh acceptance")
			}
			var diagnostic struct {
				Acquisition struct {
					Continuation bool `json:"continuation"`
				} `json:"acquisition"`
			}
			if err := json.Unmarshal(runs.progress[0].Diagnostics, &diagnostic); err != nil || diagnostic.Acquisition.Continuation != (tc.want && tc.bound != "checkpoint") {
				t.Fatalf("continuation diagnostic=%s err=%v", runs.progress[0].Diagnostics, err)
			}
		})
	}
}
