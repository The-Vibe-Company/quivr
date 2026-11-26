package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

type fakeObj struct {
	Key          string
	ETag         string
	LastModified time.Time
	ContentType  string
	Body         []byte
}

type fakeS3 struct {
	mu       sync.Mutex
	objects  map[string]fakeObj
	deny     bool
	listHits int
	getHits  map[string]int
	onGet    func(key string)
}

func newFakeS3(objs ...fakeObj) *fakeS3 {
	f := &fakeS3{objects: map[string]fakeObj{}, getHits: map[string]int{}}
	for _, o := range objs {
		f.objects[o.Key] = o
	}
	return f
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deny {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `<Error><Code>AccessDenied</Code><Message>denied</Message></Error>`)
		return
	}
	q := r.URL.Query()
	if q.Get("list-type") == "2" {
		f.listHits++
		prefix := q.Get("prefix")
		startAfter := q.Get("start-after")
		keys := make([]string, 0, len(f.objects))
		for k := range f.objects {
			if strings.HasPrefix(k, prefix) && (startAfter == "" || k > startAfter) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString(`<ListBucketResult><IsTruncated>false</IsTruncated>`)
		for _, k := range keys {
			o := f.objects[k]
			etag := strings.ReplaceAll(o.ETag, `"`, `&quot;`)
			fmt.Fprintf(&b, `<Contents><Key>%s</Key><LastModified>%s</LastModified><ETag>%s</ETag><Size>%d</Size></Contents>`,
				o.Key, o.LastModified.UTC().Format(time.RFC3339Nano), etag, len(o.Body))
		}
		b.WriteString(`</ListBucketResult>`)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, b.String())
		return
	}
	// Path style: /<bucket>/<key>
	trimmed := strings.TrimPrefix(r.URL.Path, "/")
	_, key, ok := strings.Cut(trimmed, "/")
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if f.onGet != nil {
		f.onGet(key)
	}
	o, exists := f.objects[key]
	if !exists {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `<Error><Code>NoSuchKey</Code><Message>missing</Message></Error>`)
		return
	}
	if ifMatch := r.Header.Get("If-Match"); ifMatch != "" && ifMatch != o.ETag {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusPreconditionFailed)
		_, _ = io.WriteString(w, `<Error><Code>PreconditionFailed</Code><Message>changed</Message></Error>`)
		return
	}
	f.getHits[key]++
	if o.ContentType != "" {
		w.Header().Set("Content-Type", o.ContentType)
	}
	w.Header().Set("ETag", o.ETag)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(o.Body)
}

