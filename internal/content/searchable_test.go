package content_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

// withdrawnDuringRead never finds the stale candidate and finds the others,
// then, on the recheck, no longer finds those listed in withdrawn, as when
// their Record is withdrawn while the blob is read.
type withdrawnDuringRead struct {
	content.BaselineRepository
	text      string
	withdrawn map[string]bool
	lookups   int
}

func (b *withdrawnDuringRead) Hydrate(_ context.Context, _ corpus.Scope, cs []content.Candidate) (map[int]content.Located, error) {
	b.lookups++
	out := map[int]content.Located{}
	for i, c := range cs {
		if c.SegmentID == "stale" || b.lookups > 1 && b.withdrawn[c.SegmentID] {
			continue
		}
		start := map[string]int{"a": 0, "b": 4, "c": 8}[c.SegmentID]
		h := content.Hydrated{Segment: content.Segment{ID: c.SegmentID, Start: start, End: start + 3}, TextSHA256: content.Hash([]byte(string([]rune(b.text)[start : start+3])))}
		out[i] = content.Located{Hydrated: h, Blob: content.Blob{Key: "part"}}
	}
	return out, nil
}

type countedBlob struct {
	content.Blobs
	text  string
	reads *atomic.Int64
}

func (b countedBlob) Read(context.Context, content.Blob) ([]byte, error) {
	b.reads.Add(1)
	return []byte(b.text), nil
}

type narrowRechecker struct {
	*withdrawnDuringRead
	checks int
}

func (b *narrowRechecker) RecheckHydration(_ context.Context, _ corpus.Scope, cs []content.Candidate) (map[int]bool, error) {
	b.checks++
	out := map[int]bool{}
	for i, c := range cs {
		if !b.withdrawn[c.SegmentID] {
			out[i] = true
		}
	}
	return out, nil
}

