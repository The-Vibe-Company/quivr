package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/adapters/s3"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/jackc/pgx/v5/pgxpool"
)

const packVersions, packParts = 30, 9

// Count on the HTTP boundary so the measurements include requests made by the
// S3 adapter itself. Only the remote object service is replaced.
type packBucket struct {
	mu      sync.RWMutex
	objects map[string][]byte
	puts    atomic.Int64
	gets    atomic.Int64
}

func (b *packBucket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/test/")
	switch r.Method {
	case http.MethodPut:
		data, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		b.puts.Add(1)
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, exists := b.objects[key]; exists {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		b.objects[key] = data
	case http.MethodGet:
		b.gets.Add(1)
		b.mu.RLock()
		data, exists := b.objects[key]
		b.mu.RUnlock()
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(data)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

type textPackFixture struct {
	pool    *pgxpool.Pool
	store   fixtureContentStores
	service content.Service
	scope   corpus.Scope
	corpus  corpus.Corpus
	bucket  *packBucket
	media   content.VerifiedBlob
}

const packMedia = "\x00\xff\x01"

func newTextPackFixture(t testing.TB, ctx context.Context) textPackFixture {
	t.Helper()
	pool := adapterPool(t, ctx)
	bucket := &packBucket{objects: map[string][]byte{}}
	server := httptest.NewServer(bucket)
	t.Cleanup(server.Close)
	blobs := s3.New(s3.Config{Endpoint: server.URL, Bucket: "test", AccessKey: "test", SecretKey: "test"})
	scope := corpus.Scope{
		Organization: fmt.Sprintf("adapter-text-pack-%d", time.Now().UnixNano()),
		Actions:      []string{"corpora:write", "content:write", "content:read", "search:query"},
		Corpora:      []string{"*"},
	}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "docs", Name: "Docs"})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := blobs.Put(ctx, scope.Organization, []byte(packMedia))
	if err != nil {
		t.Fatal(err)
	}
	media := content.VerifiedBlob{ID: "blob_media", MediaType: "image/png", Blob: blob}
	store := contentStores(pool)
	service := content.Service{
		Submissions: store, Receipts: store, Versions: store, Materialization: store, Baseline: store,
		Blobs: blobs, BlobSource: blobSources{media.ID: media},
	}
	return textPackFixture{pool, store, service, scope, c, bucket, media}
}

type packCase struct {
	joined, single content.Candidate
	excerpts       []string
	version        content.Version
	textPuts       int64
}

func (f textPackFixture) publish(t testing.TB, ctx context.Context, key string, legacy bool) packCase {
	t.Helper()
	org := f.scope.Organization
	manifest := content.Manifest{Kind: "manifest"}
	var ranges []content.SourceRange
	var texts []string
	for i := range packParts {
		if i == 1 {
			manifest.Parts = append(manifest.Parts, content.Part{
				Key: "media", Role: "attachment",
				Content: content.Text{Kind: "blob", BlobID: f.media.ID, MediaType: f.media.MediaType},
			})
		}
		partKey := fmt.Sprintf("paragraph-%d", i)
		text := fmt.Sprintf("été 🌌\nparagraph %d of %s", i, key)
		manifest.Parts = append(manifest.Parts, content.Part{Key: partKey, Role: "body", Content: content.Text{Kind: "text", Text: text}})
		ranges = append(ranges, content.SourceRange{PartKey: partKey, Start: 1, End: utf8.RuneCountInString(text)})
		texts = append(texts, text)
	}
	cmd := content.Command{
		Key: key, Source: content.Source{CorpusID: f.corpus.ID, Namespace: "docs", RecordKey: key},
		Content: content.Text{Kind: "manifest"}, Manifest: &manifest,
	}
	r, err := f.service.Accept(ctx, f.scope, cmd)
	if err != nil {
		t.Fatal(err)
	}
	before := f.bucket.puts.Load()
	if legacy {
		f.publishStandalone(t, ctx, r.ID)
	} else if err := f.service.Materialize(ctx, org, r.ID); err != nil {
		t.Fatal(err)
	}
	// Each publication writes one Manifest in addition to its text objects.
	textPuts := f.bucket.puts.Load() - before - 1
	v, err := f.service.ProcessingVersion(ctx, org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	first := ranges[0]
	seg, err := content.PluginSegmentation(org, v, "plugin:adapter.fixture@1", json.RawMessage(`{}`), []content.SegmentInput{
		{PartKey: first.PartKey, Start: first.Start, End: first.End, SourceRanges: ranges, SourceSeparator: "\n\n"},
		{PartKey: first.PartKey, Start: first.Start, End: first.End},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.SaveSegmentation(ctx, org, v, seg); err != nil {
		t.Fatal(err)
	}
	g, err := f.store.Generation(ctx, org, f.corpus.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.Promote(ctx, org, seg, g); err != nil {
		t.Fatal(err)
	}
	excerpts := make([]string, len(texts))
	for i, text := range texts {
		excerpts[i] = strings.TrimPrefix(text, "é")
	}
	return packCase{
		joined:   content.Candidate{SegmentID: seg.Segments[0].ID, GenerationID: g.ID, VersionID: v.ID},
		single:   content.Candidate{SegmentID: seg.Segments[1].ID, GenerationID: g.ID, VersionID: v.ID},
		excerpts: excerpts, version: v, textPuts: textPuts,
	}
}

// Write the pre-pack layout, including the Version's separate normalized text
// pointer, to exercise NULL-range rows and measure the previous request count.
func (f textPackFixture) publishStandalone(t testing.TB, ctx context.Context, receipt string) {
	t.Helper()
	org := f.scope.Organization
	work, _, err := f.store.Work(ctx, org, receipt)
	if err != nil {
		t.Fatal(err)
	}
	put := func(raw []byte) content.Blob {
		t.Helper()
		blob, err := f.service.Blobs.Put(ctx, org, raw)
		if err != nil {
			t.Fatal(err)
		}
		return blob
	}
	m := content.ManifestFor(work.Command)
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	p := content.Publication{Normalized: put([]byte(content.NormalizedText(m))), Manifest: put(raw)}
	for _, part := range m.Parts {
		if part.Content.Kind == "text" {
			p.Parts = append(p.Parts, content.PartBlob{Key: part.Key, Role: part.Role, Blob: put([]byte(part.Content.Text))})
		}
	}
	if err := f.store.Publish(ctx, work, p); err != nil {
		t.Fatal(err)
	}
}

func TestVersionTextPackMaterializationAndHydration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	f := newTextPackFixture(t, ctx)
	cases := make([]packCase, packVersions)
	candidates := make([]content.Candidate, packVersions)
	for i := range cases {
		c := f.publish(t, ctx, fmt.Sprintf("packed-%d", i), false)
		if c.textPuts != 1 {
			t.Fatalf("Version %d wrote %d text objects, want 1", i, c.textPuts)
		}
		cases[i], candidates[i] = c, c.joined
	}
	first := cases[0]
	media := first.version.Manifest.Parts[1].Content
	if media.Kind != "blob" || media.BlobID != f.media.ID || media.BlobSHA256 != f.media.Blob.SHA256 {
		t.Fatalf("materialization changed the media reference: %+v", media)
	}
	if raw, err := f.service.Blobs.Read(ctx, f.media.Blob); err != nil || string(raw) != packMedia {
		t.Fatalf("separate media object = %q, %v", raw, err)
	}

	legacy := f.publish(t, ctx, "legacy", true)
	check := func(candidates []content.Candidate, wantGets int64) map[int]content.Hydrated {
		t.Helper()
		before := f.bucket.gets.Load()
		got, err := f.service.Hydrate(ctx, f.scope, candidates)
		if err != nil || len(got) != len(candidates) {
			t.Fatalf("hydrated %d/%d candidates: %v", len(got), len(candidates), err)
		}
		if n := f.bucket.gets.Load() - before; n != wantGets {
			t.Fatalf("cold hydration made %d GetObject calls for %d candidates, want %d", n, len(candidates), wantGets)
		}
		return got
	}
	assertJoined := func(got content.Hydrated, c packCase) {
		t.Helper()
		if got.VersionID != c.version.ID || got.Segment.Text != strings.Join(c.excerpts, "\n\n") {
			t.Fatalf("hydrated Version %s text %q, want %s text %q", got.VersionID, got.Segment.Text, c.version.ID, strings.Join(c.excerpts, "\n\n"))
		}
		if n := len(got.Segment.SourceExcerpts); n != len(c.excerpts) {
			t.Fatalf("got %d source excerpts, want %d", n, len(c.excerpts))
		}
		for i, excerpt := range got.Segment.SourceExcerpts {
			if excerpt.Text != c.excerpts[i] || excerpt.PartKey != fmt.Sprintf("paragraph-%d", i) {
				t.Fatalf("source excerpt %d = %+v, want %q from its matching Part", i, excerpt, c.excerpts[i])
			}
		}
	}
	got := check(candidates, packVersions)
	for i, c := range cases {
		assertJoined(got[i], c)
	}
	// Packed multi-part + single-part share one normalized text blob; a legacy
	// multi-part segment coalesces to one Manifest read; a legacy single-part
	// segment still reads its standalone Part blob directly.
	got = check([]content.Candidate{first.joined, first.single, legacy.joined, legacy.single}, 3)
	assertJoined(got[0], first)
	assertJoined(got[2], legacy)
	if got[1].VersionID != first.version.ID || got[1].Segment.Text != first.excerpts[0] {
		t.Fatalf("single-Part candidate hydrated %+v, want %q from %s", got[1], first.excerpts[0], first.version.ID)
	}
	if got[3].VersionID != legacy.version.ID || got[3].Segment.Text != legacy.excerpts[0] {
		t.Fatalf("legacy single-Part candidate hydrated %+v, want %q from %s", got[3], legacy.excerpts[0], legacy.version.ID)
	}

	assertCorrupt := func(t *testing.T) {
		t.Helper()
		got, err := f.service.Hydrate(ctx, f.scope, []content.Candidate{first.single})
		if len(got) != 0 || !errors.Is(err, content.ErrArtifactCorrupt) {
			t.Fatalf("corrupt pack hydrated %v, %v; want no candidates and an integrity error", got, err)
		}
	}
	stored, err := f.store.Version(ctx, f.scope.Organization, first.version.RecordID, first.version.ID)
	if err != nil {
		t.Fatal(err)
	}
	key := stored.TextBlob.Key
	f.bucket.mu.RLock()
	raw := f.bucket.objects[key]
	f.bucket.mu.RUnlock()
	if len(raw) == 0 {
		t.Fatal("Version's normalized text object is absent")
	}
	changed := bytes.Clone(raw)
	changed[len(changed)-1] = 'X'
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"truncated", raw[:len(raw)-1]},
		// Corrupt a byte outside the requested Part to prove full-object validation.
		{"sha-mismatch", changed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.bucket.mu.Lock()
			f.bucket.objects[key] = tc.data
			f.bucket.mu.Unlock()
			t.Cleanup(func() {
				f.bucket.mu.Lock()
				f.bucket.objects[key] = raw
				f.bucket.mu.Unlock()
			})
			assertCorrupt(t)
		})
	}
	length := len("é" + first.excerpts[0])
	const updateRange = `UPDATE version_parts SET pack_offset=$3,pack_length=$4
WHERE organization=$1 AND version_id=$2 AND part_key='paragraph-0'`
	for _, tc := range []struct {
		name           string
		offset, length any
	}{
		{"half-null", nil, 1},
		{"negative", -1, 1},
		{"overflow", 1, int64(1<<63 - 1)},
		{"mid-rune", 1, length - 1},
		{"wrong-part", length + 1, length},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.pool.Exec(ctx, updateRange, f.scope.Organization, first.version.ID, tc.offset, tc.length); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := f.pool.Exec(ctx, updateRange, f.scope.Organization, first.version.ID, 0, length); err != nil {
					t.Error(err)
				}
			})
			assertCorrupt(t)
		})
	}
}

func BenchmarkVersionTextStorage(b *testing.B) {
	for _, variant := range []struct {
		name   string
		legacy bool
	}{
		{"standalone", true},
		{"packed", false},
	} {
		b.Run(variant.name, func(b *testing.B) {
			ctx := b.Context()
			f := newTextPackFixture(b, ctx)
			var candidates []content.Candidate
			var puts int64
			for i := range packVersions {
				c := f.publish(b, ctx, fmt.Sprintf("bench-%d", i), variant.legacy)
				candidates = append(candidates, c.joined)
				puts += c.textPuts
			}
			before := f.bucket.gets.Load()
			for b.Loop() {
				got, err := f.service.Hydrate(ctx, f.scope, candidates)
				if err != nil || len(got) != packVersions {
					b.Fatalf("hydrated %d/%d candidates: %v", len(got), packVersions, err)
				}
			}
			b.ReportMetric(float64(puts)/packVersions, "text-PUTs/version")
			b.ReportMetric(float64(f.bucket.gets.Load()-before)/float64(b.N), "GETs/search")
		})
	}
}
