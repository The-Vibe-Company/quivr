package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

// attachingConnector returns the same scripted items on every fetch.
type attachingConnector struct {
	items  func() []Item
	opened *int
}

func (attachingConnector) Kind() string                   { return "fixture" }
func (attachingConnector) ConfigSchema() []byte           { return []byte(`{"type":"object"}`) }
func (attachingConnector) CredentialSchema() []byte       { return nil }
func (attachingConnector) DefaultInterval() time.Duration { return time.Minute }
func (c attachingConnector) Fetch(context.Context, FetchRequest) (Page, error) {
	return Page{Items: c.items(), Checkpoint: json.RawMessage(`{}`)}, nil
}

type fakeBlobs struct {
	deposited map[string]string
	fail      error
}

func (f *fakeBlobs) Deposit(_ context.Context, org string, r io.Reader, mediaType string, max int64) (string, int64, error) {
	if f.fail != nil {
		return "", 0, f.fail
	}
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return "", 0, err
	}
	if int64(len(b)) > max {
		return "", 0, content.ErrInvalid
	}
	id := "blob_" + content.Hash(b)[:8]
	if f.deposited == nil {
		f.deposited = map[string]string{}
	}
	f.deposited[id] = string(b)
	return id, int64(len(b)), nil
}

type fakeReceipts struct{ keys map[string]bool }

func (f fakeReceipts) HasReceipt(_ context.Context, _, key string) (bool, error) {
	return f.keys[key], nil
}

func mailItem(revision string, opened *int) Item {
	open := func(text string) func(context.Context) (io.ReadCloser, error) {
		return func(context.Context) (io.ReadCloser, error) {
			*opened++
			return io.NopCloser(strings.NewReader(text)), nil
		}
	}
	return Item{RecordKey: "<m1@example.org>", Revision: revision,
		Manifest: &content.Manifest{Kind: "manifest", Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: "Hello"}}}},
		Attachments: []Attachment{
			{Key: "original_body", Role: "original_body", MediaType: "text/html", Open: open("<p>Hello</p>")},
			{Key: "attachment-1", Role: "attachment", MediaType: "application/pdf", Open: open("%PDF-1.7 bytes")},
		}}
}

func attachingAcquirer(t *testing.T, items func() []Item) (Acquirer, *fakeRuns, *fakeIngest, *fakeBlobs) {
	t.Helper()
	a, runs, ingest := newAcquirer(t, `{}`, "")
	registry, err := NewRegistry(attachingConnector{items: items})
	if err != nil {
		t.Fatal(err)
	}
	blobs := &fakeBlobs{}
	a.Registry, a.Blobs = registry, blobs
	return a, runs, ingest, blobs
}

func TestAttachmentsAreStreamedIntoVerifiedBlobPartsBeforeSubmission(t *testing.T) {
	opened := 0
	a, runs, ingest, blobs := attachingAcquirer(t, func() []Item { return []Item{mailItem("sha256:r1", &opened)} })
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
	for _, p := range c.Manifest.Parts[1:] {
		if p.Content.Kind != "blob" || blobs.deposited[p.Content.BlobID] == "" || p.Content.MediaType == "" {
			t.Fatalf("part %+v not a deposited Blob Part", p)
		}
	}
	if blobs.deposited[c.Manifest.Parts[2].Content.BlobID] != "%PDF-1.7 bytes" {
		t.Fatal("attachment bytes not preserved")
	}
}

func TestAnAlreadyAcceptedRevisionIsSkippedBeforeAnyDownload(t *testing.T) {
	opened := 0
	a, _, ingest, _ := attachingAcquirer(t, func() []Item { return []Item{mailItem("sha256:r1", &opened)} })
	key := KeyPrefix + content.StableID("item", "connector_1", "<m1@example.org>", "sha256:r1")
	a.Receipts = fakeReceipts{keys: map[string]bool{key: true}}
	if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
		t.Fatal(err)
	}
	if opened != 0 || len(ingest.accepted) != 0 {
		t.Fatalf("opened %d accepted %d; an accepted revision must not be downloaded again", opened, len(ingest.accepted))
	}
}

