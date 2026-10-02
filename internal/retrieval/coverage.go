package retrieval

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

// coverageEntries bounds the Corpora a CoverageCache holds; past it the
// cache starts over.
const coverageEntries = 4096

// CoverageCache keeps, for a short time, the vector spaces of each Corpus's
// routed generation and how much of the Corpus each covers. Counting that
// coverage reads every current segment of the Corpus: about 90 ms of a
// 270 ms search on a 5,000-document Corpus (THE-779), growing with the
// Corpus, for figures a retrieval plugin reads as an indication. A search
// may therefore see coverage up to TTL old; a change of routed generation or
// of its spaces (a rebuild, a space promotion) is seen at once, because both
// are part of the key.
type CoverageCache struct {
	TTL time.Duration
	// now is the clock; nil is time.Now.
	now     func() time.Time
	mu      sync.Mutex
	entries map[string]coverageEntry
}

type coverageEntry struct {
	spaces  []content.SpaceCoverage
	total   int64
	expires time.Time
}

func (c *CoverageCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// spaces describes the spaces of a route's generation, from the cache while
// fresh, else from the registry.
func (c *CoverageCache) spaces(ctx context.Context, registry SpaceRegistry, org string, r Route) ([]content.SpaceCoverage, int64, error) {
	if c == nil {
		_, spaces, total, err := registry.VectorSpaces(ctx, org, r.CorpusID)
		return spaces, total, err
	}
	roles, _ := json.Marshal(r.Generation.Spaces)
	key := strings.Join(append([]string{org, r.CorpusID, r.Generation.ID, r.Generation.SpaceID, string(roles)}, r.Generation.VectorSpaces()...), "\x00")
	c.mu.Lock()
	e, ok := c.entries[key]
	c.mu.Unlock()
	if ok && c.clock().Before(e.expires) {
		return e.spaces, e.total, nil
	}
	g, spaces, total, err := registry.VectorSpaces(ctx, org, r.CorpusID)
	if err != nil {
		return nil, 0, err
	}
	// Routing or its served space moved between the two reads: serve this
	// answer, keep nothing.
	currentRoles, _ := json.Marshal(g.Spaces)
	if g.ID != r.Generation.ID || g.SpaceID != r.Generation.SpaceID || string(currentRoles) != string(roles) {
		return spaces, total, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= coverageEntries {
		c.entries = map[string]coverageEntry{}
	}
	c.entries[key] = coverageEntry{spaces: spaces, total: total, expires: c.clock().Add(c.TTL)}
	return spaces, total, nil
}
