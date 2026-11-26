package content_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

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
}

func (r *fixtureHydrationRepo) Hydrate(_ context.Context, scope corpus.Scope, cs []content.Candidate) (map[int]content.Located, error) {
	r.lookups.Add(1)
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[int]content.Located{}
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
// LRU cache with zero additional blob reads, enforces strict (organization, key, sha256)
// isolation, never caches mismatched digests, and never bypasses Postgres Hydrate or
// RecheckHydration authorization/tombstone fences.
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

	// Warm repeat: zero blob reads, but canonical lookup + recheck still run.
	got, err = svc.Hydrate(context.Background(), orgA, []content.Candidate{{SegmentID: "packed"}})
	if err != nil || got[0].Segment.Text != joined {
		t.Fatalf("warm packed hydration = %+v, %v", got, err)
	}
	if blobs.reads.Load() != 2 || repo.lookups.Load() != 2 || repo.rechecks.Load() != 2 {
		t.Fatalf("warm counts: reads=%d lookups=%d rechecks=%d; want 2, 2, 2", blobs.reads.Load(), repo.lookups.Load(), repo.rechecks.Load())
	}

	// Cross-organization isolation: org-b with the same blob keys & digests must miss.
	got, err = svc.Hydrate(context.Background(), orgB, []content.Candidate{{SegmentID: "packed"}})
	if err != nil || got[0].Segment.Text != joined {
		t.Fatalf("org-b hydration = %+v, %v", got, err)
	}
	if blobs.reads.Load() != 4 {
		t.Fatalf("org-b shared org-a cache entry: reads=%d, want 4", blobs.reads.Load())
	}

	// SHA-256 isolation: same org and blob key with a different digest must miss.
	blobs.mu.Lock()
	blobs.data[blobAlt.Key] = []byte(partAlt)
	blobs.mu.Unlock()
	got, err = svc.Hydrate(context.Background(), orgA, []content.Candidate{{SegmentID: "alt-sha"}})
	if err != nil || got[0].Segment.Text != partAlt {
		t.Fatalf("alt-sha hydration = %+v, %v", got, err)
	}
	if blobs.reads.Load() != 5 {
		t.Fatalf("different sha256 collided in cache: reads=%d, want 5", blobs.reads.Load())
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
