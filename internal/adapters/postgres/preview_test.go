package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
)

// A preview judges the current eligible Version of each Record, the most
// recently accepted first: a corrected Record appears once, at its
// correction's acceptance; a withdrawn one does not appear; the window and
// the limit bound the listing.
func TestRecentListsCurrentEligibleVersionsNewestFirst(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := newCorrectionFixture(t, ctx, "adapter-preview-")
	corrected, _ := f.publish("corrected", "first", "First text")
	older, olderVersion := f.publish("older", "second", "Second text")
	withdrawn, _ := f.publish("withdrawn", "third", "Third text")
	_, correction := f.publish("corrected", "correction", "Corrected text")
	now := time.Now().UTC().Truncate(time.Second)
	for key, age := range map[string]time.Duration{"first": 4 * time.Hour, "second": 3 * time.Hour, "third": 2 * time.Hour, "correction": time.Hour} {
		if _, err := f.pool.Exec(ctx, `UPDATE ingestion_receipts SET accepted_at=$3 WHERE organization=$1 AND request_key=$2`, f.org, key, now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.pool.Exec(ctx, `UPDATE records SET withdrawn=true WHERE organization=$1 AND id=$2`, f.org, withdrawn); err != nil {
		t.Fatal(err)
	}
	store := postgres.EvaluationStore{ContentStore: f.store}
	list := func(after time.Time, limit int) []string {
		t.Helper()
		got, err := store.Recent(ctx, f.org, []string{f.corpusID}, after, limit)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, v := range got {
			out = append(out, v.RecordID+"@"+v.VersionID)
		}
		return out
	}
	want := []string{corrected + "@" + correction, older + "@" + olderVersion}
	if got := list(time.Time{}, 10); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("want %v, newest first, got %v", want, got)
	}
	if got := list(now.Add(-90*time.Minute), 10); len(got) != 1 || got[0] != want[0] {
		t.Fatalf("accepted_after keeps only the correction: want %v, got %v", want[:1], got)
	}
	if got := list(time.Time{}, 1); len(got) != 1 || got[0] != want[0] {
		t.Fatalf("limit 1 keeps the newest: want %v, got %v", want[:1], got)
	}
	if got, err := store.Recent(ctx, f.org, []string{"other-corpus"}, time.Time{}, 10); err != nil || len(got) != 0 {
		t.Fatalf("another Corpus lists nothing, got %v, %v", got, err)
	}
}