// A batch reads a blob its candidates share once, and a candidate withdrawn
// while the bytes were read is dropped at the recheck, the others kept at
// their positions.
func TestHydrationRechecksTheBatchAfterReadingIt(t *testing.T) {
	for _, narrow := range []bool{false, true} {
		const text = "één dos tri"
		baseline := &withdrawnDuringRead{text: text, withdrawn: map[string]bool{"b": true}}
		guard := &narrowRechecker{withdrawnDuringRead: baseline}
		var repository content.BaselineRepository = baseline
		wantLookups, wantChecks := 2, 0
		if narrow {
			repository = guard
			wantLookups, wantChecks = 1, 1
		}
		reads := &atomic.Int64{}
		s := content.Service{Baseline: repository, Blobs: countedBlob{text: text, reads: reads}}
		scope := corpus.Scope{Organization: "org", Actions: []string{"content:read", "search:query"}, Corpora: []string{"*"}}
		got, err := s.Hydrate(context.Background(), scope, []content.Candidate{{SegmentID: "stale"}, {SegmentID: "a"}, {SegmentID: "b"}, {SegmentID: "c"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[1].Segment.Text != "één" || got[3].Segment.Text != "tri" {
			t.Fatalf("narrow=%t: hydrated %v; want a:één at 1 and c:tri at 3, stale and b dropped", narrow, got)
		}
		if baseline.lookups != wantLookups || guard.checks != wantChecks || reads.Load() != 1 {
			t.Fatalf("narrow=%t: %d lookups, %d fences, %d blob reads; want %d lookups, %d fences and one shared blob read", narrow, baseline.lookups, guard.checks, reads.Load(), wantLookups, wantChecks)
		}
	}
}

// Packed passages retain independent Unicode source ranges and never pretend
// the joined excerpt belongs to only the first Part.
func TestPackedPassageUsesExactCanonicalRanges(t *testing.T) {
	v := content.Version{ID: "v", Manifest: content.Manifest{Parts: []content.Part{
		{Key: "a", Role: "body", Content: content.Text{Kind: "text", Text: "één 🌌"}},
		{Key: "b", Role: "body", Content: content.Text{Kind: "text", Text: "第二段"}},
	}}}
	input := content.SegmentInput{PartKey: "a", End: 5, SourceRanges: []content.SourceRange{{PartKey: "a", End: 5}, {PartKey: "b", End: 3}}, SourceSeparator: "\n\n"}
	seg, err := content.PluginSegmentation("org", v, "plugin:p@1", nil, []content.SegmentInput{input})
	if err != nil {
		t.Fatal(err)
	}
	if seg.Segments[0].Text != "één 🌌\n\n第二段" {
		t.Fatalf("packed text = %q", seg.Segments[0].Text)
	}
	changed := input
	changed.SourceSeparator = " "
	other, err := content.PluginSegmentation("org", v, "plugin:p@1", nil, []content.SegmentInput{changed})
	if err != nil || other.Segments[0].ID == seg.Segments[0].ID {
		t.Fatalf("separator must change immutable identity: %v", err)
	}
	input.SourceRanges[1].End = 4
	if _, err = content.PluginSegmentation("org", v, "plugin:p@1", nil, []content.SegmentInput{input}); err == nil {
		t.Fatal("accepted range beyond canonical Part")
	}
}

type fixtureBlobStore struct {
	content.Blobs
	mu    sync.RWMutex
	data  map[string][]byte
	reads atomic.Int64
}

func (s *fixtureBlobStore) Read(_ context.Context, b content.Blob) ([]byte, error) {
	s.reads.Add(1)
	s.mu.RLock()
	raw, ok := s.data[b.Key]
	s.mu.RUnlock()
	if !ok {
		return nil, content.ErrArtifactMissing
	}
	return append([]byte(nil), raw...), nil
}

type fixtureHydrationRepo struct {
	content.BaselineRepository
	mu        sync.RWMutex
	located   map[string]content.Located
	withdrawn map[string]bool
	deniedOrg map[string]bool
	lookups   atomic.Int64
	rechecks  atomic.Int64
	nonAtomic atomic.Bool
}

func (r *fixtureHydrationRepo) HydrateIsAtomic(_ context.Context, _ int) bool {
	return !r.nonAtomic.Load()
}

func (r *fixtureHydrationRepo) Hydrate(_ context.Context, scope corpus.Scope, cs []content.Candidate) (map[int]content.Located, error) {
	r.lookups.Add(1)
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[int]content.Located, len(cs))
	if r.deniedOrg[scope.Organization] {
		return out, nil
	}
	for i, c := range cs {
		if r.withdrawn[c.SegmentID] {
			continue
		}
		if l, ok := r.located[c.SegmentID]; ok {
			out[i] = l
		}
	}
	return out, nil
}

func (r *fixtureHydrationRepo) RecheckHydration(_ context.Context, scope corpus.Scope, cs []content.Candidate) (map[int]bool, error) {
	r.rechecks.Add(1)
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[int]bool{}
	if r.deniedOrg[scope.Organization] {
		return out, nil
	}
	for i, c := range cs {
		if _, ok := r.located[c.SegmentID]; ok && !r.withdrawn[c.SegmentID] {
			out[i] = true
		}
	}
	return out, nil
}

// Repeated hydration on multi-paragraph packed segments hits the content-addressed
// LRU cache with zero additional blob reads, skips the redundant RecheckHydration
// round-trip when there are zero misses, enforces strict (organization, key, sha256)
// isolation, never caches mismatched digests, and never bypasses Postgres Hydrate
// authorization/tombstone fences or RecheckHydration when external blob reads occur.
func TestHydrationBlobCacheReusesImmutableBlobsWithoutBypassingFences(t *testing.T) {
	partA := "één 🌌 first paragraph"
	partB := "第二段 second paragraph"
	joined := "één 🌌\n\n第二段"
	blobA := content.Blob{Key: "shared/part-a", SHA256: content.Hash([]byte(partA)), Size: int64(len(partA))}
	blobB := content.Blob{Key: "shared/part-b", SHA256: content.Hash([]byte(partB)), Size: int64(len(partB))}
	// Same blob key in another org or with a different SHA-256 must not collide.
	partAlt := "different immutable bytes"
	blobAlt := content.Blob{Key: "shared/part-a", SHA256: content.Hash([]byte(partAlt)), Size: int64(len(partAlt))}
	// A mismatched digest must never poison the cache.
	blobCorrupt := content.Blob{Key: "corrupt/part", SHA256: content.Hash([]byte("expected")), Size: int64(len(partA))}

	blobs := &fixtureBlobStore{data: map[string][]byte{
		blobA.Key:       []byte(partA),
		blobB.Key:       []byte(partB),
		blobCorrupt.Key: []byte(partA),
	}}
	repo := &fixtureHydrationRepo{
		located: map[string]content.Located{
			"packed": {
				Hydrated: content.Hydrated{
					RecordID:   "r1",
					VersionID:  "v1",
					TextSHA256: content.Hash([]byte(joined)),
					Segment: content.Segment{
						ID:      "packed",
						PartKey: "a",
						Start:   0,
						End:     5,
						Derivation: content.SegmentDerivation{
							SourceRanges:    []content.SourceRange{{PartKey: "a", Start: 0, End: 5}, {PartKey: "b", Start: 0, End: 3}},
							SourceSeparator: "\n\n",
						},
					},
				},
				Blob: blobA,
				Sources: []content.LocatedSource{
					{Range: content.SourceRange{PartKey: "a", Start: 0, End: 5}, Blob: blobA},
					{Range: content.SourceRange{PartKey: "b", Start: 0, End: 3}, Blob: blobB},
				},
			},
			"alt-sha": {
				Hydrated: content.Hydrated{
					RecordID:   "r2",
					VersionID:  "v2",
					TextSHA256: content.Hash([]byte(partAlt)),
					Segment:    content.Segment{ID: "alt-sha", PartKey: "a", Start: 0, End: len([]rune(partAlt))},
				},
				Blob: blobAlt,
			},
			"corrupt": {
				Hydrated: content.Hydrated{
					RecordID:   "r3",
					VersionID:  "v3",
					TextSHA256: content.Hash([]byte(partA)),
					Segment:    content.Segment{ID: "corrupt", PartKey: "a", Start: 0, End: len([]rune(partA))},
				},
				Blob: blobCorrupt,
			},
		},
		withdrawn: map[string]bool{},
		deniedOrg: map[string]bool{},
	}

	cache := content.NewHydrationBlobCache(1 << 20)
	svc := content.Service{Baseline: repo, Blobs: blobs, BlobCache: cache}
	orgA := corpus.Scope{Organization: "org-a", Actions: []string{"content:read", "search:query"}, Corpora: []string{"*"}}
	orgB := corpus.Scope{Organization: "org-b", Actions: []string{"content:read", "search:query"}, Corpora: []string{"*"}}

	// Cold call: reads both distinct blobs once, runs lookup + recheck.
	got, err := svc.Hydrate(context.Background(), orgA, []content.Candidate{{SegmentID: "packed"}})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Segment.Text != joined || len(got[0].Segment.SourceExcerpts) != 2 || got[0].Segment.SourceExcerpts[0].Text != "één 🌌" || got[0].Segment.SourceExcerpts[1].Text != "第二段" {
		t.Fatalf("cold packed hydration = %+v", got[0].Segment)
	}
	if blobs.reads.Load() != 2 || repo.lookups.Load() != 1 || repo.rechecks.Load() != 1 {
		t.Fatalf("cold counts: reads=%d lookups=%d rechecks=%d; want 2, 1, 1", blobs.reads.Load(), repo.lookups.Load(), repo.rechecks.Load())
	}

	// Warm repeat: zero blob misses, so canonical lookup still runs while the
	// redundant second RecheckHydration round-trip is skipped.
	got, err = svc.Hydrate(context.Background(), orgA, []content.Candidate{{SegmentID: "packed"}})
	if err != nil || got[0].Segment.Text != joined {
		t.Fatalf("warm packed hydration = %+v, %v", got, err)
	}
	if blobs.reads.Load() != 2 || repo.lookups.Load() != 2 || repo.rechecks.Load() != 1 {
		t.Fatalf("warm counts: reads=%d lookups=%d rechecks=%d; want 2, 2, 1", blobs.reads.Load(), repo.lookups.Load(), repo.rechecks.Load())
	}

	// Cross-organization isolation: org-b with the same blob keys & digests must miss
	// and trigger RecheckHydration because external blob reads occurred.
	got, err = svc.Hydrate(context.Background(), orgB, []content.Candidate{{SegmentID: "packed"}})
	if err != nil || got[0].Segment.Text != joined {
		t.Fatalf("org-b hydration = %+v, %v", got, err)
	}
	if blobs.reads.Load() != 4 || repo.rechecks.Load() != 2 {
		t.Fatalf("org-b counts: reads=%d rechecks=%d; want 4, 2", blobs.reads.Load(), repo.rechecks.Load())
	}

	// SHA-256 isolation: same org and blob key with a different digest must miss
	// and trigger RecheckHydration.
	blobs.mu.Lock()
	blobs.data[blobAlt.Key] = []byte(partAlt)
	blobs.mu.Unlock()
	got, err = svc.Hydrate(context.Background(), orgA, []content.Candidate{{SegmentID: "alt-sha"}})
	if err != nil || got[0].Segment.Text != partAlt {
		t.Fatalf("alt-sha hydration = %+v, %v", got, err)
	}
	if blobs.reads.Load() != 5 || repo.rechecks.Load() != 3 {
		t.Fatalf("different sha256 counts: reads=%d rechecks=%d; want 5, 3", blobs.reads.Load(), repo.rechecks.Load())
	}

	// Mixed batch (1 cached blob + 1 distinct uncached blob) has non-zero misses,
	// so RecheckHydration runs.
	partC := "third uncached paragraph"
	blobC := content.Blob{Key: "shared/part-c", SHA256: content.Hash([]byte(partC)), Size: int64(len(partC))}
	blobs.mu.Lock()
	blobs.data[blobC.Key] = []byte(partC)
	blobs.mu.Unlock()
	repo.mu.Lock()
	repo.located["part-c"] = content.Located{
		Hydrated: content.Hydrated{
			RecordID:   "r4",
			VersionID:  "v4",
			TextSHA256: content.Hash([]byte(partC)),
			Segment:    content.Segment{ID: "part-c", PartKey: "a", Start: 0, End: len([]rune(partC))},
		},
		Blob: blobC,
	}
	repo.mu.Unlock()
	got, err = svc.Hydrate(context.Background(), orgA, []content.Candidate{{SegmentID: "packed"}, {SegmentID: "part-c"}})
	if err != nil || got[0].Segment.Text != joined || got[1].Segment.Text != partC {
		t.Fatalf("mixed hydration = %+v, %v", got, err)
	}
	if blobs.reads.Load() != 6 || repo.rechecks.Load() != 4 {
		t.Fatalf("mixed batch counts: reads=%d rechecks=%d; want 6, 4", blobs.reads.Load(), repo.rechecks.Load())
	}

	// Non-atomic (sharded) lookup must still run RecheckHydration even when BlobCache serves 100% of blobs.
	repo.nonAtomic.Store(true)
	got, err = svc.Hydrate(context.Background(), orgA, []content.Candidate{{SegmentID: "packed"}})
	repo.nonAtomic.Store(false)
	if err != nil || got[0].Segment.Text != joined {
		t.Fatalf("sharded warm hydration = %+v, %v", got, err)
	}
	if blobs.reads.Load() != 6 || repo.rechecks.Load() != 5 {
		t.Fatalf("sharded warm batch counts: reads=%d rechecks=%d; want 6, 5", blobs.reads.Load(), repo.rechecks.Load())
	}

	// Digest mismatch against blob.SHA256 must never populate the cache.
	statsBefore := cache.Stats()
	if _, err = svc.Hydrate(context.Background(), orgA, []content.Candidate{{SegmentID: "corrupt"}}); err != nil {
		t.Fatal(err)
	}
	if cache.Stats().Entries != statsBefore.Entries {
		t.Fatalf("mismatched blob digest entered cache: entries %d -> %d", statsBefore.Entries, cache.Stats().Entries)
	}

	// Withdrawal/Tombstone after warm cache: candidate must vanish immediately.
	repo.mu.Lock()
	repo.withdrawn["packed"] = true
	repo.mu.Unlock()
	got, err = svc.Hydrate(context.Background(), orgA, []content.Candidate{{SegmentID: "packed"}})
	if err != nil || len(got) != 0 {
		t.Fatalf("withdrawn candidate leaked from warm cache: got=%+v err=%v", got, err)
	}

	// ACL denial after warm cache: candidate must vanish immediately.
	repo.mu.Lock()
	repo.withdrawn["packed"] = false
	repo.deniedOrg["org-a"] = true
	repo.mu.Unlock()
	got, err = svc.Hydrate(context.Background(), orgA, []content.Candidate{{SegmentID: "packed"}})
	if err != nil || len(got) != 0 {
		t.Fatalf("unauthorized org leaked from warm cache: got=%+v err=%v", got, err)
	}
}

// Memory cap is strictly enforced via LRU eviction, oversized blobs never evict
// live entries, concurrent hydration is race-free, and Prometheus metrics report
// exact hits, misses, evictions, bytes, cap, and entry count.
func TestHydrationBlobCacheEnforcesMemoryCapLRUAndMetrics(t *testing.T) {
	if disabled := content.NewHydrationBlobCache(0); disabled != nil {
		t.Fatalf("zero budget returned non-nil cache: %v", disabled)
	}

	// Each 100-byte payload + org("org"=3) + key("k1"=2) + sha256(64) + overhead(160) = 329 bytes.
	// A 700-byte cap holds exactly 2 entries (658 <= 700) and evicts on a 3rd.
	const maxBytes int64 = 700
	cache := content.NewHydrationBlobCache(maxBytes)

	makePayload := func(ch byte, n int) string { return strings.Repeat(string(ch), n) }
	texts := map[string]string{
		"k1": makePayload('a', 100),
		"k2": makePayload('b', 100),
		"k3": makePayload('c', 100),
		"k4": makePayload('d', 800), // exceeds maxBytes alone
	}
	blobs := &fixtureBlobStore{data: map[string][]byte{}}
	repo := &fixtureHydrationRepo{located: map[string]content.Located{}, withdrawn: map[string]bool{}, deniedOrg: map[string]bool{}}
	for k, txt := range texts {
		blobs.data[k] = []byte(txt)
		b := content.Blob{Key: k, SHA256: content.Hash([]byte(txt)), Size: int64(len(txt))}
		repo.located[k] = content.Located{
			Hydrated: content.Hydrated{
				RecordID:   "r-" + k,
				VersionID:  "v-" + k,
				TextSHA256: content.Hash([]byte(txt)),
				Segment:    content.Segment{ID: k, PartKey: "body", Start: 0, End: len(txt)},
			},
			Blob: b,
		}
	}
	svc := content.Service{Baseline: repo, Blobs: blobs, BlobCache: cache}
	scope := corpus.Scope{Organization: "org", Actions: []string{"content:read", "search:query"}, Corpora: []string{"*"}}

	hydrateKey := func(k string) {
		t.Helper()
		got, err := svc.Hydrate(context.Background(), scope, []content.Candidate{{SegmentID: k}})
		if err != nil || got[0].Segment.Text != texts[k] {
			t.Fatalf("hydrate %s: got=%+v err=%v", k, got, err)
		}
		if stats := cache.Stats(); stats.Bytes > stats.MaxBytes {
			t.Fatalf("memory cap exceeded after %s: %d > %d", k, stats.Bytes, stats.MaxBytes)
		}
	}

	// Insert k1, k2 (2 misses, 2 entries).
	hydrateKey("k1")
	hydrateKey("k2")
	// Touch k1 so k2 becomes the LRU victim (1 hit).
	hydrateKey("k1")
	if blobs.reads.Load() != 2 {
		t.Fatalf("k1 did not hit cache: reads=%d, want 2", blobs.reads.Load())
	}
	// Oversized k4 misses and must NOT evict k1 or k2.
	hydrateKey("k4")
	if stats := cache.Stats(); stats.Entries != 2 {
		t.Fatalf("oversized blob evicted live entries: entries=%d, want 2", stats.Entries)
	}
	// Insert k3: evicts k2 (LRU), keeps k1 and k3.
	hydrateKey("k3")
	if stats := cache.Stats(); stats.Entries != 2 || stats.Bytes > maxBytes {
		t.Fatalf("after k3: stats=%+v", stats)
	}
	// k1 is still cached (hit); k2 was evicted (miss).
	readsBefore := blobs.reads.Load()
	hydrateKey("k1")
	if blobs.reads.Load() != readsBefore {
		t.Fatalf("k1 should have remained MRU, but missed")
	}
	hydrateKey("k2")
	if blobs.reads.Load() != readsBefore+1 {
		t.Fatalf("k2 should have been evicted as LRU, but hit")
	}

	// Verify Prometheus metrics output.
	var prom strings.Builder
	cache.WriteMetrics(&prom)
	out := prom.String()
	for _, want := range []string{
		`quivr_hydration_blob_cache_lookups_total{result="hit"} 2`,
		`quivr_hydration_blob_cache_lookups_total{result="miss"} 5`,
		`quivr_hydration_blob_cache_evictions_total 2`,
		`quivr_hydration_blob_cache_max_bytes 700`,
		`quivr_hydration_blob_cache_entries 2`,
		`quivr_hydration_blob_cache_bytes 658`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in metrics:\n%s", want, out)
		}
	}

	// Concurrent hydration across goroutines must stay race-free and bounded.
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			keys := []string{"k1", "k2", "k3", "k4"}
			for i := range 32 {
				k := keys[(id+i)%len(keys)]
				got, err := svc.Hydrate(context.Background(), scope, []content.Candidate{{SegmentID: k}})
				if err != nil || got[0].Segment.Text != texts[k] {
					t.Errorf("concurrent hydrate %s failed: %v", k, err)
					return
				}
				if s := cache.Stats(); s.Bytes > maxBytes {
					t.Errorf("concurrent cap exceeded: %d > %d", s.Bytes, maxBytes)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

func BenchmarkHydratePackedSegments(b *testing.B) {
	const numSegments = 50
	const partsPerSegment = 5
	blobs := &fixtureBlobStore{data: map[string][]byte{}}
	repo := &fixtureHydrationRepo{located: map[string]content.Located{}, withdrawn: map[string]bool{}, deniedOrg: map[string]bool{}}
	candidates := make([]content.Candidate, numSegments)
	for s := range numSegments {
		segID := fmt.Sprintf("seg-%d", s)
		candidates[s] = content.Candidate{SegmentID: segID, GenerationID: "gen-1"}
		ranges := make([]content.SourceRange, partsPerSegment)
		sources := make([]content.LocatedSource, partsPerSegment)
		parts := make([]string, partsPerSegment)
		for p := range partsPerSegment {
			partKey := "p-" + strconv.Itoa(p)
			body := fmt.Sprintf("Segment %d paragraph %d: canonical immutable text content for hydration benchmark.", s, p)
			blobKey := fmt.Sprintf("org/sha256/%d-%d", s, p)
			blobs.data[blobKey] = []byte(body)
			blob := content.Blob{Key: blobKey, SHA256: content.Hash([]byte(body)), Size: int64(len(body))}
			r := content.SourceRange{PartKey: partKey, Start: 0, End: len([]rune(body))}
			ranges[p] = r
			sources[p] = content.LocatedSource{Range: r, Blob: blob}
			parts[p] = body
		}
		joined := strings.Join(parts, "\n\n")
		repo.located[segID] = content.Located{
			Hydrated: content.Hydrated{
				RecordID:   "rec-" + strconv.Itoa(s),
				VersionID:  "ver-" + strconv.Itoa(s),
				TextSHA256: content.Hash([]byte(joined)),
				Segment: content.Segment{
					ID:      segID,
					PartKey: ranges[0].PartKey,
					Start:   ranges[0].Start,
					End:     ranges[0].End,
					Derivation: content.SegmentDerivation{
						SourceRanges:    ranges,
						SourceSeparator: "\n\n",
					},
				},
			},
			Blob:    sources[0].Blob,
			Sources: sources,
		}
	}
	scope := corpus.Scope{Organization: "bench-org", Actions: []string{"content:read", "search:query"}, Corpora: []string{"*"}}
	ctx := context.Background()

	b.Run("uncached", func(b *testing.B) {
		svc := content.Service{Baseline: repo, Blobs: blobs}
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			got, err := svc.Hydrate(ctx, scope, candidates)
			if err != nil || len(got) != numSegments {
				b.Fatalf("hydrate failed: %d, %v", len(got), err)
			}
		}
	})

	b.Run("cached_warm", func(b *testing.B) {
		cache := content.NewHydrationBlobCache(content.DefaultHydrationBlobCacheBytes)
		svc := content.Service{Baseline: repo, Blobs: blobs, BlobCache: cache}
		if _, err := svc.Hydrate(ctx, scope, candidates); err != nil {
			b.Fatal(err)
		}
		readsAfterWarm := blobs.reads.Load()
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			got, err := svc.Hydrate(ctx, scope, candidates)
			if err != nil || len(got) != numSegments {
				b.Fatalf("hydrate failed: %d, %v", len(got), err)
			}
		}
		if blobs.reads.Load() != readsAfterWarm {
			b.Fatalf("warm benchmark triggered %d blob reads", blobs.reads.Load()-readsAfterWarm)
		}
	})
}

func TestHydrateCoalescesLegacyMultiPartManifestAndCancelsOnContext(t *testing.T) {
	manifest := content.Manifest{
		Kind: "manifest",
		Parts: []content.Part{
			{Key: "p1", Role: "body", Content: content.Text{Kind: "text", Text: "één 🌌"}},
			{Key: "p2", Role: "body", Content: content.Text{Kind: "text", Text: "second paragraph"}},
			{Key: "p3", Role: "body", Content: content.Text{Kind: "text", Text: "第二段"}},
		},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestBlob := content.Blob{
		Key:    "org/sha256/manifest-1",
		SHA256: content.Hash(manifestBytes),
		Size:   int64(len(manifestBytes)),
	}
	ranges := []content.SourceRange{
		{PartKey: "p1", Start: 0, End: len([]rune("één 🌌"))},
		{PartKey: "p2", Start: 0, End: len([]rune("second paragraph"))},
		{PartKey: "p3", Start: 0, End: len([]rune("第二段"))},
	}
	wantText := "één 🌌\n\nsecond paragraph\n\n第二段"
	located := func(segID string, blob content.Blob, srcRanges []content.SourceRange) content.Located {
		return content.Located{
			Hydrated: content.Hydrated{
				RecordID:   "rec-1",
				VersionID:  "ver-1",
				TextSHA256: content.Hash([]byte(wantText)),
				Segment: content.Segment{
					ID:      segID,
					PartKey: "p1",
					Start:   0,
					End:     len([]rune("één 🌌")),
					Derivation: content.SegmentDerivation{
						SourceRanges:    srcRanges,
						SourceSeparator: "\n\n",
					},
				},
			},
			ManifestBlob: blob,
		}
	}

	blobs := &fixtureBlobStore{data: map[string][]byte{manifestBlob.Key: manifestBytes}}
	repo := &fixtureHydrationRepo{
		located: map[string]content.Located{
			"seg-1": located("seg-1", manifestBlob, ranges),
			"seg-2": located("seg-2", manifestBlob, ranges),
		},
		withdrawn: map[string]bool{},
		deniedOrg: map[string]bool{},
	}
	scope := corpus.Scope{Organization: "org", Actions: []string{"content:read", "search:query"}, Corpora: []string{"*"}}
	svc := content.Service{Baseline: repo, Blobs: blobs, BlobCache: content.NewHydrationBlobCache(1 << 20)}

	got, err := svc.Hydrate(context.Background(), scope, []content.Candidate{
		{SegmentID: "seg-1", GenerationID: "g1"},
		{SegmentID: "seg-2", GenerationID: "g1"},
	})
	if err != nil || len(got) != 2 {
		t.Fatalf("manifest hydrate = %+v, %v", got, err)
	}
	if got[0].Segment.Text != wantText || len(got[0].Segment.SourceExcerpts) != 3 || got[0].Segment.SourceExcerpts[2].Text != "第二段" {
		t.Fatalf("hydrated segment = %+v; want %q with 3 source excerpts", got[0].Segment, wantText)
	}
	if blobs.reads.Load() != 1 {
		t.Fatalf("cold manifest hydration made %d blob reads across 2 segments, want 1", blobs.reads.Load())
	}
	if _, err := svc.Hydrate(context.Background(), scope, []content.Candidate{{SegmentID: "seg-1", GenerationID: "g1"}}); err != nil {
		t.Fatal(err)
	}
	if blobs.reads.Load() != 1 {
		t.Fatalf("warm manifest hydration made %d blob reads, want 1 (cached)", blobs.reads.Load())
	}

	for _, tc := range []struct {
		name   string
		raw    []byte
		range_ []content.SourceRange
	}{
		{name: "invalid json", raw: []byte("{not-json"), range_: ranges},
		{name: "missing part", raw: manifestBytes, range_: append(append([]content.SourceRange{}, ranges...), content.SourceRange{PartKey: "missing", Start: 0, End: 1})},
		{name: "out of bounds", raw: manifestBytes, range_: []content.SourceRange{{PartKey: "p1", Start: 0, End: 999}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			badBlob := content.Blob{Key: "org/sha256/" + tc.name, SHA256: content.Hash(tc.raw), Size: int64(len(tc.raw))}
			badBlobs := &fixtureBlobStore{data: map[string][]byte{badBlob.Key: tc.raw}}
			badRepo := &fixtureHydrationRepo{
				located:   map[string]content.Located{"seg-bad": located("seg-bad", badBlob, tc.range_)},
				withdrawn: map[string]bool{},
				deniedOrg: map[string]bool{},
			}
			badSvc := content.Service{Baseline: badRepo, Blobs: badBlobs}
			if _, err := badSvc.Hydrate(context.Background(), scope, []content.Candidate{{SegmentID: "seg-bad", GenerationID: "g1"}}); !errors.Is(err, content.ErrArtifactCorrupt) {
				t.Fatalf("%s: err = %v; want ErrArtifactCorrupt", tc.name, err)
			}
		})
	}

	t.Run("canceled context stops launching reads", func(t *testing.T) {
		const totalBlobs = 70
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var started atomic.Int64
		allSlotsFilled := make(chan struct{})
		blockingBlobs := blockingBlobReader{
			read: func(rctx context.Context, _ content.Blob) ([]byte, error) {
				if started.Add(1) == 64 {
					close(allSlotsFilled)
				}
				<-rctx.Done()
				return nil, rctx.Err()
			},
		}
		manyRepo := &fixtureHydrationRepo{
			located:   make(map[string]content.Located, totalBlobs),
			withdrawn: map[string]bool{},
			deniedOrg: map[string]bool{},
		}
		candidates := make([]content.Candidate, totalBlobs)
		for i := range totalBlobs {
			segID := fmt.Sprintf("seg-cancel-%d", i)
			candidates[i] = content.Candidate{SegmentID: segID, GenerationID: "g1"}
			b := content.Blob{Key: fmt.Sprintf("org/sha256/cancel-%d", i), SHA256: fmt.Sprintf("sha-%d", i), Size: 4}
			manyRepo.located[segID] = content.Located{
				Hydrated: content.Hydrated{
					RecordID:   fmt.Sprintf("rec-%d", i),
					VersionID:  fmt.Sprintf("ver-%d", i),
					TextSHA256: content.Hash([]byte("text")),
					Segment:    content.Segment{ID: segID, PartKey: "p1", Start: 0, End: 4},
				},
				Blob: b,
			}
		}

		go func() {
			<-allSlotsFilled
			cancel()
		}()
		cancelSvc := content.Service{Baseline: manyRepo, Blobs: blockingBlobs}
		if _, err := cancelSvc.Hydrate(ctx, scope, candidates); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled hydrate err = %v; want context.Canceled", err)
		}
		if got := started.Load(); got != 64 {
			t.Fatalf("canceled hydrate launched %d/%d blob reads, want 64 (queued reads stopped)", got, totalBlobs)
		}
	})
}

type blockingBlobReader struct {
	content.Blobs
	read func(context.Context, content.Blob) ([]byte, error)
}

func (b blockingBlobReader) Read(ctx context.Context, blob content.Blob) ([]byte, error) {
	return b.read(ctx, blob)
}

// One failing blob read fails its batch at once: the batch's other reads are
// cancelled instead of awaited, and none is left running after Hydrate
// returns, with or without a blob cache.
func TestHydrateFailFast(t *testing.T) {
	const blobCount, slow = 20, 300 * time.Millisecond
	errStore := errors.New("blob store unavailable")
	scope := corpus.Scope{Organization: "org", Actions: []string{"content:read", "search:query"}, Corpora: []string{"*"}}
	repo := &fixtureHydrationRepo{located: map[string]content.Located{}, withdrawn: map[string]bool{}, deniedOrg: map[string]bool{}}
	candidates := make([]content.Candidate, blobCount)
	for i := range blobCount {
		id := fmt.Sprintf("seg-%d", i)
		candidates[i] = content.Candidate{SegmentID: id, GenerationID: "g1"}
		repo.located[id] = content.Located{
			Hydrated: content.Hydrated{RecordID: "r", VersionID: "v", TextSHA256: content.Hash([]byte("text")), Segment: content.Segment{ID: id, PartKey: "body", End: 4}},
			Blob:     content.Blob{Key: fmt.Sprintf("org/sha256/%d", i), SHA256: content.Hash([]byte("text")), Size: 4},
		}
	}
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprintf("cached=%t", cached), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var running atomic.Int64
				reader := blockingBlobReader{read: func(ctx context.Context, b content.Blob) ([]byte, error) {
					if b.Key == "org/sha256/0" {
						return nil, errStore
					}
					running.Add(1)
					defer running.Add(-1)
					select {
					case <-time.After(slow):
						return []byte("text"), nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}}
				svc := content.Service{Baseline: repo, Blobs: reader}
				if cached {
					svc.BlobCache = content.NewHydrationBlobCache(1 << 20)
				}
				start := time.Now()
				_, err := svc.Hydrate(context.Background(), scope, candidates)
				elapsed := time.Since(start)
				synctest.Wait()
				if !errors.Is(err, errStore) || elapsed > 30*time.Millisecond {
					t.Fatalf("1 failing of %d blobs: err %v after %v; want %v within 30ms", blobCount, err, elapsed, errStore)
				}
				if n := running.Load(); n != 0 {
					t.Fatalf("%d blob reads still running after Hydrate returned", n)
				}
			})
		})
	}
}
