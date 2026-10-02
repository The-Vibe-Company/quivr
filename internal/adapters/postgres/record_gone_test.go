package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

// TestRecordGoneFence owns the withdrawn-or-tombstoned SQL contract. It covers
// independent fences and Organization scoping through a real adapter caller;
// withdrawal's transaction and retrieval currentness have separate owners.
func TestRecordGoneFence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := rebuildAdapterPool(t, ctx)
	defer pool.Close()
	org := fmt.Sprintf("adapter-gone-%d", time.Now().UnixNano())
	for _, organization := range []string{org, org + "-other"} {
		scope := corpus.Scope{Organization: organization, Actions: []string{"corpora:write"}, Corpora: []string{"*"}}
		c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "gone", Name: "Gone fence"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO records(organization,id,corpus_id,namespace,record_key) VALUES($1,'same-record',$2,'fixture','record')`, organization, c.ID); err != nil {
			t.Fatal(err)
		}
	}
	// The same Record ID in another Organization is already tombstoned.
	if _, err := pool.Exec(ctx, `INSERT INTO tombstones(organization,record_id) VALUES($1,'same-record')`, org+"-other"); err != nil {
		t.Fatal(err)
	}
	store := contentStores(pool)
	for _, tc := range []struct {
		name                  string
		withdrawn, tombstoned bool
		wantGone              bool
	}{
		{"live despite another organization's fence", false, false, false},
		{"withdrawn only", true, false, true},
		{"tombstoned only", false, true, true},
		{"both fences", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, `UPDATE records SET withdrawn=$2 WHERE organization=$1 AND id='same-record'`, org, tc.withdrawn); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `DELETE FROM tombstones WHERE organization=$1 AND record_id='same-record'`, org); err != nil {
				t.Fatal(err)
			}
			if tc.tombstoned {
				if _, err := pool.Exec(ctx, `INSERT INTO tombstones(organization,record_id) VALUES($1,'same-record')`, org); err != nil {
					t.Fatal(err)
				}
			}
			gone, superseded, err := store.Superseded(ctx, org, "same-record", "version")
			if err != nil || gone != tc.wantGone || superseded {
				t.Fatalf("Superseded(%s, same-record): gone=%v superseded=%v err=%v; want gone=%v superseded=false", org, gone, superseded, err, tc.wantGone)
			}
		})
	}
}
