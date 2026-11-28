package content_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

// slowBlobReader serves text after latency on the bubble's fake clock, or
// fails with the reader's context, counting reads and context cancellations.
type slowBlobReader struct {
	content.Blobs
	text             string
	latency          time.Duration
	reads, cancelled atomic.Int64
}

func (r *slowBlobReader) Read(ctx context.Context, _ content.Blob) ([]byte, error) {
	r.reads.Add(1)
	select {
	case <-time.After(r.latency):
		return []byte(r.text), nil
	case <-ctx.Done():
		r.cancelled.Add(1)
		return nil, ctx.Err()
	}
}

// Concurrent cold hydrations of one blob share a single canonical read whose
// lifetime belongs to its waiters, not to the caller that started it: the
// starter leaving does not fail the others, and the read is cancelled only
// once every waiter has left.
func TestHydrateColdStampede(t *testing.T) {
	const callers = 32
	const text = "één 🌌 第二段 shared cold blob"
	blob := content.Blob{Key: "org/sha256/cold", SHA256: content.Hash([]byte(text)), Size: int64(len(text))}
	scope := corpus.Scope{Organization: "org", Actions: []string{"content:read", "search:query"}, Corpora: []string{"*"}}
	candidates := []content.Candidate{{SegmentID: "cold", GenerationID: "g1"}}
	newService := func() (content.Service, *slowBlobReader) {
		reader := &slowBlobReader{text: text, latency: 20 * time.Millisecond}
		repo := &fixtureHydrationRepo{located: map[string]content.Located{"cold": {
			Hydrated: content.Hydrated{RecordID: "r", VersionID: "v", TextSHA256: content.Hash([]byte(text)), Segment: content.Segment{ID: "cold", PartKey: "body", End: len([]rune(text))}},
			Blob:     blob,
		}}, withdrawn: map[string]bool{}, deniedOrg: map[string]bool{}}
		return content.Service{Baseline: repo, Blobs: reader, BlobCache: content.NewHydrationBlobCache(1 << 20)}, reader
	}
	type outcome struct {
		caller int
		text   string
		err    error
	}
	// stampede starts the first caller, lets it reach the read, then starts
	// the rest, so caller 0 is the one that began the shared read.
	stampede := func(svc content.Service, contexts []context.Context) <-chan outcome {
		out := make(chan outcome, len(contexts))
		hydrate := func(i int) {
			got, err := svc.Hydrate(contexts[i], scope, candidates)
			out <- outcome{caller: i, text: got[0].Segment.Text, err: err}
		}
		go hydrate(0)
		synctest.Wait()
		for i := 1; i < len(contexts); i++ {
			go hydrate(i)
		}
		synctest.Wait()
		return out
	}
	collect := func(out <-chan outcome) ([]error, []string) {
		errs, texts := make([]error, callers), make([]string, callers)
		for range callers {
			o := <-out
			errs[o.caller], texts[o.caller] = o.err, o.text
		}
		return errs, texts
	}

	t.Run("starter leaving does not fail followers", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			svc, reader := newService()
			contexts := make([]context.Context, callers)
			starter, leave := context.WithCancel(context.Background())
			contexts[0] = starter
			for i := 1; i < callers; i++ {
				contexts[i] = context.Background()
			}
			out := stampede(svc, contexts)
			leave()
			errs, texts := collect(out)
			if got := reader.reads.Load(); got != 1 {
				t.Fatalf("%d concurrent cold hydrations of one blob made %d canonical reads, want 1", callers, got)
			}
			if !errors.Is(errs[0], context.Canceled) {
				t.Fatalf("starter err = %v; want context.Canceled", errs[0])
			}
			for i := 1; i < callers; i++ {
				if errs[i] != nil || texts[i] != text {
					t.Fatalf("follower %d after the starter left: text %q, err %v; want %q", i, texts[i], errs[i], text)
				}
			}
		})
	})

	t.Run("read is cancelled only when every waiter leaves", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			svc, reader := newService()
			contexts := make([]context.Context, callers)
			cancels := make([]context.CancelFunc, callers)
			for i := range contexts {
				contexts[i], cancels[i] = context.WithCancel(context.Background())
				defer cancels[i]()
			}
			start := time.Now()
			out := stampede(svc, contexts)
			for _, cancel := range cancels[:callers-1] {
				cancel()
			}
			synctest.Wait()
			if got := reader.cancelled.Load(); got != 0 {
				t.Fatalf("read cancelled while a waiter remained: %d cancellations", got)
			}
			cancels[callers-1]()
			errs, _ := collect(out)
			synctest.Wait()
			if reads, cancelled := reader.reads.Load(), reader.cancelled.Load(); cancelled != reads || time.Since(start) != 0 {
				t.Fatalf("after every waiter left: %d reads, %d cancelled, %v elapsed; want every read cancelled at once", reads, cancelled, time.Since(start))
			}
			for i, err := range errs {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("waiter %d err = %v; want context.Canceled", i, err)
				}
			}
			// An abandoned read is forgotten: the next caller reads afresh.
			got, err := svc.Hydrate(context.Background(), scope, candidates)
			if err != nil || got[0].Segment.Text != text {
				t.Fatalf("hydration after an abandoned read = %+v, %v; want %q", got, err, text)
			}
		})
	})
}
