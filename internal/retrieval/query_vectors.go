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

type queryFlight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	vector  []float32
	err     error
}

// QueryVectorCache keeps completed encodings for one service's encoders.
// Its pointer is shared across the copies of Service used to serve searches.
type QueryVectorCache struct {
	maxEntries, maxBytes int
	lookups, evictions   *telemetry.Family
	mu                   sync.Mutex
	flights              map[string]*queryFlight
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
		flights:    make(map[string]*queryFlight),
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
	if node := c.entries[key]; node != nil {
		// Another flight already populated this immutable key; refresh LRU without duplicating nodes.
		c.lru.MoveToFront(node)
		c.mu.Unlock()
		return
	}
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
	c.mu.Lock()
	if node := c.entries[key]; node != nil {
		c.lru.MoveToFront(node)
		vector := slices.Clone(node.Value.(queryVectorEntry).vector)
		c.mu.Unlock()
		c.lookups.Add(1, "hit")
		return vector, nil
	}
	f := c.flights[key]
	if f != nil {
		f.waiters++
		c.mu.Unlock()
		c.lookups.Add(1, "miss")
	} else {
		work, cancel := context.WithTimeout(context.WithoutCancel(ctx), queryVectorTimeout)
		f = &queryFlight{done: make(chan struct{}), cancel: cancel, waiters: 1}
		c.flights[key] = f
		c.mu.Unlock()
		c.lookups.Add(1, "miss")
		go func() {
			defer cancel()
			vector, err := run(work)
			if err == nil {
				vector = slices.Clone(vector)
				c.store(key, vector)
			}
			c.mu.Lock()
			f.vector = vector
			f.err = err
			if c.flights[key] == f {
				delete(c.flights, key)
			}
			close(f.done)
			c.mu.Unlock()
		}()
	}
	select {
	case <-ctx.Done():
		c.mu.Lock()
		f.waiters--
		if f.waiters == 0 {
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
