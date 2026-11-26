package weaviate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"golang.org/x/sync/errgroup"
)

// Opt-in measurement on a dedicated Weaviate 1.39.14 instance. Set
// QUIVR_TENANT_ISOLATION_PASSAGES=2000000 for the full isolation run and
// QUIVR_WEAVIATE_METRICS_URL to its Prometheus endpoint.
func TestTenantIsolationUnderProjection(t *testing.T) {
	raw := os.Getenv("QUIVR_TENANT_ISOLATION_PASSAGES")
	if raw == "" {
		t.Skip("opt-in tenant isolation measurement")
	}
	count, err := strconv.Atoi(raw)
	if err != nil || count < 1000 {
		t.Fatal("QUIVR_TENANT_ISOLATION_PASSAGES must be at least 1000")
	}
	metricsURL := os.Getenv("QUIVR_WEAVIATE_METRICS_URL")
	if metricsURL == "" {
		t.Fatal("QUIVR_WEAVIATE_METRICS_URL is required for the tombstone check")
	}
	httpStore, writer := realTenantStores(t)
	ctx, stop := context.WithTimeout(context.Background(), 45*time.Minute)
	defer stop()
	if err := httpStore.Bootstrap(ctx, InitialCollection); err != nil {
		t.Fatal(err)
	}
	org := fmt.Sprintf("tenant-isolation-%d", time.Now().UnixNano())
	g := content.Generation{ID: "default", Collection: InitialCollection, ItemKeywordsProjected: true, SpaceID: "fixture"}
	small := retrieval.Route{CorpusID: "small", Generation: g}
	large := retrieval.Route{CorpusID: "large", Generation: g}
	for _, r := range []retrieval.Route{small, large} {
		if err := httpStore.EnsureGeneration(ctx, g.Collection, org, r.CorpusID, g.ID); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, _ = httpStore.PurgeGeneration(cleanup, g.Collection, org, r.CorpusID, g.ID)
		})
	}
	needle := []float32{1, 0, 0, 0}
	projectTenantDocument(t, ctx, writer, org, small, "needle", "cobalt beacon", needle)
	largeCtx, err := tenantContext(ctx, g.Collection, org, large.CorpusID, g.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Load already-enriched unified segments through the production batch
	// transport to measure Weaviate contention without embedding model cost.
	// One immutable BM25 anchor accompanies every document of 100 passages.
	var published atomic.Int64
	progress := make(chan int64, 1)
	group, work := errgroup.WithContext(largeCtx)
	jobs := make(chan int)
	group.Go(func() error {
		defer close(jobs)
		for start := 0; start < count; start += projectionBatch {
			select {
			case jobs <- start:
			case <-work.Done():
				return work.Err()
			}
		}
		return nil
	})
	const importConcurrency = 6
	for range importConcurrency {
		group.Go(func() error {
			for start := range jobs {
				end := min(start+projectionBatch, count)
				id := strconv.Itoa(start)
				objects := make([]projectionObject, 0, end-start+1)
				v := content.Version{ID: id, RecordID: id}
				objects = append(objects, itemAnchor(g, org, large.CorpusID, "example", v, nil))
				for n := start; n < end; n++ {
					segment := strconv.Itoa(n)
					objects = append(objects, projectionObject{
						Class: g.Collection, ID: objectID(org, g.ID, segment),
						Properties: map[string]any{"organization": org, "corpusId": large.CorpusID, "generationId": g.ID,
							"segmentId": segment, "versionId": id, "recordId": id, itemKind: segmentKind, passageText: "concurrent import"},
						Vectors: map[string][]float32{legacyVector: {float32(n%17 + 1), float32(n%29 + 1), float32(n%41 + 1), 1}},
					})
				}
				if err := writer.insertBatch(work, objects); err != nil {
					return err
				}
				completed := published.Add(int64(end - start))
				select {
				case progress <- completed:
				default:
				}
			}
			return nil
		})
	}
	done := make(chan error, 1)
	go func() {
		done <- group.Wait()
		close(progress)
	}()
	q := retrieval.Request{Query: "beacon", Vector: needle, Mode: "hybrid", GroupBy: "record", K: 4}
	scope := corpus.Scope{Organization: org}
	var samples []time.Duration
	lastSample := int64(0)
	sampleInterval := int64(max(projectionBatch, count/240))
	for current := range progress {
		if current-lastSample < sampleInterval || published.Load() >= int64(count) {
			continue
		}
		lastSample = current
		start := time.Now()
		rows, err := writer.Search(work, []retrieval.Route{small}, scope, q)
		if err != nil {
			if work.Err() != nil {
				break
			}
			stop()
			<-done
			t.Fatalf("small query during projection after %d passages: %v", current, err)
		}
		if len(rows) != 1 || rows[0].SegmentID != "needle" {
			stop()
			<-done
			t.Fatalf("small query after %d passages: %+v", current, rows)
		}
		samples = append(samples, time.Since(start))
	}
	if err := <-done; err != nil || published.Load() != int64(count) {
		t.Fatalf("concurrent projection published %d/%d passages: %v", published.Load(), count, err)
	}
	if len(samples) == 0 {
		t.Fatal("no successful query overlapped projection")
	}
	slices.Sort(samples)
	p95 := samples[(95*len(samples)+99)/100-1]
	t.Logf("small-corpus p95 %v across %d searches during %d-passage projection", p95, len(samples), count)
	if p95 >= 200*time.Millisecond {
		t.Errorf("small-corpus p95 %v exceeded 200ms", p95)
	}

	shard := tenantFrom(largeCtx)
	tombstonesBefore := tenantTombstones(t, ctx, metricsURL, shard)
	if tombstonesBefore != "0" {
		t.Fatalf("expected tenant shard baseline of zero tombstones, got %q", tombstonesBefore)
	}
	start := time.Now()
	result, err := httpStore.PurgeGeneration(ctx, g.Collection, org, large.CorpusID, g.ID)
	elapsed := time.Since(start)
	if err != nil || !result.Complete || result.Deleted != 0 || elapsed >= time.Second {
		t.Fatalf("purge of %d passages took %v: %+v (%v)", count, elapsed, result, err)
	}
	if exists, err := httpStore.tenantExists(largeCtx, g.Collection); err != nil || exists {
		t.Fatalf("tenant remained after purge: %v (%v)", exists, err)
	}
	if got := tenantTombstones(t, ctx, metricsURL, shard); got != "" && got != "0" {
		t.Fatalf("purge produced tombstones: %q", got)
	}
	t.Logf("purge of %d passages acknowledged in %v with zero shard tombstones", count, elapsed)
}

func tenantTombstones(t *testing.T, ctx context.Context, endpoint, shard string) string {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := http.Client{Timeout: 5 * time.Second}
	reply, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer reply.Body.Close()
	data, err := io.ReadAll(io.LimitReader(reply.Body, 8<<20))
	if err != nil || reply.StatusCode != http.StatusOK {
		t.Fatalf("metrics status %d: %v", reply.StatusCode, err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.HasPrefix(line, "tombstone_delete_list_size{") && strings.Contains(line, `shard_name="`+shard+`"`) {
			_, gauge, _ := strings.Cut(line, "} ")
			return gauge
		}
	}
	return ""
}
