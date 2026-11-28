package retrieval

import (
	"container/list"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/telemetry"
)

const (
	queryVectorEntries = 4096
	queryVectorBytes   = 12 << 20
	// Account for the list node, entry, and its share of the map in addition
	// to the variable-size key and vector payload. TestQueryVectorCacheMemoryBound
	// checks the estimate against the live heap.
	queryVectorEntryOverhead = 384
	// The plugin protocol caps embed_query at 10 s. This also bounds shared
	// work after every caller has left.
	queryVectorTimeout = 10 * time.Second

	// An encoder outage is remembered per key for queryVectorFailureTTL, give
	// or take queryVectorFailureJitter, so concurrent and retried searches do
	// not hammer an encoder that just failed. Jitter spreads the retries of
	// keys that failed together.
	queryVectorFailureTTL    = time.Second
	queryVectorFailureJitter = 0.2
	// Bound remembered outages independently of the vector budget.
	queryVectorFailures     = 1024
	queryVectorFailureBytes = 1 << 20

	// Lookups are counted in striped atomics and folded into the metric family
	// every queryVectorCountBatch lookups per stripe, on each scrape, and on a
	// cold lookup at least queryVectorCountFlush after the previous fold. The
	// family serializes its writers, which would otherwise bound concurrent
	// hits.
	queryVectorCountStripes = 16
	queryVectorCountBatch   = 64
	queryVectorCountFlush   = time.Second
)

// lookupCount batches one series of the lookups family.
type lookupCount struct {
	result  string
	stripes [queryVectorCountStripes]struct {
		n atomic.Int64
		// Keep each stripe on its own cache line.
		_ [56]byte
	}
}

type queryFlight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	// abandoned is set once every waiter left, so its cancellation is not
	// remembered as an encoder outage.
	abandoned bool
	vector    []float32
	err       error
}

type queryFailure struct {
	until time.Time
	err   error
}

// QueryVectorCache keeps completed encodings for one service's encoders.
// Its pointer is shared across the copies of Service used to serve searches.
//
// Hits read entries without taking mu: completed vectors are immutable and
// each hit clones its own copy. Eviction is second-chance LRU: a hit only sets
// the entry's referenced bit, and eviction moves a referenced entry back to the
// front once instead of dropping it.
type QueryVectorCache struct {
	maxEntries, maxBytes int
	lookups, evictions   *telemetry.Family
	hits, misses         lookupCount
	counted              atomic.Int64
	// entries maps a key to its *queryVectorEntry. Only holders of mu write it.
	entries      sync.Map
	mu           sync.Mutex
	flights      map[string]*queryFlight
	failures     map[string]queryFailure
	failureBytes int
	lru          list.List
	bytes        int
}

type queryVectorEntry struct {
	key        string
	vector     []float32
	bytes      int
	referenced atomic.Bool
}

func NewQueryVectorCache() *QueryVectorCache {
	return newQueryVectorCache(queryVectorEntries, queryVectorBytes)
}

func newQueryVectorCache(maxEntries, maxBytes int) *QueryVectorCache {
	c := &QueryVectorCache{
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		lookups:    telemetry.NewFamily("quivr_query_vector_cache_lookups_total", "Query vector cache lookups by outcome.", []string{"result"}, nil, 2),
		evictions:  telemetry.NewFamily("quivr_query_vector_cache_evictions_total", "Query vectors evicted by the cache limits.", nil, nil, 1),
		flights:    make(map[string]*queryFlight),
		failures:   make(map[string]queryFailure),
	}
	c.hits.result, c.misses.result = "hit", "miss"
	c.lookups.Add(0, "hit")
	c.lookups.Add(0, "miss")
	c.counted.Store(time.Now().UnixNano())
	return c
}

func (c *QueryVectorCache) count(l *lookupCount) {
	stripe := &l.stripes[rand.Uint32()%queryVectorCountStripes]
	if stripe.n.Add(1) >= queryVectorCountBatch {
		c.lookups.Add(stripe.n.Swap(0), l.result)
	}
}

