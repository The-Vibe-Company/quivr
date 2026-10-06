package app

import "testing"

func TestAuditRetentionCalendarMonths(t *testing.T) {
	for _, tc := range []struct {
		input, want int
		invalid     bool
	}{{0, 12, false}, {1, 1, false}, {24, 24, false}, {-1, 0, true}, {1201, 0, true}} {
		got, err := auditRetentionMonths(tc.input)
		if got != tc.want || (err != nil) != tc.invalid {
			t.Fatalf("audit_retention_months=%d: got %d,%v; want %d,invalid=%v", tc.input, got, err, tc.want, tc.invalid)
		}
	}
}
