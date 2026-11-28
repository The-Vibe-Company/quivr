package retrieval

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
)

type testQueryEmbedder struct {
	run func(context.Context, string) ([]float32, error)
}

func (e testQueryEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	return e.run(ctx, text)
}

func (testQueryEmbedder) Space() content.VectorSpace { return content.VectorSpace{ID: "test-space"} }

func TestQueryEncodingCoalescesWithIndependentCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		calls := make(chan string, 2)
		service := Service{QueryVectors: NewQueryVectorCache(), Embedder: testQueryEmbedder{
			run: func(ctx context.Context, text string) ([]float32, error) {
				calls <- text
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-release:
					return []float32{1, 2}, nil
				}
			},
		}}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		leader := make(chan error, 1)
		go func() {
			_, err := service.EncodeQuery(ctx, "org", "test-space", "hello")
			leader <- err
		}()
		if text := <-calls; text != "query: hello" {
			t.Fatalf("encoder input = %q, want query: hello", text)
		}
		waiter := make(chan error, 1)
		go func() {
			vector, err := service.EncodeQuery(t.Context(), "org", "test-space", "hello")
			if err == nil {
				vector[0] = -1
			}
			waiter <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-leader; !errors.Is(err, ErrUnavailable) {
			t.Errorf("canceled caller error = %v, want %v", err, ErrUnavailable)
		}
		close(release)
		if err := <-waiter; err != nil {
			t.Fatalf("remaining caller error = %v", err)
		}
		vector, err := service.EncodeQuery(t.Context(), "org", "test-space", "hello")
		if err != nil || len(vector) != 2 || vector[0] != 1 {
			t.Fatalf("hot vector = %v, error = %v, want [1 2]", vector, err)
		}
		if len(calls) != 0 {
			t.Fatalf("encoder called %d extra times for the same key", len(calls))
		}
	})
}

func TestQueryVectorCacheEviction(t *testing.T) {
	for _, tc := range []struct {
		name       string
		maxEntries int
		maxBytes   int
	}{
		{name: "entry limit", maxEntries: 2, maxBytes: 1 << 20},
		{name: "byte limit", maxEntries: 100, maxBytes: 2*(queryVectorEntryOverhead+1+4*16) + 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := newQueryVectorCache(tc.maxEntries, tc.maxBytes)
			calls := 0
			for _, step := range []struct {
				key       string
				wantCalls int
			}{
				{"a", 1}, {"b", 2}, {"a", 2}, {"c", 3}, {"a", 3}, {"b", 4},
			} {
				_, err := cache.encode(t.Context(), step.key, func(context.Context) ([]float32, error) {
					calls++
					return make([]float32, 16), nil
				})
				if err != nil || calls != step.wantCalls {
					t.Fatalf("lookup %q made %d encodes, error = %v, want %d encodes", step.key, calls, err, step.wantCalls)
				}
			}
			var metrics bytes.Buffer
			cache.WriteMetrics(&metrics)
			for _, want := range []string{
				"quivr_query_vector_cache_lookups_total{result=\"hit\"} 2\n",
				"quivr_query_vector_cache_lookups_total{result=\"miss\"} 4\n",
				"quivr_query_vector_cache_evictions_total 2\n",
			} {
				if !strings.Contains(metrics.String(), want) {
					t.Errorf("missing metric %q in:\n%s", want, &metrics)
				}
			}
		})
	}
}

func TestQueryVectorCacheSkipsFailuresAndOversizedVectors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := newQueryVectorCache(10, 512)
		ctx := t.Context()
		calls := 0
		encode := func(key string, dimension int, err, wantErr error, wantCalls int) {
			t.Helper()
			vector, gotErr := cache.encode(ctx, key, func(context.Context) ([]float32, error) {
				calls++
				return make([]float32, dimension), err
			})
			if !errors.Is(gotErr, wantErr) || (wantErr == nil && len(vector) != dimension) || calls != wantCalls {
				t.Fatalf("lookup %q returned %d dimensions, error = %v after %d encodes, want %d dimensions, error = %v after %d encodes", key, len(vector), gotErr, calls, dimension, wantErr, wantCalls)
			}
		}
		encode("small", 0, ErrUnavailable, ErrUnavailable, 1)
		// The outage is remembered for its window instead of re-encoding.
		encode("small", 16, nil, ErrUnavailable, 1)
		time.Sleep(queryVectorFailureTTL * 2)
		encode("small", 16, nil, nil, 2)
		encode("large", 1024, nil, nil, 3)
		encode("large", 1024, nil, nil, 4)
		encode("small", 16, nil, nil, 4)
	})
}