func TestAChangedRevisionOfAnAcceptedRecordIsStillSubmitted(t *testing.T) {
	opened := 0
	a, _, ingest, _ := attachingAcquirer(t, func() []Item { return []Item{mailItem("sha256:r2", &opened)} })
	old := KeyPrefix + content.StableID("item", "connector_1", "<m1@example.org>", "sha256:r1")
	a.Receipts = fakeReceipts{keys: map[string]bool{old: true}}
	if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
		t.Fatal(err)
	}
	if opened != 2 || len(ingest.accepted) != 1 || ingest.accepted[0].Revision != "sha256:r2" {
		t.Fatalf("opened %d accepted %+v; a changed message must become a correction", opened, ingest.accepted)
	}
}

func TestAttachmentFailuresEndTheRunWithTheirClass(t *testing.T) {
	for name, tc := range map[string]struct {
		open  error
		store error
		class ErrorClass
		code  string
	}{
		"source refuses":    {open: AccessError("mailbox_access_denied"), class: ClassAccess, code: "mailbox_access_denied"},
		"source unreadable": {open: errors.New("reset"), class: ClassTransient, code: "source_unavailable"},
		"storage down":      {store: errors.New("s3 down"), class: ClassTransient, code: "ingestion_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			a, runs, ingest, blobs := attachingAcquirer(t, func() []Item {
				item := mailItem("sha256:r1", new(int))
				if tc.open != nil {
					item.Attachments[1].Open = func(context.Context) (io.ReadCloser, error) { return nil, tc.open }
				}
				return []Item{item}
			})
			blobs.fail = tc.store
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

func TestAnAttachmentLargerThanAnnouncedIsSkippedWithItsReason(t *testing.T) {
	var reasons []string
	a, runs, ingest, _ := attachingAcquirer(t, func() []Item {
		item := mailItem("sha256:r1", new(int))
		item.Attachments[1].Open = func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(io.LimitReader(zeroReader{}, MaxAttachmentBytes+1)), nil
		}
		item.Attachments[1].Skip = func(reason string) { reasons = append(reasons, reason) }
		return []Item{item}
	})
	if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
		t.Fatal(err)
	}
	if len(ingest.accepted) != 1 || runs.finished[0] != nil || len(reasons) != 1 || reasons[0] != "too_large" {
		t.Fatalf("accepted %d finished %+v reasons %v", len(ingest.accepted), runs.finished, reasons)
	}
	for _, p := range ingest.accepted[0].Manifest.Parts {
		if p.Key == "attachment-1" {
			t.Fatal("an oversized attachment must not become a Part")
		}
	}
}

func TestAnOversizedAttachmentWithoutSkipRejectsOnlyItsItem(t *testing.T) {
	a, runs, ingest, _ := attachingAcquirer(t, func() []Item {
		item := mailItem("sha256:r1", new(int))
		item.Attachments[1].Open = func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(io.LimitReader(zeroReader{}, MaxAttachmentBytes+1)), nil
		}
		return []Item{item}
	})
	if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
		t.Fatal(err)
	}
	if len(ingest.accepted) != 0 || runs.finished[0] == nil || runs.finished[0].Code != "item_rejected" || !runs.finished[0].Completed || len(runs.checkpoints) != 1 {
		t.Fatalf("finished %+v checkpoints %v", runs.finished, runs.checkpoints)
	}
}

func TestASourceFailureWhileStreamingIsASourceFailure(t *testing.T) {
	a, runs, ingest, _ := attachingAcquirer(t, func() []Item {
		item := mailItem("sha256:r1", new(int))
		item.Attachments[1].Open = func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(io.MultiReader(strings.NewReader("partial"), failingReader{})), nil
		}
		return []Item{item}
	})
	if err := a.Run(context.Background(), "org_a", "connector_1", 3); err != nil {
		t.Fatal(err)
	}
	if got := runs.finished[0]; got == nil || got.Code != "source_unavailable" || len(ingest.accepted) != 0 || len(runs.checkpoints) != 0 {
		t.Fatalf("finished %+v", got)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
