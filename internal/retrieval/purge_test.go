package retrieval_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
)

type recorded struct {
	item     retrieval.PurgeItem
	deleted  int
	complete bool
}

type fakePurgeStore struct {
	noticeLimit, claimLimit int
	grace                   time.Duration
	items                   []retrieval.PurgeItem
	records                 []recorded
}

func (s *fakePurgeStore) NoticePurges(_ context.Context, limit int) (int, error) {
	s.noticeLimit = limit
	return 0, nil
}

func (s *fakePurgeStore) ClaimPurges(_ context.Context, grace, _ time.Duration, limit int) ([]retrieval.PurgeItem, error) {
	s.grace, s.claimLimit = grace, limit
	if len(s.items) > limit {
		return s.items[:limit], nil
	}
	return s.items, nil
}

func (s *fakePurgeStore) RecordPurge(_ context.Context, item retrieval.PurgeItem, deleted int, complete bool) error {
	s.records = append(s.records, recorded{item, deleted, complete})
	return nil
}

type fakePurgeProjection struct {
	calls   []string
	results map[string]retrieval.PurgeResult
	fail    string
}

func (p *fakePurgeProjection) PurgeGeneration(_ context.Context, collection, org, corpusID, generationID string) (retrieval.PurgeResult, error) {
	return p.call("generation/" + collection + "/" + org + "/" + corpusID + "/" + generationID)
}

func (p *fakePurgeProjection) PurgeVersion(_ context.Context, collection, org, versionID string) (retrieval.PurgeResult, error) {
	return p.call("version/" + collection + "/" + org + "/" + versionID)
}

func (p *fakePurgeProjection) call(key string) (retrieval.PurgeResult, error) {
	p.calls = append(p.calls, key)
	if key == p.fail {
		return retrieval.PurgeResult{}, errors.New("projection unavailable")
	}
	if r, ok := p.results[key]; ok {
		return r, nil
	}
	return retrieval.PurgeResult{Deleted: 2, Complete: true}, nil
}

func TestPurgerSweepIsBoundedAndRecordsOutcomes(t *testing.T) {
	store := &fakePurgeStore{items: []retrieval.PurgeItem{
		{Organization: "o", Kind: retrieval.PurgeGeneration, CorpusID: "c", GenerationID: "g", Collections: []string{"C1"}},
		{Organization: "o", Kind: retrieval.PurgeVersion, VersionID: "v", Collections: []string{"C1", "C2"}},
		{Organization: "o", Kind: retrieval.PurgeVersion, VersionID: "big", Collections: []string{"C1"}},
		{Organization: "o", Kind: retrieval.PurgeVersion, VersionID: "beyond-batch", Collections: []string{"C1"}},
	}}
	projection := &fakePurgeProjection{results: map[string]retrieval.PurgeResult{"version/C1/o/big": {Deleted: 10000, Complete: false}}}
	metrics := retrieval.NewPurgeMetrics()
	done, err := retrieval.Purger{Store: store, Projection: projection, Batch: 3, Grace: 2 * time.Hour, Metrics: metrics}.Sweep(context.Background())
	if err != nil || done != 2 {
		t.Fatalf("sweep completed %d %v", done, err)
	}
	if store.noticeLimit != 3 || store.claimLimit != 3 || store.grace != 2*time.Hour {
		t.Fatalf("bounds notice %d claim %d grace %s", store.noticeLimit, store.claimLimit, store.grace)
	}
	wantCalls := []string{"generation/C1/o/c/g", "version/C1/o/v", "version/C2/o/v", "version/C1/o/big"}
	if len(projection.calls) != len(wantCalls) {
		t.Fatalf("calls %v", projection.calls)
	}
	for i := range wantCalls {
		if projection.calls[i] != wantCalls[i] {
			t.Fatalf("calls %v, want %v", projection.calls, wantCalls)
		}
	}
	want := []recorded{{store.items[0], 2, true}, {store.items[1], 4, true}, {store.items[2], 10000, false}}
	if len(store.records) != len(want) {
		t.Fatalf("records %+v", store.records)
	}
	for i := range want {
		got := store.records[i]
		if got.item.VersionID != want[i].item.VersionID || got.deleted != want[i].deleted || got.complete != want[i].complete {
			t.Fatalf("record %d = %+v, want %+v", i, got, want[i])
		}
	}
	if metrics.Purges[retrieval.PurgeGeneration].Load() != 1 || metrics.Purges[retrieval.PurgeVersion].Load() != 1 ||
		metrics.Objects[retrieval.PurgeGeneration].Load() != 2 || metrics.Objects[retrieval.PurgeVersion].Load() != 10004 {
		t.Fatalf("metrics purges %d/%d objects %d/%d", metrics.Purges[retrieval.PurgeGeneration].Load(), metrics.Purges[retrieval.PurgeVersion].Load(), metrics.Objects[retrieval.PurgeGeneration].Load(), metrics.Objects[retrieval.PurgeVersion].Load())
	}
}

// A projection failure leaves its item unrecorded, so its lease expires and a
// later sweep repeats the idempotent delete; the other items still proceed and
// the sweep reports the failure.
func TestPurgerLeavesFailedItemsForALaterSweep(t *testing.T) {
	store := &fakePurgeStore{items: []retrieval.PurgeItem{
		{Organization: "o", Kind: retrieval.PurgeVersion, VersionID: "v", Collections: []string{"C1", "C2"}},
		{Organization: "o", Kind: retrieval.PurgeVersion, VersionID: "w", Collections: []string{"C1"}},
	}}
	projection := &fakePurgeProjection{fail: "version/C2/o/v"}
	metrics := retrieval.NewPurgeMetrics()
	done, err := retrieval.Purger{Store: store, Projection: projection, Metrics: metrics}.Sweep(context.Background())
	if err == nil || done != 1 || len(store.records) != 1 || store.records[0].item.VersionID != "w" {
		t.Fatalf("sweep %d %v, records %+v", done, err, store.records)
	}
	if metrics.Objects[retrieval.PurgeVersion].Load() != 4 || metrics.Purges[retrieval.PurgeVersion].Load() != 1 {
		t.Fatalf("metrics objects %d purges %d", metrics.Objects[retrieval.PurgeVersion].Load(), metrics.Purges[retrieval.PurgeVersion].Load())
	}
	if store.grace != retrieval.DefaultPurgeGrace || store.claimLimit != 100 {
		t.Fatalf("defaults grace %s batch %d", store.grace, store.claimLimit)
	}
}
