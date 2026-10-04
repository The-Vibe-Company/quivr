package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
)

// A worker started before the api migrates waits for the migration instead of
// exiting (THE-806); waiting never hides a schema newer than the binary, and
// without a wait budget (the api) the first pending check still fails.
func TestAwaitSchemaWaitsOnlyForPendingMigrations(t *testing.T) {
	pending := fmt.Errorf("schema migration missing: 20990101T0000Z_next.sql (1 pending): %w", postgres.ErrMigrationsPending)
	newer := fmt.Errorf("schema migration missing: 20990101T0000Z_next.sql (1 pending) while 20990102T0000Z_later.sql is applied: %w", postgres.ErrSchemaNewer)
	for _, tc := range []struct {
		name    string
		results []error
		limit   time.Duration
		calls   int
		wantErr string
	}{
		{name: "migration lands during the wait", results: []error{pending, pending, nil}, limit: time.Minute, calls: 3},
		{name: "schema newer than the binary", results: []error{newer}, limit: 100 * time.Millisecond, calls: 1, wantErr: "newer than this binary"},
		{name: "no wait budget", results: []error{pending}, limit: 0, calls: 1, wantErr: "run migrate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			check := func(context.Context) error {
				err := tc.results[min(calls, len(tc.results)-1)]
				calls++
				return err
			}
			err := awaitSchema(context.Background(), check, schemaWait{Limit: tc.limit, First: time.Millisecond, Max: time.Millisecond})
			if calls != tc.calls {
				t.Fatalf("checks = %d, want %d (err %v)", calls, tc.calls, err)
			}
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}
