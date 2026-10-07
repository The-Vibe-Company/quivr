package retrieval

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

// SpaceDescriptions reads registry metadata without counting segments.
type SpaceDescriptions interface {
	SpaceRegistry
	DescribeVectorSpaces(context.Context, string, string) (content.Generation, []content.SpaceCoverage, error)
}

// SpaceSnapshots serves coverage asynchronously. One refresh per process may
// count at a time; requests use the previous snapshot, with its age, or explicit
// unknown counts until the first refresh completes. Metadata and owner presence
// are always read from the current generation. TTL controls refresh attempts,
// not how long a snapshot remains useful during a slow refresh or outage.
type SpaceSnapshots struct {
	registry SpaceDescriptions
	parent   context.Context
	ttl      time.Duration
	mu       sync.Mutex
	busy     bool
	entries  map[string]*spaceSnapshot
}

type spaceSnapshot struct {
	spaces         map[string]content.SpaceCoverage
	total          int64
	observed, next time.Time
}

func NewSpaceSnapshots(parent context.Context, registry SpaceDescriptions, ttl time.Duration) *SpaceSnapshots {
	return &SpaceSnapshots{registry: registry, parent: parent, ttl: ttl, entries: map[string]*spaceSnapshot{}}
}

func (c *SpaceSnapshots) VectorSpaces(ctx context.Context, org, corpusID string) (content.Generation, []content.SpaceCoverage, int64, error) {
	g, spaces, err := c.registry.DescribeVectorSpaces(ctx, org, corpusID)
	if err != nil {
		return g, nil, 0, err
	}
	key := snapshotKey(org, corpusID, g)
	now := time.Now()
	c.mu.Lock()
	e := c.entries[key]
	if e == nil {
		if len(c.entries) >= coverageEntries {
			c.entries = map[string]*spaceSnapshot{}
		}
		e = &spaceSnapshot{}
		c.entries[key] = e
	}
	if !c.busy && !now.Before(e.next) && c.parent.Err() == nil {
		c.busy = true
		e.next = now.Add(c.ttl)
		go c.refresh(org, corpusID, key, e)
	}
	for i := range spaces {
		sp := &spaces[i]
		sp.CoverageUnknown = true
		if old, ok := e.spaces[sp.ID]; ok {
			sp.Segments, sp.TotalSegments, sp.VersionsCovered = old.Segments, old.TotalSegments, old.VersionsCovered
			sp.CoverageUnknown = false
			age := max(0, now.Sub(e.observed).Milliseconds())
			sp.CoverageAgeMS = &age
		}
	}
	total := e.total
	c.mu.Unlock()
	return g, spaces, total, nil
}

func (c *SpaceSnapshots) refresh(org, corpusID, key string, entry *spaceSnapshot) {
	// The refresh outlives a request deadline, but not application shutdown.
	ctx, cancel := context.WithTimeout(c.parent, time.Minute)
	defer cancel()
	observed := time.Now()
	g, spaces, total, err := c.registry.VectorSpaces(ctx, org, corpusID)
	if err != nil && c.parent.Err() == nil {
		slog.WarnContext(ctx, "vector space coverage refresh failed", "corpus", corpusID, "error", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.busy = false
	entry.next = time.Now().Add(c.ttl)
	if err != nil || snapshotKey(org, corpusID, g) != key || c.entries[key] != entry {
		return
	}
	entry.spaces = make(map[string]content.SpaceCoverage, len(spaces))
	for _, sp := range spaces {
		entry.spaces[sp.ID] = sp
	}
	entry.total, entry.observed = total, observed
}

func snapshotKey(org, corpusID string, g content.Generation) string {
	data, _ := json.Marshal(g)
	return org + "\x00" + corpusID + "\x00" + string(data)
}
