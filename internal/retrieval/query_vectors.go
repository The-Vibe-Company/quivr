package retrieval

import (
	"container/list"
	"context"
	"encoding/binary"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"golang.org/x/sync/singleflight"
)

const (
	queryVectorEntries = 4096
	queryVectorBytes   = 12 << 20
	// Account for the list node, entry, and its share of the map in addition
	// to the variable-size key and vector payload.
	queryVectorEntryOverhead = 192
	// The plugin protocol caps embed_query at 10 s. This also bounds shared
	// work after every caller has left.
	queryVectorTimeout = 10 * time.Second
)

// QueryVectorCache keeps completed encodings for one service's encoders.
// Its pointer is shared across the copies of Service used to serve searches.
type QueryVectorCache struct {
	maxEntries, maxBytes int
	lookups, evictions   *telemetry.Family
	flight               singleflight.Group
	mu                   sync.Mutex
	lru                  list.List
	entries              map[string]*list.Element
	bytes                int
}

type queryVectorEntry struct {
	key    string
	vector []float32
	bytes  int
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
		entries:    make(map[string]*list.Element),
	}
	c.lookups.Add(0, "hit")
	c.lookups.Add(0, "miss")
	return c
}

// WriteMetrics writes the cache counters in Prometheus text format.
func (c *QueryVectorCache) WriteMetrics(w io.Writer) {
	c.lookups.Write(w)
	c.evictions.Write(w)
}

func queryVectorKey(parts ...string) string {
	var key strings.Builder
	var size [binary.MaxVarintLen64]byte
	for _, part := range parts {
		n := binary.PutUvarint(size[:], uint64(len(part)))
		key.Write(size[:n])
		key.WriteString(part)
	}
	return key.String()
}

func queryGenerations(routes []Route) string {
	if len(routes) == 1 {
		return queryVectorKey(routes[0].Generation.ID)
	}
	ids := make([]string, 0, len(routes))
	for _, r := range routes {
		ids = append(ids, r.Generation.ID)
	}
	slices.Sort(ids)
	return queryVectorKey(slices.Compact(ids)...)
}

func (c *QueryVectorCache) lookup(key string) ([]float32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if node := c.entries[key]; node != nil {
		c.lru.MoveToFront(node)
		return node.Value.(queryVectorEntry).vector, true
	}
	return nil, false
}

// store takes ownership of an immutable vector. Only the flight for this key
// may insert it, and that flight has already checked for a completed entry.
func (c *QueryVectorCache) store(key string, vector []float32) {
	size := queryVectorEntryOverhead + len(key) + 4*len(vector)
	if size > c.maxBytes {
		return
	}
	key = strings.Clone(key)
	c.mu.Lock()
	evicted := int64(0)
	for c.lru.Len() >= c.maxEntries || c.bytes+size > c.maxBytes {
		node := c.lru.Back()
		entry := node.Value.(queryVectorEntry)
		delete(c.entries, entry.key)
		c.lru.Remove(node)
		c.bytes -= entry.bytes
		evicted++
	}
	c.entries[key] = c.lru.PushFront(queryVectorEntry{key: key, vector: vector, bytes: size})
	c.bytes += size
	c.mu.Unlock()
	if evicted > 0 {
		c.evictions.Add(evicted)
	}
}

func (c *QueryVectorCache) encode(ctx context.Context, key string, run func(context.Context) ([]float32, error)) ([]float32, error) {
	if c == nil {
		return run(ctx)
	}
	if ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	if vector, ok := c.lookup(key); ok {
		c.lookups.Add(1, "hit")
		return slices.Clone(vector), nil
	}
	c.lookups.Add(1, "miss")
	result := c.flight.DoChan(key, func() (any, error) {
		// An earlier flight may have filled the cache after our first lookup.
		if vector, ok := c.lookup(key); ok {
			return vector, nil
		}
		// A canceled waiter must not abort work another search is waiting for.
		work, cancel := context.WithTimeout(context.WithoutCancel(ctx), queryVectorTimeout)
		defer cancel()
		vector, err := run(work)
		if err != nil {
			return nil, err
		}
		vector = slices.Clone(vector)
		c.store(key, vector)
		return vector, nil
	})
	select {
	case <-ctx.Done():
		return nil, ErrUnavailable
	case res := <-result:
		if ctx.Err() != nil {
			return nil, ErrUnavailable
		}
		if res.Err != nil {
			return nil, res.Err
		}
		return slices.Clone(res.Val.([]float32)), nil
	}
}
