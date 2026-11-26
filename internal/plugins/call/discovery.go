package call

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost"
)

const defaultDiscoveryTTL = 30 * time.Second

func discoveryTTL() time.Duration {
	raw := os.Getenv("QUIVR_PLUGIN_DISCOVERY_TTL")
	switch raw {
	case "":
		return defaultDiscoveryTTL
	case "0":
		return 0
	}
	if ttl, err := time.ParseDuration(raw); err == nil && ttl >= 0 {
		return ttl
	}
	return defaultDiscoveryTTL
}

type discoveryKey struct {
	endpoint, digest, id, version, contributions string
	signed                                       bool
}

func keyFor(pin *plugins.Pin) discoveryKey {
	m := &pin.Manifest
	return discoveryKey{
		endpoint: pin.Endpoint, digest: pin.ManifestDigest,
		id: m.ID, version: m.Version, contributions: strings.Join(m.Contributions.Names(), ","),
		signed: pin.Speaks(plugins.FeatureSignedCalls),
	}
}

type discoveryEntry struct {
	done     chan struct{}
	served   string
	err      error
	retry    bool
	verified time.Time
}

// One entry coalesces concurrent misses and retains a successful verification.
// A failed flight can serve its current waiters but is removed before they wake.
type discoveryCache struct {
	mu        sync.Mutex
	entries   map[discoveryKey]*discoveryEntry
	nextSweep time.Time
}

var discoveries = discoveryCache{entries: make(map[discoveryKey]*discoveryEntry)}

func (c *discoveryCache) invalidate(key discoveryKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

func (c *discoveryCache) get(ctx context.Context, key discoveryKey, pin *plugins.Pin, ttl time.Duration) (string, error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		c.mu.Lock()
		now := time.Now()
		if !now.Before(c.nextSweep) {
			for k, entry := range c.entries {
				if !entry.verified.IsZero() && now.Sub(entry.verified) >= ttl {
					delete(c.entries, k)
				}
			}
			c.nextSweep = now.Add(ttl)
		}
		entry := c.entries[key]
		if entry == nil || (!entry.verified.IsZero() && now.Sub(entry.verified) >= ttl) {
			entry = &discoveryEntry{done: make(chan struct{})}
			c.entries[key] = entry
			c.mu.Unlock()
			return c.verify(ctx, key, pin, entry)
		}
		c.mu.Unlock()

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-entry.done:
			if !entry.retry {
				return entry.served, entry.err
			}
			// The leader's invocation ended. A waiter with time left can
			// take over without inheriting that caller's deadline.
		}
	}
}

func (c *discoveryCache) verify(ctx context.Context, key discoveryKey, pin *plugins.Pin, entry *discoveryEntry) (string, error) {
	served, err := verifyDiscovery(ctx, pin)
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil {
		err = ctx.Err()
		entry.retry = true
	}
	if c.entries[key] != entry {
		// A POST can invalidate discovery while a refresh is in flight.
		if err == nil {
			err = fmt.Errorf("%w: discovery was invalidated during verification", plugins.ErrUnavailable)
		}
	} else if err != nil {
		delete(c.entries, key)
	} else {
		entry.verified = time.Now()
	}
	if err != nil {
		served = ""
	}
	entry.served, entry.err = served, err
	close(entry.done)
	return served, err
}

func verifyDiscovery(ctx context.Context, pin *plugins.Pin) (string, error) {
	served, issues, err := devhost.Discover(ctx, pin.Endpoint, pin.Report())
	if err != nil {
		return "", fmt.Errorf("%w: %w", plugins.ErrUnavailable, err)
	}
	if len(issues) > 0 {
		return "", fmt.Errorf("%w: discovery does not match the pinned manifest: %s", plugins.ErrUnavailable, plugins.DescribeIssues(issues))
	}
	return served, nil
}