type failingPinnedSpaceEncoder struct {
	ownsCalls int
	resolve   func(context.Context, string) (QueryEncoder, string, error)
}

func (f *failingPinnedSpaceEncoder) Owns(string) bool {
	f.ownsCalls++
	return true
}

func (f *failingPinnedSpaceEncoder) EncodeQuery(context.Context, string, string, string) ([]float32, error) {
	return nil, errors.New("unexpected direct encode")
}

func (f *failingPinnedSpaceEncoder) ResolveQueryEncoder(ctx context.Context, space string) (QueryEncoder, string, error) {
	return f.resolve(ctx, space)
}

func TestQueryVectorCacheCancelsFlightWhenAllWaitersLeave(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		canceled := make(chan struct{}, 1)
		service := Service{QueryVectors: NewQueryVectorCache(), Embedder: testQueryEmbedder{
			run: func(ctx context.Context, _ string) ([]float32, error) {
				<-ctx.Done()
				canceled <- struct{}{}
				return nil, ctx.Err()
			},
		}}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			_, err := service.EncodeQuery(ctx, "org", "test-space", "stalled")
			done <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-done; !errors.Is(err, ErrUnavailable) {
			t.Fatalf("caller error = %v, want ErrUnavailable", err)
		}
		synctest.Wait()
		select {
		case <-canceled:
		default:
			t.Fatal("in-flight encode was not canceled when the last waiter left")
		}
	})
}

func TestResolveQueryEncoderErrorReturnsUnavailableWithoutCallingOwns(t *testing.T) {
	enc := &failingPinnedSpaceEncoder{
		resolve: func(context.Context, string) (QueryEncoder, string, error) {
			return nil, "", errors.New("db unavailable")
		},
	}
	service := Service{QueryVectors: NewQueryVectorCache(), Spaces: enc}
	_, err := service.EncodeQuery(t.Context(), "org", "plugin-space", "hello")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if enc.ownsCalls != 0 {
		t.Fatalf("Owns() called %d times on resolver error; want 0", enc.ownsCalls)
	}
}

func BenchmarkEncodeQuery(b *testing.B) {
	service := Service{QueryVectors: NewQueryVectorCache(), Embedder: testQueryEmbedder{
		run: func(context.Context, string) ([]float32, error) { return make([]float32, 768), nil },
	}}
	ctx := b.Context()
	if _, err := service.EncodeQuery(ctx, "org", "test-space", "how to index a record"); err != nil {
		b.Fatal(err)
	}
	b.Run("hot", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := service.EncodeQuery(ctx, "org", "test-space", "how to index a record"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("cold", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; b.Loop(); i++ {
			if _, err := service.EncodeQuery(ctx, "org", "test-space", fmt.Sprintf("how to index record %d", i)); err != nil {
				b.Fatal(err)
			}
		}
	})
}

const queryVectorPanicChild = "QUIVR_QUERY_VECTOR_PANIC_CHILD"

// A panicking encoder runs in the flight goroutine, where an unrecovered panic
// ends the whole API process. The child process proves the panic is contained.
func TestQueryVectorCachePanickingEncoderFailsWaiters(t *testing.T) {
	if os.Getenv(queryVectorPanicChild) == "1" {
		runQueryVectorPanicChild(t)
		return
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestQueryVectorCachePanickingEncoderFailsWaiters$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), queryVectorPanicChild+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child process exited with %v, want exit 0:\n%s", err, out)
	}
	for _, want := range []string{"--- PASS: TestQueryVectorCachePanickingEncoderFailsWaiters", "query encoder panicked"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Fatalf("child output lacks %q:\n%s", want, out)
		}
	}
	if bytes.Contains(out, []byte("secret query text")) {
		t.Fatalf("child logged the panic value:\n%s", out)
	}
}

