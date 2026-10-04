package plugins

import "math"

// SearchCost sums validated non-negative cost reports across rounds or profiles.
// Its zero value is an empty sum. Engine and Contract Runner use the same rule.
type SearchCost struct {
	cents, roundoff float64
}

// Add compensates for accumulated floating-point roundoff.
func (c *SearchCost) Add(cents float64) {
	delta := cents - c.roundoff
	total := c.cents + delta
	c.roundoff = (total - c.cents) - delta
	c.cents = total
}

// Cents returns the reported total.
func (c SearchCost) Cents() float64 { return c.cents }

// Exceeds allows only machine roundoff at a positive allowance. A zero
// allowance stays strict, and an overflow or invalid sum is always refused.
func (c SearchCost) Exceeds(allowance float64) bool {
	if allowance > 0 {
		allowance = math.Nextafter(math.Nextafter(allowance, math.Inf(1)), math.Inf(1))
	}
	return math.IsInf(c.cents, 0) || math.IsNaN(c.cents) || c.cents > allowance
}