// flushCounts folds every pending lookup into the metric family.
func (c *QueryVectorCache) flushCounts(now time.Time) {
	c.counted.Store(now.UnixNano())
	for _, l := range []*lookupCount{&c.hits, &c.misses} {
		n := int64(0)
		for i := range l.stripes {
			n += l.stripes[i].n.Swap(0)
		}
		if n > 0 {
			c.lookups.Add(n, l.result)
		}
	}
}

// WriteMetrics writes the cache counters in Prometheus text format.
func (c *QueryVectorCache) WriteMetrics(w io.Writer) {
	c.flushCounts(time.Now())
	c.lookups.Write(w)
	c.evictions.Write(w)
}

func queryVectorKey(parts ...string) string {
	var size [binary.MaxVarintLen64]byte
	n := 0
	for _, part := range parts {
		n += binary.PutUvarint(size[:], uint64(len(part))) + len(part)
	}
	var key strings.Builder
	key.Grow(n)
	for _, part := range parts {
		m := binary.PutUvarint(size[:], uint64(len(part)))
		key.Write(size[:m])
		key.WriteString(part)
	}
	return key.String()
}

// queryFailureTTL returns how long one outage is remembered: the base TTL with
// uniform jitter of ±queryVectorFailureJitter.
func queryFailureTTL() time.Duration {
	scale := 1 - queryVectorFailureJitter + 2*queryVectorFailureJitter*rand.Float64()
	return time.Duration(float64(queryVectorFailureTTL) * scale)
}

// rememberable reports whether an encoder error is an outage worth damping.
// Errors tied to the query itself, such as ErrQueryTooLong or the
// ErrUnsupported an invalid encoder answer maps to, are returned uncached as
// before.
func rememberable(err error) bool {
	return errors.Is(err, ErrUnavailable) && !errors.Is(err, ErrQueryTooLong) && !errors.Is(err, ErrUnsupported)
}

// failure returns the remembered outage for key. The caller holds mu.
func (c *QueryVectorCache) failure(key string, now time.Time) (error, bool) {
	failure, ok := c.failures[key]
	if !ok {
		return nil, false
	}
	if now.Before(failure.until) {
		return failure.err, true
	}
	c.forget(key)
	return nil, false
}

// forget drops the remembered outage for key. The caller holds mu.
func (c *QueryVectorCache) forget(key string) {
	if _, ok := c.failures[key]; ok {
		delete(c.failures, key)
		c.failureBytes -= len(key)
	}
}

// remember records an outage for key, dropping expired outages when full and
// skipping the record if the bound still holds. The caller holds mu.
func (c *QueryVectorCache) remember(key string, err error, now time.Time) {
	c.forget(key)
	if len(c.failures) >= queryVectorFailures || c.failureBytes+len(key) > queryVectorFailureBytes {
		for k, failure := range c.failures {
			if !now.Before(failure.until) {
				c.forget(k)
			}
		}
		if len(c.failures) >= queryVectorFailures || c.failureBytes+len(key) > queryVectorFailureBytes {
			return
		}
	}
	c.failures[key] = queryFailure{until: now.Add(queryFailureTTL()), err: err}
	c.failureBytes += len(key)
}

// store takes ownership of an immutable vector and reports how many entries
// it evicted. The caller holds mu.
func (c *QueryVectorCache) store(key string, vector []float32) int64 {
	size := queryVectorEntryOverhead + len(key) + 4*len(vector)
	if size > c.maxBytes {
		return 0
	}
	if existing, ok := c.entries.Load(key); ok {
		// An abandoned flight already populated this immutable key.
		existing.(*queryVectorEntry).referenced.Store(true)
		return 0
	}
	evicted := int64(0)
	// Each entry gets at most one second chance per store, so hits racing
	// with eviction cannot keep it looping.
	chances := c.lru.Len()
	for c.lru.Len() >= c.maxEntries || c.bytes+size > c.maxBytes {
		node := c.lru.Back()
		entry := node.Value.(*queryVectorEntry)
		if chances > 0 && entry.referenced.Swap(false) {
			chances--
			c.lru.MoveToFront(node)
			continue
		}
		c.entries.Delete(entry.key)
		c.lru.Remove(node)
		c.bytes -= entry.bytes
		evicted++
	}
	entry := &queryVectorEntry{key: key, vector: vector, bytes: size}
	c.lru.PushFront(entry)
	c.entries.Store(key, entry)
	c.bytes += size
	return evicted
}