func runQueryVectorPanicChild(t *testing.T) {
	const waiters = 64
	release := make(chan struct{})
	var calls atomic.Int32
	service := Service{QueryVectors: NewQueryVectorCache(), Embedder: testQueryEmbedder{
		run: func(_ context.Context, text string) ([]float32, error) {
			calls.Add(1)
			if text == "query: healthy" {
				return []float32{1}, nil
			}
			<-release
			panic("encoder bug on secret query text")
		},
	}}
	cache := service.QueryVectors
	key := queryVectorKey("engine", "org", "test-space", "boom")
	type result struct {
		err error
		at  time.Time
	}
	results := make(chan result, waiters)
	for range waiters {
		go func() {
			_, err := service.EncodeQuery(t.Context(), "org", "test-space", "boom")
			results <- result{err, time.Now()}
		}()
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		cache.mu.Lock()
		joined := 0
		if f := cache.flights[key]; f != nil {
			joined = f.waiters
		}
		cache.mu.Unlock()
		if joined == waiters {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d waiters joined the flight", joined, waiters)
		}
	}
	panicked := time.Now()
	close(release)
	for range waiters {
		r := <-results
		if !errors.Is(r.err, ErrUnavailable) {
			t.Fatalf("waiter error = %v, want ErrUnavailable", r.err)
		}
		if elapsed := r.at.Sub(panicked); elapsed > 50*time.Millisecond {
			t.Fatalf("waiter returned %v after the panic, want <= 50ms", elapsed)
		}
	}
	cache.mu.Lock()
	flights := len(cache.flights)
	cache.mu.Unlock()
	if flights != 0 {
		t.Fatalf("%d flights left after the panic, want 0", flights)
	}
	if _, err := service.EncodeQuery(t.Context(), "org", "test-space", "boom"); !errors.Is(err, ErrUnavailable) || calls.Load() != 1 {
		t.Fatalf("retry error = %v after %d encodes, want remembered ErrUnavailable after 1", err, calls.Load())
	}
	if vector, err := service.EncodeQuery(t.Context(), "org", "test-space", "healthy"); err != nil || len(vector) != 1 {
		t.Fatalf("healthy lookup after the panic = %v, %v", vector, err)
	}
}

func TestQueryVectorCacheRemembersOutagesPerKey(t *testing.T) {
	for _, latency := range []time.Duration{0, 20 * time.Millisecond} {
		t.Run(fmt.Sprintf("latency %v", latency), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const requests, keys = 320, 4
				outage := time.Now().Add(200 * time.Millisecond)
				var mu sync.Mutex
				calls := map[string]int{}
				service := Service{QueryVectors: NewQueryVectorCache(), Embedder: testQueryEmbedder{
					run: func(_ context.Context, text string) ([]float32, error) {
						mu.Lock()
						calls[text]++
						mu.Unlock()
						time.Sleep(latency)
						if time.Now().Before(outage) {
							return nil, errors.New("encoder down")
						}
						return []float32{1, 2}, nil
					},
				}}
				var wg sync.WaitGroup
				for i := range requests {
					wg.Go(func() {
						if _, err := service.EncodeQuery(t.Context(), "org", "test-space", fmt.Sprint("q", i%keys)); !errors.Is(err, ErrUnavailable) {
							t.Errorf("lookup during outage error = %v, want ErrUnavailable", err)
						}
					})
					time.Sleep(200 * time.Millisecond / requests)
				}
				wg.Wait()
				for i := range keys {
					if got := calls[fmt.Sprint("query: q", i)]; got != 1 {
						t.Errorf("key q%d encoded %d times during the outage, want 1", i, got)
					}
				}
				// The window, jitter included, ends within 2 s; the key then recovers.
				time.Sleep(2 * time.Second)
				for i := range keys {
					if vector, err := service.EncodeQuery(t.Context(), "org", "test-space", fmt.Sprint("q", i)); err != nil || len(vector) != 2 {
						t.Fatalf("lookup after the outage = %v, %v", vector, err)
					}
				}
				if got := calls["query: q0"]; got != 2 {
					t.Fatalf("key q0 encoded %d times, want 2", got)
				}
			})
		})
	}
}

