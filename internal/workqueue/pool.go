package workqueue

// PoolHeadroom is the share of a process's database pool kept free of
// activity slots: dispatcher acknowledgements, attempt-lease renewals and pool
// health checks acquire connections next to running work. At least one
// connection is always reserved.
func PoolHeadroom(maxConns int32) int {
	return max(1, int(maxConns)/4)
}

// Bound fits a resolved configuration's served activity slots into a
// database pool of maxConns connections less PoolHeadroom. Slots that already
// fit are unchanged; otherwise each served class keeps its share of the
// budget, rounded down, and at least one slot so no class starves. Rounding
// leftovers go to the class that requested more, live on a tie. A
// non-positive maxConns leaves the configuration unchanged.
//
// Activity slots above the pool only queue on connection acquisition: a slot
// waiting past its deadline turns a database wait into a failed attempt. The
// bound keeps every running slot and the headroom able to hold a connection
// at once whenever maxConns exceeds the number of served classes. A smaller
// pool cannot: each served class still keeps one slot, because a class
// without slots would never run its work, and those slots queue on the pool.
func (c Config) Bound(maxConns int32) Config {
	if maxConns <= 0 {
		return c
	}
	requested := 0
	for _, q := range c.Queues {
		requested += c.Capacity(q)
	}
	budget := max(int(maxConns)-PoolHeadroom(maxConns), len(c.Queues))
	if requested <= budget {
		return c
	}
	live, bulk := c.Slots.Live, c.Slots.Bulk
	share := func(n int) int { return max(1, n*budget/requested) }
	if c.Serves(Live) {
		c.Slots.Live = share(c.Slots.Live)
	}
	if c.Serves(Bulk) {
		c.Slots.Bulk = share(c.Slots.Bulk)
	}
	granted := 0
	for _, q := range c.Queues {
		granted += c.Capacity(q)
	}
	if left := budget - granted; left > 0 {
		if c.Serves(Live) && (!c.Serves(Bulk) || live >= bulk) {
			c.Slots.Live += left
		} else {
			c.Slots.Bulk += left
		}
	}
	return c
}
