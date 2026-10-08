package app

import (
	"encoding/json"
	"testing"
)

func TestImportAuditDetailConfiguration(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{{`{}`, false}, {`{"retain_import_audit_detail":false}`, false}, {`{"retain_import_audit_detail":true}`, true}} {
		var cfg Config
		if err := json.Unmarshal([]byte(tc.raw), &cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.RetainImportAuditDetail != tc.want {
			t.Fatalf("%s: import audit detail=%v, want %v", tc.raw, cfg.RetainImportAuditDetail, tc.want)
		}
	}
}

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