func TestQueryVectorCacheFailureTTLIsJittered(t *testing.T) {
	low := time.Duration(float64(queryVectorFailureTTL) * (1 - queryVectorFailureJitter))
	high := time.Duration(float64(queryVectorFailureTTL) * (1 + queryVectorFailureJitter))
	if high > 2*time.Second {
		t.Fatalf("failure TTL reaches %v, want <= 2s", high)
	}
	lowest, highest := high, low
	for range 10000 {
		ttl := queryFailureTTL()
		if ttl < low || ttl > high {
			t.Fatalf("failure TTL %v outside [%v, %v]", ttl, low, high)
		}
		lowest, highest = min(lowest, ttl), max(highest, ttl)
	}
	if spread := high - low; highest-lowest < spread*9/10 {
		t.Fatalf("failure TTLs span [%v, %v], want close to [%v, %v]", lowest, highest, low, high)
	}
}

func TestQueryVectorCacheDoesNotRememberQueryErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"query too long", publicerr.WithDetail(ErrQueryTooLong, "query exceeds 128 tokens")},
		{"invalid encoder answer", ErrUnsupported},
		{"other error", errors.New("not an outage")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := NewQueryVectorCache()
			calls := 0
			for want := 1; want <= 3; want++ {
				_, err := cache.encode(t.Context(), "key", func(context.Context) ([]float32, error) {
					calls++
					return nil, tc.err
				})
				if !errors.Is(err, tc.err) || calls != want {
					t.Fatalf("lookup %d error = %v after %d encodes, want %v after %d", want, err, calls, tc.err, want)
				}
			}
			if len(cache.failures) != 0 {
				t.Fatalf("remembered %d failures, want 0", len(cache.failures))
			}
		})
	}
}

func TestQueryVectorCacheBoundsRememberedOutages(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := NewQueryVectorCache()
		fail := func(context.Context) ([]float32, error) { return nil, ErrUnavailable }
		for i := range 2 * queryVectorFailures {
			if _, err := cache.encode(t.Context(), fmt.Sprint("key", i), fail); !errors.Is(err, ErrUnavailable) {
				t.Fatal(err)
			}
		}
		if len(cache.failures) != queryVectorFailures {
			t.Fatalf("remembered %d failures, want the %d bound", len(cache.failures), queryVectorFailures)
		}
		time.Sleep(2 * time.Second)
		if _, err := cache.encode(t.Context(), "fresh", fail); !errors.Is(err, ErrUnavailable) {
			t.Fatal(err)
		}
		if _, ok := cache.failures["fresh"]; !ok || len(cache.failures) != 1 {
			t.Fatalf("expired failures were not dropped for a fresh outage: %d remembered", len(cache.failures))
		}
	})
}

func TestQueryVectorCacheDoesNotRememberAbandonedFlights(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := NewQueryVectorCache()
		calls := 0
		stall := func(ctx context.Context) ([]float32, error) {
			calls++
			<-ctx.Done()
			return nil, ErrUnavailable
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			_, err := cache.encode(ctx, "key", stall)
			done <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-done; !errors.Is(err, ErrUnavailable) {
			t.Fatalf("canceled lookup error = %v, want ErrUnavailable", err)
		}
		synctest.Wait()
		vector, err := cache.encode(t.Context(), "key", func(context.Context) ([]float32, error) {
			calls++
			return []float32{1}, nil
		})
		if err != nil || len(vector) != 1 || calls != 2 {
			t.Fatalf("lookup after an abandoned flight = %v, %v after %d encodes, want a fresh encode", vector, err, calls)
		}
	})
}