func makeCred(t *testing.T) quivrplugin.Credential {
	t.Helper()
	var c quivrplugin.Credential
	raw := []byte(`{"access_key_id":"test-key","secret_access_key":"test-secret"}`)
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func makeConfig(t *testing.T, cfg map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestKeyCheckpointStreamsAndResumesAcrossModalities(t *testing.T) {
	t0 := time.Date(2026, 1, 10, 10, 0, 0, 0, time.UTC)
	pngBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 1}
	s3srv := newFakeS3(
		fakeObj{Key: "inbox/a.txt", ETag: `"e1"`, LastModified: t0, ContentType: "application/octet-stream", Body: []byte("plain note")},
		fakeObj{Key: "inbox/b.xml", ETag: `"e2"`, LastModified: t0.Add(time.Minute), ContentType: "application/xml; charset=utf-8", Body: []byte("<doc>xml</doc>")},
		fakeObj{Key: "inbox/c.png", ETag: `"e3"`, LastModified: t0.Add(2 * time.Minute), ContentType: "", Body: pngBytes},
		fakeObj{Key: "inbox/empty.txt", ETag: `"e4"`, LastModified: t0.Add(3 * time.Minute), ContentType: "text/plain", Body: nil},
		fakeObj{Key: "inbox/skip.tmp", ETag: `"e5"`, LastModified: t0.Add(4 * time.Minute), ContentType: "text/plain", Body: []byte("ignored")},
	)
	srv := httptest.NewServer(s3srv)
	defer srv.Close()

	conn := NewPrefixConnector()
	defer conn.Close()
	cfg := makeConfig(t, map[string]any{
		"bucket":           "demo",
		"prefix":           "inbox/",
		"region":           "us-east-1",
		"endpoint":         srv.URL,
		"exclude_patterns": []string{"**/*.tmp"},
		"checkpoint_mode":  "key",
		"batch_size":       2,
		"concurrency":      4,
	})
	cred := makeCred(t)

	req := &quivrplugin.FetchRequest{
		OrganizationID: "org-1",
		Credential:     cred,
		Checkpoint:     json.RawMessage("null"),
	}
	req.Connector.ID = "inst-1"
	req.Connector.Kind = Kind
	req.Connector.Config = cfg

	p1, err := conn.Fetch(context.Background(), req)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(p1.Items) != 2 || !p1.More {
		t.Fatalf("page 1 items=%d more=%v, want 2 true", len(p1.Items), p1.More)
	}
	if p1.Items[0].RecordKey != "a.txt" || p1.Items[0].Attachments[0].MediaType != "text/plain" {
		t.Fatalf("item 0 = %+v", p1.Items[0])
	}
	if p1.Items[1].RecordKey != "b.xml" || p1.Items[1].Attachments[0].MediaType != "application/xml" {
		t.Fatalf("item 1 = %+v", p1.Items[1])
	}
	if p1.SubmissionConcurrency != 4 {
		t.Fatalf("concurrency = %d, want 4", p1.SubmissionConcurrency)
	}

	// OpenAttachment hits warm cache first (no second S3 GET), then falls back to cold S3 read.
	if s3srv.getHits["inbox/a.txt"] != 1 {
		t.Fatalf("expected 1 S3 GET during Fetch, got %d", s3srv.getHits["inbox/a.txt"])
	}
	attReq := &quivrplugin.AttachmentRequest{OrganizationID: "org-1", Credential: cred}
	attReq.Connector.ID = "inst-1"
	attReq.Connector.Config = cfg
	attReq.Attachment = p1.Items[0].Attachments[0]
	rc, err := conn.OpenAttachment(context.Background(), attReq)
	if err != nil {
		t.Fatalf("warm OpenAttachment: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "plain note" {
		t.Fatalf("warm bytes = %q", string(got))
	}
	if s3srv.getHits["inbox/a.txt"] != 1 {
		t.Fatalf("warm OpenAttachment should hit cache without S3 GET, got %d hits", s3srv.getHits["inbox/a.txt"])
	}

	// Evict session cache and confirm cold S3 fallback succeeds.
	conn.Close()
	rc, err = conn.OpenAttachment(context.Background(), attReq)
	if err != nil {
		t.Fatalf("cold OpenAttachment: %v", err)
	}
	got, _ = io.ReadAll(rc)
	rc.Close()
	if string(got) != "plain note" || s3srv.getHits["inbox/a.txt"] != 2 {
		t.Fatalf("cold fallback failed: bytes=%q hits=%d", string(got), s3srv.getHits["inbox/a.txt"])
	}

	// Page 2 resumes from page 1's checkpoint, emits c.png, skips empty.txt.
	cpBytes, _ := json.Marshal(p1.Checkpoint)
	req.Checkpoint = cpBytes
	p2, err := conn.Fetch(context.Background(), req)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(p2.Items) != 1 || p2.More {
		t.Fatalf("page 2 items=%d more=%v, want 1 false", len(p2.Items), p2.More)
	}
	if p2.Items[0].RecordKey != "c.png" || p2.Items[0].Attachments[0].MediaType != "image/png" {
		t.Fatalf("item 0 = %+v", p2.Items[0])
	}

	// Page 3 is empty and terminal.
	cpBytes, _ = json.Marshal(p2.Checkpoint)
	req.Checkpoint = cpBytes
	p3, err := conn.Fetch(context.Background(), req)
	if err != nil {
		t.Fatalf("page 3: %v", err)
	}
	if len(p3.Items) != 0 || p3.More {
		t.Fatalf("page 3 items=%d more=%v, want 0 false", len(p3.Items), p3.More)
	}
}

func TestTimestampCheckpointOrderingTieBreakAndOverwrite(t *testing.T) {
	t0 := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	s3srv := newFakeS3(
		fakeObj{Key: "feed/z-old.txt", ETag: `"e1"`, LastModified: t0, Body: []byte("first by mtime")},
		fakeObj{Key: "feed/a-tie.txt", ETag: `"e2"`, LastModified: t0.Add(time.Minute), Body: []byte("tie A")},
		fakeObj{Key: "feed/b-tie.txt", ETag: `"e3"`, LastModified: t0.Add(time.Minute), Body: []byte("tie B")},
	)
	srv := httptest.NewServer(s3srv)
	defer srv.Close()

	conn := NewPrefixConnector()
	defer conn.Close()
	cfg := makeConfig(t, map[string]any{
		"bucket":          "demo",
		"prefix":          "feed/",
		"region":          "us-east-1",
		"endpoint":        srv.URL,
		"checkpoint_mode": "timestamp",
		"batch_size":      2,
	})
	cred := makeCred(t)
	req := &quivrplugin.FetchRequest{OrganizationID: "org-1", Credential: cred, Checkpoint: json.RawMessage("null")}
	req.Connector.ID = "inst-ts"
	req.Connector.Config = cfg

	p1, err := conn.Fetch(context.Background(), req)
	if err != nil {
		t.Fatalf("ts page 1: %v", err)
	}
	if len(p1.Items) != 2 || !p1.More {
		t.Fatalf("ts page 1 items=%d more=%v", len(p1.Items), p1.More)
	}
	if p1.Items[0].RecordKey != "z-old.txt" || p1.Items[1].RecordKey != "a-tie.txt" {
		t.Fatalf("ts page 1 order = [%s, %s]", p1.Items[0].RecordKey, p1.Items[1].RecordKey)
	}

	cp1, _ := json.Marshal(p1.Checkpoint)
	req.Checkpoint = cp1
	req.PageInRun = 1
	p2, err := conn.Fetch(context.Background(), req)
	if err != nil {
		t.Fatalf("ts page 2: %v", err)
	}
	if len(p2.Items) != 1 || p2.More || p2.Items[0].RecordKey != "b-tie.txt" {
		t.Fatalf("ts page 2 = %+v", p2)
	}
	if s3srv.listHits != 1 {
		t.Fatalf("timestamp mode should reuse bounded snapshot across pages in one run, got %d ListObjectsV2 calls", s3srv.listHits)
	}

	// Overwrite z-old.txt with a newer timestamp and confirm incremental pickup on next run.
	s3srv.mu.Lock()
	s3srv.objects["feed/z-old.txt"] = fakeObj{
		Key:          "feed/z-old.txt",
		ETag:         `"e1-v2"`,
		LastModified: t0.Add(5 * time.Minute),
		Body:         []byte("updated content"),
	}
	s3srv.mu.Unlock()

	cp2, _ := json.Marshal(p2.Checkpoint)
	req.Checkpoint = cp2
	req.PageInRun = 0
	p3, err := conn.Fetch(context.Background(), req)
	if err != nil {
		t.Fatalf("ts page 3: %v", err)
	}
	if len(p3.Items) != 1 || p3.Items[0].RecordKey != "z-old.txt" || p3.Items[0].Revision == p1.Items[0].Revision {
		t.Fatalf("overwrite not picked up cleanly: %+v", p3.Items)
	}
}

func TestAllowedMediaTypesOversizedAndAccessErrors(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	s3srv := newFakeS3(
		fakeObj{Key: "mix/doc.txt", ETag: `"e1"`, LastModified: t0, Body: []byte("hello")},
		fakeObj{Key: "mix/pic.png", ETag: `"e2"`, LastModified: t0, ContentType: "application/octet-stream", Body: []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}},
		fakeObj{Key: "mix/huge.png", ETag: `"e3"`, LastModified: t0, ContentType: "image/png", Body: []byte("0123456789abcdef")},
	)
	srv := httptest.NewServer(s3srv)
	defer srv.Close()

	conn := NewPrefixConnector()
	defer conn.Close()
	cfg := makeConfig(t, map[string]any{
		"bucket":              "demo",
		"prefix":              "mix/",
		"region":              "us-east-1",
		"endpoint":            srv.URL,
		"allowed_media_types": []string{"image/png"},
		"max_object_bytes":    10,
		"skip_oversized":      true,
	})
	cred := makeCred(t)
	req := &quivrplugin.FetchRequest{OrganizationID: "org-1", Credential: cred, Checkpoint: json.RawMessage("null")}
	req.Connector.ID = "inst-filter"
	req.Connector.Config = cfg

	page, err := conn.Fetch(context.Background(), req)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].RecordKey != "pic.png" {
		t.Fatalf("filtered items = %+v", page.Items)
	}

	// AccessDenied maps to access error without leaking endpoint or secret.
	s3srv.mu.Lock()
	s3srv.deny = true
	s3srv.mu.Unlock()
	credReq := &quivrplugin.CredentialRequest{OrganizationID: "org-1", Credential: cred}
	credReq.Connector.Config = cfg
	_, err = conn.CheckCredential(context.Background(), credReq)
	if err == nil {
		t.Fatalf("expected access error")
	}
	if strings.Contains(err.Error(), srv.URL) || strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("error leaked secret/endpoint: %v", err)
	}
}

