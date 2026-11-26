package retrieval

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/The-Vibe-Company/quivr/internal/content"
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
		{name: "byte limit", maxEntries: 100, maxBytes: 600},
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
	cache := newQueryVectorCache(10, 512)
	ctx := t.Context()
	calls := 0
	encode := func(key string, dimension int, err error) {
		t.Helper()
		vector, gotErr := cache.encode(ctx, key, func(context.Context) ([]float32, error) {
			calls++
			return make([]float32, dimension), err
		})
		if !errors.Is(gotErr, err) || (err == nil && len(vector) != dimension) {
			t.Fatalf("lookup %q returned %d dimensions, error = %v, want %d dimensions, error = %v", key, len(vector), gotErr, dimension, err)
		}
	}
	encode("small", 0, ErrUnavailable)
	encode("small", 16, nil)
	encode("large", 1024, nil)
	encode("large", 1024, nil)
	encode("small", 16, nil)
	if calls != 4 {
		t.Fatalf("encoder calls = %d, want 4 including two oversized encodings and a failed attempt", calls)
	}
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