func TestQueryVectorCacheMemoryBound(t *testing.T) {
	cache := NewQueryVectorCache()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := range 100_000 {
		key := queryVectorKey("engine", "org", "test-space", "distinct query "+strconv.Itoa(i))
		if _, err := cache.encode(t.Context(), key, func(context.Context) ([]float32, error) {
			return make([]float32, benchQueryDimensions), nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	cache.mu.Lock()
	entries, accounted := cache.lru.Len(), cache.bytes
	cache.mu.Unlock()
	mapped := 0
	cache.entries.Range(func(any, any) bool { mapped++; return true })
	if entries > queryVectorEntries || mapped != entries || accounted > queryVectorBytes {
		t.Fatalf("cache holds %d listed and %d mapped entries in %d accounted bytes, want <= %d entries and %d bytes", entries, mapped, accounted, queryVectorEntries, queryVectorBytes)
	}
	if grown := int64(after.HeapAlloc) - int64(before.HeapAlloc); grown > queryVectorBytes {
		t.Fatalf("heap grew %d bytes for %d entries, want <= %d", grown, entries, queryVectorBytes)
	}
	runtime.KeepAlive(cache)
}

func TestQueryVectorCacheCancelledLookupsReleaseFlights(t *testing.T) {
	const batches, batch = 10, 1000 // Stay under the race detector's goroutine limit.
	cache := NewQueryVectorCache()
	stall := func(ctx context.Context) ([]float32, error) {
		<-ctx.Done()
		return nil, ErrUnavailable
	}
	baseline := runtime.NumGoroutine()
	for b := range batches {
		ctx, cancel := context.WithCancel(t.Context())
		var wg sync.WaitGroup
		for i := range batch {
			wg.Go(func() {
				if _, err := cache.encode(ctx, fmt.Sprint(b, "/", i), stall); !errors.Is(err, ErrUnavailable) {
					t.Errorf("canceled lookup error = %v, want ErrUnavailable", err)
				}
			})
		}
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
			cache.mu.Lock()
			flights := len(cache.flights)
			cache.mu.Unlock()
			if flights == batch {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%d of %d flights started", flights, batch)
			}
		}
		cancel()
		wg.Wait()
	}
	var delta int
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		if delta = runtime.NumGoroutine() - baseline; delta <= 2 || time.Now().After(deadline) {
			break
		}
	}
	cache.mu.Lock()
	flights, failures := len(cache.flights), len(cache.failures)
	cache.mu.Unlock()
	if delta > 2 || flights != 0 || failures != 0 {
		t.Fatalf("after %d canceled lookups: %d extra goroutines, %d flights, %d remembered failures; want <= 2, 0, 0", batches*batch, delta, flights, failures)
	}
}

// Hits race with evictions; every lookup must still return its own key's vector.
func TestQueryVectorCacheConcurrentHitsAndEvictions(t *testing.T) {
	cache := newQueryVectorCache(8, 1<<20)
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Go(func() {
			for i := range 2000 {
				k := (g*31 + i) % 24
				vector, err := cache.encode(t.Context(), strconv.Itoa(k), func(context.Context) ([]float32, error) {
					return []float32{float32(k), float32(k)}, nil
				})
				if err != nil || len(vector) != 2 || vector[0] != float32(k) {
					t.Errorf("key %d returned %v, %v", k, vector, err)
					return
				}
				vector[1] = -1 // Callers own their copy.
			}
		})
	}
	wg.Wait()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	mapped := 0
	cache.entries.Range(func(_, value any) bool {
		mapped++
		if entry := value.(*queryVectorEntry); entry.vector[1] != entry.vector[0] {
			t.Errorf("cached vector %v was mutated through a hit", entry.vector)
		}
		return true
	})
	if cache.lru.Len() > 8 || mapped != cache.lru.Len() {
		t.Fatalf("cache lists %d and maps %d entries, want equal and <= 8", cache.lru.Len(), mapped)
	}
}

func TestQueryVectorCacheCountsBatchedLookups(t *testing.T) {
	cache := NewQueryVectorCache()
	encode := func(context.Context) ([]float32, error) { return []float32{1}, nil }
	if _, err := cache.encode(t.Context(), "key", encode); err != nil {
		t.Fatal(err)
	}
	const goroutines, hits = 8, 1000
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for range hits {
				if _, err := cache.encode(t.Context(), "key", encode); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	var metrics bytes.Buffer
	cache.WriteMetrics(&metrics)
	for _, want := range []string{
		fmt.Sprintf("quivr_query_vector_cache_lookups_total{result=\"hit\"} %d\n", goroutines*hits),
		"quivr_query_vector_cache_lookups_total{result=\"miss\"} 1\n",
	} {
		if !strings.Contains(metrics.String(), want) {
			t.Errorf("missing metric %q in:\n%s", want, &metrics)
		}
	}
}
