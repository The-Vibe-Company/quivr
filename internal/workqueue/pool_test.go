package workqueue

import (
	"sync"
	"sync/atomic"
	"testing"
)

// saturate runs every slot and the reserved headroom users at once against a
// pool of maxConns connections. Each user takes a connection without waiting,
// the worst case of an acquire timeout, and holds it until every user has
// tried, so the result does not depend on scheduling or on a clock.
func saturate(slots, reserved int, maxConns int32) (lost int, peak int32) {
	pool := make(chan struct{}, maxConns)
	var held, top atomic.Int32
	var failed atomic.Int64
	var tried, done sync.WaitGroup
	release := make(chan struct{})
	tried.Add(slots + reserved)
	for range slots + reserved {
		done.Go(func() {
			select {
			case pool <- struct{}{}:
				n := held.Add(1)
				for old := top.Load(); n > old && !top.CompareAndSwap(old, n); old = top.Load() {
				}
				tried.Done()
				<-release
				held.Add(-1)
				<-pool
			default:
				failed.Add(1)
				tried.Done()
			}
		})
	}
	tried.Wait()
	close(release)
	done.Wait()
	return int(failed.Load()), top.Load()
}

// Owns the slot-vs-pool contract: served slots plus the reserved headroom
// never exceed a pool larger than the served classes, so no running slot loses
// its attempt to connection acquisition, while each class keeps capacity and a
// fitting config is kept. Smaller pools keep exactly one slot per class.
func TestBoundFitsServedSlotsIntoThePool(t *testing.T) {
	both := []string{Live, Bulk}
	for _, tc := range []struct {
		name             string
		queues           []string
		live, bulk       int
		maxConns         int32
		wantLive, wantBk int
	}{
		{"64_slots_on_4_conns", both, 32, 32, 4, 2, 1},
		{"uneven_64_slots_on_4_conns", both, 60, 4, 4, 2, 1},
		{"defaults_fit_16", both, 4, 4, 16, 4, 4},
		{"defaults_on_4_conns", both, 4, 4, 4, 2, 1},
		{"bulk_heavier_gets_leftover", both, 8, 24, 8, 1, 5},
		{"bulk_only_keeps_live_setting", []string{Bulk}, 32, 64, 4, 32, 3},
		{"large_pool_scales_by_share", both, 96, 32, 64, 36, 12},
		{"driver_default_pool_unknown", both, 32, 32, 0, 32, 32},
		{"3_conns_fit_one_slot_per_class", both, 32, 32, 3, 1, 1},
		{"2_conns_keep_one_slot_per_class", both, 32, 32, 2, 1, 1},
		{"1_conn_keeps_one_slot_per_class", both, 32, 32, 1, 1, 1},
		{"1_conn_bulk_only", []string{Bulk}, 32, 32, 1, 32, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Config{Queues: tc.queues, Slots: Slots{Live: tc.live, Bulk: tc.bulk}}
			got := c.Bound(tc.maxConns)
			if got.Slots.Live != tc.wantLive || got.Slots.Bulk != tc.wantBk {
				t.Fatalf("Bound(%d) of live=%d bulk=%d: got live=%d bulk=%d, want live=%d bulk=%d",
					tc.maxConns, tc.live, tc.bulk, got.Slots.Live, got.Slots.Bulk, tc.wantLive, tc.wantBk)
			}
			if tc.maxConns <= 0 {
				return
			}
			slots := 0
			for _, q := range got.Queues {
				slots += got.Capacity(q)
			}
			if int(tc.maxConns) <= len(tc.queues) {
				if slots != len(tc.queues) {
					t.Fatalf("pool of %d below %d served classes: %d slots, want one per class", tc.maxConns, len(tc.queues), slots)
				}
				return
			}
			lost, peak := saturate(slots, PoolHeadroom(tc.maxConns), tc.maxConns)
			if lost != 0 || peak > tc.maxConns {
				t.Fatalf("%d served slots + %d reserved on %d conns: lost=%d peak=%d", slots, PoolHeadroom(tc.maxConns), tc.maxConns, lost, peak)
			}
		})
	}
}