func TestLiteralPercentKeysCheckpointValidationChurnAndSessionCap(t *testing.T) {
	t0 := time.Date(2026, 4, 1, 8, 0, 0, 0, time.UTC)

	t.Run("literal percent keys preserve exact identity", func(t *testing.T) {
		s3srv := newFakeS3(
			fakeObj{Key: "inbox/Q3 100%.txt", ETag: `"e1"`, LastModified: t0, Body: []byte("quarter")},
			fakeObj{Key: "inbox/a%2Fb.txt", ETag: `"e2"`, LastModified: t0.Add(time.Minute), Body: []byte("encoded")},
		)
		srv := httptest.NewServer(s3srv)
		defer srv.Close()
		conn := NewPrefixConnector()
		defer conn.Close()
		cfg := makeConfig(t, map[string]any{
			"bucket":   "demo",
			"prefix":   "inbox/",
			"region":   "us-east-1",
			"endpoint": srv.URL,
		})
		req := &quivrplugin.FetchRequest{OrganizationID: "org-1", Credential: makeCred(t), Checkpoint: json.RawMessage("null")}
		req.Connector.ID = "inst-pct"
		req.Connector.Config = cfg
		page, err := conn.Fetch(context.Background(), req)
		if err != nil {
			t.Fatalf("Fetch with literal %% keys failed: %v", err)
		}
		if len(page.Items) != 2 || page.Items[0].RecordKey != "Q3 100%.txt" || page.Items[1].RecordKey != "a%2Fb.txt" {
			t.Fatalf("unexpected record keys: %+v", page.Items)
		}
	})

	t.Run("out-of-prefix checkpoint rejected", func(t *testing.T) {
		s3srv := newFakeS3()
		srv := httptest.NewServer(s3srv)
		defer srv.Close()
		conn := NewPrefixConnector()
		defer conn.Close()
		cfg := makeConfig(t, map[string]any{
			"bucket":   "demo",
			"prefix":   "inbox/",
			"region":   "us-east-1",
			"endpoint": srv.URL,
		})
		req := &quivrplugin.FetchRequest{
			OrganizationID: "org-1",
			Credential:     makeCred(t),
			Checkpoint:     json.RawMessage(`{"mode":"key","last_key":"other/z.txt","objects_done":1}`),
		}
		req.Connector.ID = "inst-bad-cp"
		req.Connector.Config = cfg
		_, err := conn.Fetch(context.Background(), req)
		if err == nil || !strings.Contains(err.Error(), "invalid_checkpoint") {
			t.Fatalf("expected invalid_checkpoint error, got %v", err)
		}
	})

	t.Run("mid-fetch delete skipped and overwrite returns transient error", func(t *testing.T) {
		s3srv := newFakeS3(
			fakeObj{Key: "inbox/a-deleted.txt", ETag: `"e1"`, LastModified: t0, Body: []byte("gone")},
			fakeObj{Key: "inbox/b-kept.txt", ETag: `"e2"`, LastModified: t0.Add(time.Minute), Body: []byte("kept")},
		)
		s3srv.onGet = func(key string) {
			if key == "inbox/a-deleted.txt" {
				delete(s3srv.objects, key)
			}
		}
		srv := httptest.NewServer(s3srv)
		defer srv.Close()
		conn := NewPrefixConnector()
		defer conn.Close()
		cfg := makeConfig(t, map[string]any{
			"bucket":   "demo",
			"prefix":   "inbox/",
			"region":   "us-east-1",
			"endpoint": srv.URL,
		})
		req := &quivrplugin.FetchRequest{OrganizationID: "org-1", Credential: makeCred(t), Checkpoint: json.RawMessage("null")}
		req.Connector.ID = "inst-churn"
		req.Connector.Config = cfg
		page, err := conn.Fetch(context.Background(), req)
		if err != nil {
			t.Fatalf("delete churn should skip object, got %v", err)
		}
		if len(page.Items) != 1 || page.Items[0].RecordKey != "b-kept.txt" {
			t.Fatalf("expected only b-kept.txt, got %+v", page.Items)
		}

		// Now mutate ETag before GetObject to trigger PreconditionFailed -> transient error.
		s3srv2 := newFakeS3(
			fakeObj{Key: "inbox/c-changed.txt", ETag: `"e1"`, LastModified: t0, Body: []byte("v1")},
		)
		s3srv2.onGet = func(key string) {
			o := s3srv2.objects[key]
			o.ETag = `"e2"`
			s3srv2.objects[key] = o
		}
		srv2 := httptest.NewServer(s3srv2)
		defer srv2.Close()
		req.Connector.Config = makeConfig(t, map[string]any{
			"bucket":   "demo",
			"prefix":   "inbox/",
			"region":   "us-east-1",
			"endpoint": srv2.URL,
		})
		_, err = conn.Fetch(context.Background(), req)
		if err == nil || !strings.Contains(err.Error(), "object_changed") {
			t.Fatalf("expected object_changed error, got %v", err)
		}
	})

	t.Run("maxSessions enforced when all sessions are locked", func(t *testing.T) {
		conn := NewPrefixConnector()
		locked := make([]*prefixSession, 0, maxSessions)
		for i := 0; i < maxSessions; i++ {
			s, err := conn.session(fmt.Sprintf("k-%d", i))
			if err != nil {
				t.Fatalf("session %d: %v", i, err)
			}
			s.mu.Lock()
			s.expires = time.Now().Add(time.Minute)
			locked = append(locked, s)
		}
		_, err := conn.session("overflow")
		for _, s := range locked {
			s.mu.Unlock()
		}
		conn.Close()
		if err == nil || !strings.Contains(err.Error(), "prefix_capacity") {
			t.Fatalf("expected prefix_capacity error, got %v", err)
		}
	})
}