// hit returns a private copy of a loaded entry's vector.
func (c *QueryVectorCache) hit(value any) []float32 {
	entry := value.(*queryVectorEntry)
	// Write the shared bit only on its first hit since the last eviction scan.
	if !entry.referenced.Load() {
		entry.referenced.Store(true)
	}
	c.count(&c.hits)
	return slices.Clone(entry.vector)
}

func (c *QueryVectorCache) encode(ctx context.Context, key string, run func(context.Context) ([]float32, error)) ([]float32, error) {
	if c == nil {
		return run(ctx)
	}
	if ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	if value, ok := c.entries.Load(key); ok {
		return c.hit(value), nil
	}
	c.mu.Lock()
	// A flight may have completed since the lock-free lookup.
	if value, ok := c.entries.Load(key); ok {
		c.mu.Unlock()
		return c.hit(value), nil
	}
	f := c.flights[key]
	if f != nil {
		f.waiters++
		c.mu.Unlock()
		c.count(&c.misses)
	} else {
		now := time.Now()
		if err, ok := c.failure(key, now); ok {
			c.mu.Unlock()
			c.count(&c.misses)
			return nil, err
		}
		work, cancel := context.WithTimeout(context.WithoutCancel(ctx), queryVectorTimeout)
		f = &queryFlight{done: make(chan struct{}), cancel: cancel, waiters: 1}
		key = strings.Clone(key)
		c.flights[key] = f
		c.mu.Unlock()
		c.count(&c.misses)
		if now.UnixNano()-c.counted.Load() >= int64(queryVectorCountFlush) {
			c.flushCounts(now)
		}
		go c.fly(key, f, work, run)
	}
	select {
	case <-ctx.Done():
		c.mu.Lock()
		f.waiters--
		if f.waiters == 0 {
			f.abandoned = true
			if c.flights[key] == f {
				delete(c.flights, key)
			}
			f.cancel()
		}
		c.mu.Unlock()
		return nil, ErrUnavailable
	case <-f.done:
		if ctx.Err() != nil {
			return nil, ErrUnavailable
		}
		if f.err != nil {
			return nil, f.err
		}
		return slices.Clone(f.vector), nil
	}
}

// fly runs one shared encoding. A panicking encoder fails its waiters with
// ErrUnavailable instead of crashing the process, and always releases the
// flight.
func (c *QueryVectorCache) fly(key string, f *queryFlight, work context.Context, run func(context.Context) ([]float32, error)) {
	var vector []float32
	var err error = ErrUnavailable
	defer func() {
		if r := recover(); r != nil {
			// The panic value may quote tenant input; log only its type.
			slog.Error("query encoder panicked; failing the shared lookup", "component", "search", "panic_type", fmt.Sprintf("%T", r), "stack", string(debug.Stack()))
			vector, err = nil, ErrUnavailable
		}
		f.cancel()
		c.finish(key, f, vector, err)
	}()
	vector, err = run(work)
	if err == nil {
		vector = slices.Clone(vector)
	}
}

func (c *QueryVectorCache) finish(key string, f *queryFlight, vector []float32, err error) {
	evicted := int64(0)
	c.mu.Lock()
	switch {
	case err == nil:
		evicted = c.store(key, vector)
		c.forget(key)
	case !f.abandoned && rememberable(err):
		c.remember(key, err, time.Now())
	}
	f.vector = vector
	f.err = err
	if c.flights[key] == f {
		delete(c.flights, key)
	}
	close(f.done)
	c.mu.Unlock()
	if evicted > 0 {
		c.evictions.Add(evicted)
	}
}
