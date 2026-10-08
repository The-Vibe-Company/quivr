package retrieval_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/processing"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

type fakeRebuildStore struct {
	mu         sync.Mutex
	generation content.Generation
	candidates []retrieval.RebuildCandidate
	covered    map[string][]content.Embedding
	coverErr   error
	failed     []operations.Error
	activated  bool
	gap        bool
	// state mirrors the canonical Operation state; empty means running.
	state string
	// cancelAfterCovers requests cancellation once that many Versions are covered.
	cancelAfterCovers int
	// cancelBeforeActivate requests cancellation just before activation commits.
	cancelBeforeActivate bool
	confirms             int
	checkpoints          int
	cursor               string
	coveredEvents        chan string
	scanWait             bool
	scanLate             bool
	quarantined          map[string]content.Diagnostic
	quarantinedEvents    chan string
}

func (f *fakeRebuildStore) current() string {
	if f.state == "" {
		return operations.StateRunning
	}
	return f.state
}

func (f *fakeRebuildStore) BeginRebuild(context.Context, string, string) (retrieval.RebuildTarget, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.generation
	if g.ID == "" {
		g = content.Generation{ID: "target", Collection: "Shared", SpaceID: "space"}
	}
	return retrieval.RebuildTarget{Operation: operations.Operation{ID: "op", CorpusID: "corpus", State: f.current()}, Generation: g, Cursor: f.cursor}, nil
}
func (f *fakeRebuildStore) ConfirmCancel(context.Context, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.current() == operations.StateCancelRequested {
		f.state = operations.StateCanceled
		f.confirms++
	}
	return nil
}
func (f *fakeRebuildStore) RebuildCandidates(_ context.Context, _, _ string, limit int) ([]retrieval.RebuildCandidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.scanLate {
		limit = 2
	}
	out := f.candidatesAfter(f.cursor, limit)
	if len(out) == 0 && f.cursor != "" {
		out = f.candidatesAfter("", limit)
	}
	return out, nil
}
func (f *fakeRebuildStore) candidatesAfter(after string, limit int) []retrieval.RebuildCandidate {
	out := []retrieval.RebuildCandidate{}
	ordered := slices.Clone(f.candidates)
	slices.SortFunc(ordered, func(a, b retrieval.RebuildCandidate) int {
		if a.VersionID < b.VersionID {
			return -1
		}
		if a.VersionID > b.VersionID {
			return 1
		}
		return 0
	})
	for _, c := range ordered {
		if c.VersionID <= after {
			continue
		}
		if _, held := f.quarantined[c.VersionID]; held {
			continue
		}
		if _, ok := f.covered[c.VersionID]; !ok || f.gap {
			out = append(out, c)
			if len(out) == limit {
				break
			}
		}
	}
	return out
}
func (f *fakeRebuildStore) RebuildCandidatesAfter(ctx context.Context, _, _, after string, limit int) ([]retrieval.RebuildCandidate, error) {
	if f.scanWait && limit > 1 {
		<-ctx.Done()
		if !f.scanLate {
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.candidatesAfter(after, limit), nil
}
func (f *fakeRebuildStore) RebuildCandidatePending(_ context.Context, _, _, version string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.candidatesAfter("", len(f.candidates)) {
		if c.VersionID == version {
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeRebuildStore) CheckpointRebuild(_ context.Context, _, _, after string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.current() != operations.StateRunning {
		return operations.ErrNotRunning
	}
	f.checkpoints++
	f.cursor = after
	return nil
}
func (f *fakeRebuildStore) QuarantineRebuild(_ context.Context, _, _ string, version string, reason content.Diagnostic) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.current() != operations.StateRunning {
		return operations.ErrNotRunning
	}
	if f.quarantined == nil {
		f.quarantined = map[string]content.Diagnostic{}
	}
	f.quarantined[version] = reason
	if f.quarantinedEvents != nil {
		f.quarantinedEvents <- version
	}
	return nil
}

type selectiveRebuildDeriver struct {
	fakeDeriver
	refused string
}

func (d *selectiveRebuildDeriver) Derive(ctx context.Context, org, corpusID string, v content.Version, g content.Generation) (content.Segmentation, []content.EmbeddingData, error) {
	if v.ID == d.refused {
		return content.Segmentation{}, nil, content.Refused("cannot process this item")
	}
	return d.fakeDeriver.Derive(ctx, org, corpusID, v, g)
}

func TestRebuildQuarantinesOneTerminalItemAndActivatesOthers(t *testing.T) {
	store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1", VectorsRequired: true}, {RecordID: "r2", VersionID: "v2", VectorsRequired: true}}, covered: map[string][]content.Embedding{}}
	r := rebuilder(store, &fakeRebuildContent{}, &fakeRebuildProjection{})
	r.Plugin = &selectiveRebuildDeriver{refused: "v1"}
	r.Concurrency = 2
	run(t, r)
	if !store.activated || len(store.failed) != 0 || len(store.quarantined) != 1 || store.quarantined["v1"].Code != "ingestion_refused" || len(store.covered["v2"]) != 1 {
		t.Fatalf("activated=%v failed=%v held=%v covered=%v", store.activated, store.failed, store.quarantined, store.covered)
	}
}

func (f *fakeRebuildStore) CoverRebuild(_ context.Context, _, _ string, seg content.Segmentation, artifacts []content.Embedding) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.coverErr != nil {
		return false, f.coverErr
	}
	if f.current() != operations.StateRunning {
		return false, operations.ErrNotRunning
	}
	f.covered[seg.VersionID] = artifacts
	if f.coveredEvents != nil {
		f.coveredEvents <- seg.VersionID
	}
	if f.cancelAfterCovers > 0 && len(f.covered) >= f.cancelAfterCovers {
		f.state = operations.StateCancelRequested
	}
	return true, nil
}
func (f *fakeRebuildStore) ActivateRebuild(context.Context, string, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancelBeforeActivate {
		f.state = operations.StateCancelRequested
	}
	if len(f.failed) > 0 || f.current() != operations.StateRunning {
		return false, operations.ErrNotRunning
	}
	f.activated = !f.gap
	return f.activated, nil
}
func (f *fakeRebuildStore) FailRebuild(_ context.Context, _, _ string, e operations.Error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Like the adapter, a failure only lands on a queued or running Operation.
	if f.current() == operations.StateRunning || f.current() == operations.StateQueued {
		f.failed = append(f.failed, e)
	}
	return nil
}

type fakeRebuildContent struct {
	mu         sync.Mutex
	versionErr error
	missingID  string
	reads      int
	onRead     func()
	timeouts   int
}

func (f *fakeRebuildContent) TrustedVersion(_ context.Context, _, _ string, recordID, id string) (content.Version, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.onRead != nil {
		f.onRead()
	}
	if f.versionErr != nil && (f.missingID == "" || f.missingID == id) {
		return content.Version{}, f.versionErr
	}
	return content.Version{ID: id, RecordID: recordID}, nil
}

func (f *fakeRebuildContent) CountEnrichmentTimeout(context.Context, string, string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.timeouts++
	return f.timeouts, nil
}

type fakeRebuildProjection struct {
	mu               sync.Mutex
	lexical, vectors int
}

func (f *fakeRebuildProjection) Publish(context.Context, content.Generation, string, string, string, content.Version, content.Segmentation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lexical++
	return nil
}
func (f *fakeRebuildProjection) PublishEmbeddings(_ context.Context, _ content.Generation, _ string, data []content.EmbeddingData) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vectors += len(data)
	return nil
}
func (f *fakeRebuildProjection) Search(context.Context, []retrieval.Route, corpus.Scope, retrieval.Request) ([]content.Candidate, error) {
	return nil, errors.New("unused")
}

// fakeDeriver stands for the pinned ingestion plugin that owns "space".
type fakeDeriver struct {
	mu                sync.Mutex
	err               error
	derived, segments int
	onDerive          func()
}

// ownerDeriver models the route-bound plugin's owner-specific served space.
type ownerDeriver struct {
	fakeDeriver
	owner string
}

func (d *ownerDeriver) Bound(context.Context, string, []string) processing.DerivationDriver {
	return d
}
func (d *ownerDeriver) ServedSpace(g content.Generation) string { return g.ServedFor(d.owner) }
func (*ownerDeriver) Serves(context.Context, content.Version, content.Generation) error {
	return nil
}

func (*fakeDeriver) Owns(_ context.Context, space string) bool { return space == "space" }
func (*fakeDeriver) Gone(context.Context, error) (*content.Diagnostic, error) {
	return nil, nil
}
func (d *fakeDeriver) segmentation(v content.Version) content.Segmentation {
	return content.Segmentation{ID: "plugin-seg-" + v.ID, VersionID: v.ID, Segments: []content.Segment{{ID: "plugin-segment-" + v.ID, PartKey: "body"}}}
}
func (d *fakeDeriver) Segment(_ context.Context, _, _ string, v content.Version, _ content.Generation) (content.Segmentation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.segments++
	return d.segmentation(v), d.err
}
func (d *fakeDeriver) Derive(_ context.Context, org, _ string, v content.Version, g content.Generation) (content.Segmentation, []content.EmbeddingData, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.derived++
	if d.onDerive != nil {
		d.onDerive()
	}
	data := []content.EmbeddingData{{Artifact: content.Embedding{ID: "plugin-artifact", Organization: org, SegmentID: "plugin-segment-" + v.ID, SpaceID: g.SpaceID}, Vector: []float32{1}}}
	return d.segmentation(v), data, d.err
}

// routedTo routes every Corpus to a generation served by one space.
type routedTo string

func (r routedTo) Generation(context.Context, string, string) (content.Generation, error) {
	return content.Generation{ID: "routed", SpaceID: string(r)}, nil
}

type routedGeneration content.Generation

func (r routedGeneration) Generation(context.Context, string, string) (content.Generation, error) {
	return content.Generation(r), nil
}

func rebuilder(store *fakeRebuildStore, c *fakeRebuildContent, p *fakeRebuildProjection) retrieval.Rebuilder {
	return retrieval.Rebuilder{Concurrency: 1, Store: store, Cancellation: store, Content: c, Projection: p, Plugin: &fakeDeriver{}, Routing: routedTo("space")}
}

func run(t *testing.T, r retrieval.Rebuilder) {
	t.Helper()
	for i := 0; i < 10; i++ {
		done, err := r.Step(context.Background(), "org", "op")
		if err != nil {
			t.Fatal(err)
		}
		if done {
			return
		}
	}
	t.Fatal("rebuild did not converge")
}

// A Version whose vectors the routed generation serves is derived with its
// vectors through the plugin; one still waiting for its enrichment in the
// same space is covered with its segments alone, and never waits for an
// embedding backend: its enrichment attaches the vectors after the cutover.
func TestRebuildDerivesThroughThePluginAndActivates(t *testing.T) {
	store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1", VectorsRequired: true}, {RecordID: "r2", VersionID: "v2"}}, covered: map[string][]content.Embedding{}}
	p, d := &fakeRebuildProjection{}, &fakeDeriver{}
	r := rebuilder(store, &fakeRebuildContent{}, p)
	r.Plugin = d
	run(t, r)
	if !store.activated || len(store.failed) != 0 || d.derived != 1 || d.segments != 1 {
		t.Fatalf("activated=%v failed=%v derived=%d segmented=%d", store.activated, store.failed, d.derived, d.segments)
	}
	if len(store.covered["v1"]) != 1 || store.covered["v1"][0].ID != "plugin-artifact" || len(store.covered["v2"]) != 0 || p.lexical != 2 || p.vectors != 1 {
		t.Fatalf("covered=%v projection=%+v", store.covered, p)
	}
}

// A target of another space than the routed generation's, such as the move
// off the legacy E5 space, gets every Version's vectors in its space.
func TestRebuildOntoAnotherSpaceEmbedsEveryVersion(t *testing.T) {
	store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1"}}, covered: map[string][]content.Embedding{}}
	d := &fakeDeriver{}
	r := rebuilder(store, &fakeRebuildContent{}, &fakeRebuildProjection{})
	r.Plugin, r.Routing = d, routedTo("legacy-e5")
	run(t, r)
	if !store.activated || d.derived != 1 || d.segments != 0 || len(store.covered["v1"]) != 1 {
		t.Fatalf("activated=%v derived=%d segmented=%d covered=%v", store.activated, d.derived, d.segments, store.covered)
	}
}

func TestRebuildAfterOwnerSwitchEmbedsCarriedSpace(t *testing.T) {
	store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1"}}, covered: map[string][]content.Embedding{}}
	store.generation = content.Generation{ID: "target", SpaceID: "space", IngestionRouting: &content.IngestionRouting{Default: "next.owner"}}
	d := &fakeDeriver{}
	r := rebuilder(store, &fakeRebuildContent{}, &fakeRebuildProjection{})
	r.Plugin = d
	r.Routing = routedGeneration(content.Generation{ID: "routed", SpaceID: "space", IngestionRouting: &content.IngestionRouting{Default: "previous.owner"}})
	run(t, r)
	if !store.activated || len(store.covered["v1"]) != 1 {
		t.Fatalf("owner switch activated=%v vector coverage=%v", store.activated, store.covered)
	}
}

func TestRebuildPreservesLegacyLexicalAvailability(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current content.Generation
		vectors int
	}{
		{"primary space", content.Generation{ID: "legacy", SpaceID: "space"}, 0},
		{"carried owner space", content.Generation{ID: "legacy", SpaceID: "old-space", Spaces: []content.GenerationSpace{{ID: "old-space", OwnerPluginID: "old.owner", Role: content.SpaceServed}, {ID: "space", OwnerPluginID: "same.owner", Role: content.SpaceServed}}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1"}}, covered: map[string][]content.Embedding{}}
			store.generation = content.Generation{ID: "target", SpaceID: "space", IngestionRouting: &content.IngestionRouting{Default: "same.owner"}, Spaces: []content.GenerationSpace{{ID: "space", OwnerPluginID: "same.owner", Role: content.SpaceServed}}}
			r := rebuilder(store, &fakeRebuildContent{}, &fakeRebuildProjection{})
			r.Plugin, r.Routing = &ownerDeriver{owner: "same.owner"}, routedGeneration(tc.current)
			run(t, r)
			if !store.activated || len(store.covered["v1"]) != tc.vectors {
				t.Fatalf("legacy rebuild activated=%v vector coverage=%v, want %d", store.activated, store.covered, tc.vectors)
			}
		})
	}
}

// A target whose served space the pinned plugin does not own cannot be built.
func TestRebuildRetriesWhenNoPinnedPluginServesTarget(t *testing.T) {
	store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1"}}, covered: map[string][]content.Embedding{}}
	r := rebuilder(store, &fakeRebuildContent{}, &fakeRebuildProjection{})
	r.Plugin = nil
	done, err := r.Step(t.Context(), "org", "op")
	if done || !errors.Is(err, processing.ErrSpaceUnowned) || store.activated || len(store.failed) != 0 || len(store.quarantined) != 0 {
		t.Fatalf("activated=%v failed=%v", store.activated, store.failed)
	}
}

func TestRebuildQuarantinesAnItemWhoseCanonicalArtifactIsLost(t *testing.T) {
	store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1"}}, covered: map[string][]content.Embedding{}}
	run(t, rebuilder(store, &fakeRebuildContent{versionErr: content.ErrArtifactCorrupt}, &fakeRebuildProjection{}))
	if !store.activated || len(store.failed) != 0 || store.quarantined["v1"].Code != "canonical_content_unavailable" {
		t.Fatalf("activated=%v failed=%v", store.activated, store.failed)
	}
}

// A candidate that remains listed after canonical hydration returns not found
// must terminate clearly instead of completing identical batches forever.
func TestRebuildQuarantinesAnEligibleItemThatCannotBeHydrated(t *testing.T) {
	for _, withdrawn := range []bool{false, true} {
		t.Run(fmt.Sprintf("withdrawn=%v", withdrawn), func(t *testing.T) {
			store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1"}}, covered: map[string][]content.Embedding{}}
			canonical := &fakeRebuildContent{versionErr: corpus.ErrNotFound}
			if withdrawn {
				canonical.onRead = func() { store.candidates = nil }
			}
			run(t, rebuilder(store, canonical, &fakeRebuildProjection{}))
			if withdrawn {
				if !store.activated || len(store.failed) != 0 {
					t.Fatalf("withdrawn Version blocked activation: activated=%v failed=%v", store.activated, store.failed)
				}
			} else if !store.activated || len(store.failed) != 0 || store.quarantined["v1"].Code != "canonical_content_unavailable" {
				t.Fatalf("activated=%v failed=%v", store.activated, store.failed)
			}
		})
	}
	t.Run("unreadable Version beyond a slow first page", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		store := &fakeRebuildStore{covered: map[string][]content.Embedding{}, quarantinedEvents: make(chan string, 1)}
		for i := range 60 {
			id := fmt.Sprintf("v%03d", i)
			store.candidates = append(store.candidates, retrieval.RebuildCandidate{RecordID: fmt.Sprint(i), VersionID: id, VectorsRequired: true})
		}
		release := make(chan struct{})
		close(release)
		d := &gatedRebuildDeriver{entered: make(chan string, 60), release: release, slow: make(chan struct{}), slowID: "v000"}
		r := rebuilder(store, &fakeRebuildContent{versionErr: corpus.ErrNotFound, missingID: "v059"}, &fakeRebuildProjection{})
		r.Plugin, r.Concurrency = d, 3
		finished := make(chan error, 1)
		go func() { _, err := r.Step(ctx, "org", "op"); finished <- err }()
		select {
		case version := <-store.quarantinedEvents:
			if version != "v059" {
				t.Fatalf("quarantined %s, want unreadable v059", version)
			}
		case err := <-finished:
			t.Fatalf("stream ended without quarantining eligible unreadable v059 beyond the first gap page: %v", err)
		case <-ctx.Done():
			t.Fatal("eligible unreadable v059 was silently skipped beyond the first gap page")
		}
		cancel()
		if err := <-finished; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if store.quarantined["v059"].Code != "canonical_content_unavailable" {
			t.Fatalf("diagnostic=%v", store.quarantined)
		}
	})
}

func TestRebuildRetriesWhenStorageLeavesTheSameCoverageGap(t *testing.T) {
	store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1", VectorsRequired: true}}, covered: map[string][]content.Embedding{}, gap: true}
	done, err := rebuilder(store, &fakeRebuildContent{}, &fakeRebuildProjection{}).Step(t.Context(), "org", "op")
	if done || err == nil || store.activated || len(store.failed) != 0 || store.checkpoints != 0 {

		t.Fatalf("activated=%v failed=%v", store.activated, store.failed)
	}
}

func TestRebuildQuarantinesSegmentationDifferingFromDurableArtifact(t *testing.T) {
	store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1"}}, covered: map[string][]content.Embedding{}, coverErr: content.ErrConflict}
	run(t, rebuilder(store, &fakeRebuildContent{}, &fakeRebuildProjection{}))
	if !store.activated || len(store.failed) != 0 || store.quarantined["v1"].Code != "segmentation_mismatch" {
		t.Fatalf("activated=%v failed=%v", store.activated, store.failed)
	}
}

func TestRebuildWaitsWhileCoverageGapsRemain(t *testing.T) {
	store := &fakeRebuildStore{covered: map[string][]content.Embedding{}, gap: true}
	done, err := rebuilder(store, &fakeRebuildContent{}, &fakeRebuildProjection{}).Step(context.Background(), "org", "op")
	if err != nil || done {
		t.Fatalf("gap step = %v %v", done, err)
	}
}

// Cancellation landing mid-batch stops remaining work, keeps committed coverage
// and settles the Operation as canceled without activating the target.
func TestRebuildStopsRemainingWorkWhenCancellationIsRequested(t *testing.T) {
	store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1"}, {RecordID: "r2", VersionID: "v2"}}, covered: map[string][]content.Embedding{}, cancelAfterCovers: 1}
	p := &fakeRebuildProjection{}
	run(t, rebuilder(store, &fakeRebuildContent{}, p))
	if store.activated || store.state != operations.StateCanceled || store.confirms != 1 || len(store.failed) != 0 {
		t.Fatalf("activated=%v state=%s confirms=%d failed=%v", store.activated, store.state, store.confirms, store.failed)
	}
	if _, ok := store.covered["v1"]; !ok || len(store.covered) != 1 {
		t.Fatalf("committed coverage %v", store.covered)
	}
	t.Run("while no document completes", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			store := &fakeRebuildStore{covered: map[string][]content.Embedding{}, candidates: []retrieval.RebuildCandidate{{RecordID: "r0", VersionID: "v000", VectorsRequired: true}}}
			release := make(chan struct{})
			close(release)
			d := &gatedRebuildDeriver{entered: make(chan string, 1), release: release, slow: make(chan struct{}), slowID: "v000"}
			r := rebuilder(store, &fakeRebuildContent{}, &fakeRebuildProjection{})
			r.Plugin = d
			go func() {
				<-d.entered
				store.mu.Lock()
				store.state = operations.StateCancelRequested
				store.mu.Unlock()
			}()
			done, err := r.Step(context.Background(), "org", "op")
			if !done || err != nil || store.state != operations.StateCanceled || store.confirms != 1 || store.activated || len(store.covered) != 0 || d.active.Load() != 0 {
				t.Fatalf("stalled cancellation done=%v err=%v state=%s confirms=%d active=%d covered=%v", done, err, store.state, store.confirms, d.active.Load(), store.covered)
			}
		})
	})

}

// A request observed at the start of a step settles before any content work.
func TestRebuildConfirmsCancellationRequestedBetweenSteps(t *testing.T) {
	store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1"}}, covered: map[string][]content.Embedding{}, state: operations.StateCancelRequested}
	c, p := &fakeRebuildContent{}, &fakeRebuildProjection{}
	done, err := rebuilder(store, c, p).Step(context.Background(), "org", "op")
	if err != nil || !done || store.state != operations.StateCanceled || p.lexical != 0 || c.reads != 0 {
		t.Fatalf("done=%v err=%v state=%s projection=%+v reads=%d", done, err, store.state, p, c.reads)
	}
	// A queued Operation canceled directly never runs and needs no confirmation.
	store = &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1"}}, covered: map[string][]content.Embedding{}, state: operations.StateCanceled}
	done, err = rebuilder(store, c, p).Step(context.Background(), "org", "op")
	if err != nil || !done || store.state != operations.StateCanceled || store.confirms != 0 || p.lexical != 0 {
		t.Fatalf("canceled step done=%v err=%v state=%s", done, err, store.state)
	}
}

// Completion racing cancellation: once cancellation is committed first,
// activation refuses and the Operation settles as canceled.
func TestRebuildCancellationBeforeActivationPreventsCutover(t *testing.T) {
	store := &fakeRebuildStore{covered: map[string][]content.Embedding{}, cancelBeforeActivate: true}
	run(t, rebuilder(store, &fakeRebuildContent{}, &fakeRebuildProjection{}))
	if store.activated || store.state != operations.StateCanceled {
		t.Fatalf("activated=%v state=%s", store.activated, store.state)
	}
}

// Terminal per-item failures quarantine only that item; outages retry.
func TestRebuildPluginFailures(t *testing.T) {
	for code, err := range map[string]error{"ingestion_refused": fmt.Errorf("%w: terminal", content.ErrIngestionRefused), "segmentation_mismatch": content.ErrConflict} {
		store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1", VectorsRequired: true}}, covered: map[string][]content.Embedding{}}
		r := rebuilder(store, &fakeRebuildContent{}, &fakeRebuildProjection{})
		r.Plugin = &fakeDeriver{err: err}
		run(t, r)
		if !store.activated || len(store.failed) != 0 || store.quarantined["v1"].Code != code || store.quarantined["v1"].Retryable {
			t.Fatalf("%s: activated=%v failed=%v", code, store.activated, store.failed)
		}
	}
	store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1"}}, covered: map[string][]content.Embedding{}}
	r := rebuilder(store, &fakeRebuildContent{}, &fakeRebuildProjection{})
	r.Plugin = &fakeDeriver{err: errors.New("plugin unavailable")}
	if _, err := r.Step(context.Background(), "org", "op"); err == nil || len(store.failed) != 0 || store.checkpoints != 0 {
		t.Fatalf("outage: err=%v failed=%v", err, store.failed)
	}
}

// A terminal failure discovered after cancellation was requested settles as
// canceled: the earlier operator request wins over the later failure.
func TestRebuildTerminalFailureAfterCancelRequestSettlesCanceled(t *testing.T) {
	store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1", VectorsRequired: true}}, covered: map[string][]content.Embedding{}}
	r := rebuilder(store, &fakeRebuildContent{}, &fakeRebuildProjection{})
	r.Plugin = &fakeDeriver{err: fmt.Errorf("%w: terminal", content.ErrIngestionRefused), onDerive: func() { store.state = operations.StateCancelRequested }}
	run(t, r)
	if store.activated || store.state != operations.StateCanceled || len(store.failed) != 0 {
		t.Fatalf("activated=%v state=%s failed=%v", store.activated, store.state, store.failed)
	}
}

// The shared per-item deadline budget holds only that Version; outages retry
// freely and never consume it.
func TestRebuildStopsAfterTheSharedVectorDeadlineBudget(t *testing.T) {
	store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1", VectorsRequired: true}}, covered: map[string][]content.Embedding{}}
	canonical := &fakeRebuildContent{}
	plugin := &fakeDeriver{}
	rebuilder := retrieval.Rebuilder{Store: store, Cancellation: store, Content: canonical, Projection: &fakeRebuildProjection{}, Plugin: plugin, Routing: routedTo("space")}
	for _, cause := range []error{processing.ErrPluginDeadline, errors.New("connection refused"), processing.ErrPluginDeadline} {
		plugin.err = cause
		if done, err := rebuilder.Step(context.Background(), "org", "op"); err == nil || done {
			t.Fatalf("transient derivation: done=%v err=%v", done, err)
		}
	}
	if canonical.timeouts != 2 || len(store.failed) != 0 {
		t.Fatalf("outage consumed deadline budget: %d %v", canonical.timeouts, store.failed)
	}
	plugin.err = processing.ErrPluginDeadline
	done, err := rebuilder.Step(context.Background(), "org", "op")
	if err != nil || done || len(store.failed) != 0 || store.quarantined["v1"].Code != content.CodeEnrichmentTimeout {
		t.Fatalf("deadline budget: done=%v err=%v failures=%v activated=%v", done, err, store.failed, store.activated)
	}
	run(t, rebuilder)
	if !store.activated {
		t.Fatal("held item blocked activation")
	}
}

// Holding embedding calls open proves simultaneous Version work without sleeps.
// Step must respect the cap and cover each candidate once before cutover.
type gatedRebuildDeriver struct {
	fakeDeriver
	entered chan string
	release <-chan struct{}
	slow    <-chan struct{}
	slowID  string
	active  atomic.Int32
	peak    atomic.Int32
}

func (d *gatedRebuildDeriver) Derive(ctx context.Context, org, corpusID string, v content.Version, g content.Generation) (content.Segmentation, []content.EmbeddingData, error) {
	active := d.active.Add(1)
	defer d.active.Add(-1)
	for old := d.peak.Load(); active > old; old = d.peak.Load() {
		if d.peak.CompareAndSwap(old, active) {
			break
		}
	}
	select {
	case d.entered <- v.ID:
	case <-ctx.Done():
		return content.Segmentation{}, nil, ctx.Err()
	}
	select {
	case <-d.release:
	case <-ctx.Done():
		return content.Segmentation{}, nil, ctx.Err()
	}
	if v.ID == d.slowID {
		select {
		case <-d.slow:
		case <-ctx.Done():
			return content.Segmentation{}, nil, ctx.Err()
		}
	}
	return d.fakeDeriver.Derive(ctx, org, corpusID, v, g)
}
func TestRebuildCoversVersionsConcurrentlyWithinTheLimit(t *testing.T) {
	for _, concurrency := range []int{3, 32} {
		t.Run(fmt.Sprint(concurrency), func(t *testing.T) {
			versions := max(60, concurrency*3)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			store := &fakeRebuildStore{covered: map[string][]content.Embedding{}, coveredEvents: make(chan string, versions)}
			for i := range versions {
				store.candidates = append(store.candidates, retrieval.RebuildCandidate{RecordID: fmt.Sprint(i), VersionID: fmt.Sprintf("v%03d", i), VectorsRequired: true})
			}
			release, slow := make(chan struct{}), make(chan struct{})
			slowID, wantCursor := "v000", ""
			if concurrency == 32 {
				slowID, wantCursor = "v005", "v004"
			}
			d := &gatedRebuildDeriver{entered: make(chan string, versions), release: release, slow: slow, slowID: slowID}
			r := rebuilder(store, &fakeRebuildContent{}, &fakeRebuildProjection{})
			r.Plugin, r.Concurrency = d, concurrency
			finished := make(chan error, 1)
			go func() { _, err := r.Step(ctx, "org", "op"); finished <- err }()
			for range concurrency {
				select {
				case <-d.entered:
				case <-ctx.Done():
					t.Fatalf("%d embedding calls did not overlap", concurrency)
				}
			}
			close(release)
			for range versions - 1 {
				select {
				case id := <-store.coveredEvents:
					if id == slowID {
						t.Fatal("slow Version covered before release")
					}
				case <-ctx.Done():
					store.mu.Lock()
					covered := len(store.covered)
					store.mu.Unlock()
					t.Fatalf("other slots stopped at %d/%d covered Versions behind slow %s; must advance across pages", covered, versions-1, slowID)
				}
			}
			store.mu.Lock()
			cursor := store.cursor
			store.mu.Unlock()
			if cursor > wantCursor {
				t.Fatalf("checkpoint %q, must stay at or below predecessor %q of unfinished %s", cursor, wantCursor, slowID)
			}
			if peak := d.peak.Load(); peak != int32(concurrency) {
				t.Fatalf("peak concurrency %d, want %d", peak, concurrency)
			}
			// Interrupt the attempt while the first Version is unfinished. Resume
			// from durable coverage; fast Versions must not run again.
			cancel()
			if err := <-finished; !errors.Is(err, context.Canceled) {
				t.Fatalf("interrupted step: %v, want cancellation", err)
			}
			next := &fakeDeriver{}
			r.Plugin = next
			run(t, r)
			if !store.activated || len(store.covered) != versions || d.derived != versions-1 || next.derived != 1 {
				t.Fatalf("activated=%v coverage=%d pre-crash calls=%d resumed calls=%d", store.activated, len(store.covered), d.derived, next.derived)
			}
		})
	}
}

// A one-minute admission budget leaves active documents time to finish; a
// slow item must not be canceled and restarted forever at each turn boundary.
type clockedRebuildDeriver struct {
	fakeDeriver
	delay time.Duration
	fast  string
}

func (d *clockedRebuildDeriver) Derive(ctx context.Context, org, corpusID string, v content.Version, g content.Generation) (content.Segmentation, []content.EmbeddingData, error) {
	if v.ID != d.fast {
		timer := time.NewTimer(d.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return content.Segmentation{}, nil, ctx.Err()
		}
	}
	return d.fakeDeriver.Derive(ctx, org, corpusID, v, g)
}
func TestRebuildYieldsWithoutConsumingItemDeadlineBudget(t *testing.T) {
	for _, tc := range []struct {
		name               string
		scanWait, scanLate bool
		attempt, delay     time.Duration
	}{
		{"document", false, false, 30 * time.Minute, 80 * time.Second},
		{"candidate lookup", true, false, 30 * time.Minute, 80 * time.Second},
		{"short attempt", false, false, 40 * time.Second, 30 * time.Second},
		{"lookup returns at cutoff", true, true, 30 * time.Minute, 80 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), tc.attempt)
				defer cancel()
				store := &fakeRebuildStore{covered: map[string][]content.Embedding{}, scanWait: tc.scanWait, scanLate: tc.scanLate}
				versions := 4
				d := &clockedRebuildDeriver{delay: tc.delay}
				if tc.scanWait {
					d.fast = "v001"
					if !tc.scanLate {
						versions = 2
					}
				}
				for i := range versions {
					store.candidates = append(store.candidates, retrieval.RebuildCandidate{RecordID: fmt.Sprint(i), VersionID: fmt.Sprintf("v%03d", i), VectorsRequired: true})
				}
				canonical := &fakeRebuildContent{}
				r := rebuilder(store, canonical, &fakeRebuildProjection{})
				r.Concurrency, r.Plugin = 2, d
				started := time.Now()
				done, err := r.Step(ctx, "org", "op")
				if done || err != nil || time.Since(started) != tc.delay || ctx.Err() != nil {
					t.Fatalf("turn done=%v err=%v elapsed=%v parent=%v; want drain after %v", done, err, time.Since(started), ctx.Err(), tc.delay)
				}
				if len(store.covered) != 2 || d.derived != 2 || canonical.timeouts != 0 || len(store.quarantined) != 0 || store.cursor != "v001" {
					t.Fatalf("yield covered=%v calls=%d deadlines=%d held=%v cursor=%q; want only initial two Versions covered", store.covered, d.derived, canonical.timeouts, store.quarantined, store.cursor)
				}
			})
		})
	}
}

// A failed candidate cancels its siblings, and Step joins their cleanup before
// recording failure or retrying. A parent cancellation has the same join rule.
type interruptedRebuildDeriver struct {
	fakeDeriver
	entered     chan string
	fail        <-chan struct{}
	cleanup     <-chan struct{}
	canceled    chan string
	cause       error
	afterCancel error
}

func (d *interruptedRebuildDeriver) Derive(ctx context.Context, _, _ string, v content.Version, _ content.Generation) (content.Segmentation, []content.EmbeddingData, error) {
	d.entered <- v.ID
	if v.ID == "0" && d.cause != nil {
		<-d.fail
		return content.Segmentation{}, nil, d.cause
	}
	<-ctx.Done()
	d.canceled <- v.ID
	<-d.cleanup
	if v.ID == "1" && d.afterCancel != nil {
		return content.Segmentation{}, nil, d.afterCancel
	}
	return content.Segmentation{}, nil, ctx.Err()
}
func TestRebuildJoinsCanceledCandidatesBeforeSettling(t *testing.T) {
	outage := errors.New("provider unavailable")
	for _, tc := range []struct {
		name        string
		cause       error
		afterCancel error
		canceled    bool
	}{
		{name: "outage", cause: outage},

		{name: "context"},
		{name: "item isolation during outage", cause: outage, afterCancel: content.ErrIngestionRefused},
		{name: "cancellation precedence", cause: outage, afterCancel: operations.ErrNotRunning, canceled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			watchdog, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			store := &fakeRebuildStore{covered: map[string][]content.Embedding{}}
			for i := range 9 {
				store.candidates = append(store.candidates, retrieval.RebuildCandidate{RecordID: fmt.Sprint(i), VersionID: fmt.Sprint(i), VectorsRequired: true})
			}
			fail, cleanup := make(chan struct{}), make(chan struct{})
			d := &interruptedRebuildDeriver{entered: make(chan string, 9), fail: fail, cleanup: cleanup, canceled: make(chan string, 3), cause: tc.cause, afterCancel: tc.afterCancel}
			r := rebuilder(store, &fakeRebuildContent{}, &fakeRebuildProjection{})
			r.Concurrency, r.Plugin = 3, d
			finished := make(chan error, 1)
			go func() { _, err := r.Step(ctx, "org", "op"); finished <- err }()
			for range 3 {
				select {
				case <-d.entered:
				case <-watchdog.Done():
					t.Fatal("initial candidates did not overlap")
				}
			}
			siblings := 2
			if tc.cause == nil {
				siblings = 3
				cancel()
			} else {
				if tc.canceled {
					store.mu.Lock()
					store.state = operations.StateCancelRequested
					store.mu.Unlock()
				}
				close(fail)
			}
			for range siblings {
				select {
				case <-d.canceled:
				case <-watchdog.Done():
					t.Fatal("candidate did not observe cancellation")
				}
			}
			select {
			case err := <-finished:
				t.Fatalf("Step returned before siblings joined: %v", err)
			default:
			}
			store.mu.Lock()
			failuresBeforeJoin := len(store.failed)
			store.mu.Unlock()
			if failuresBeforeJoin != 0 {
				t.Fatal("failure committed before canceled candidates joined")
			}
			close(cleanup)
			var err error
			select {
			case err = <-finished:
			case <-watchdog.Done():
				t.Fatal("Step did not return after candidates joined")
			}
			if tc.canceled {
				if err != nil || store.state != operations.StateCanceled || store.confirms != 1 || len(store.failed) != 0 {
					t.Fatalf("cancellation err=%v state=%s confirms=%d failures=%v", err, store.state, store.confirms, store.failed)
				}
			} else {
				want := tc.cause
				if want == nil {
					want = context.Canceled
				}
				if !errors.Is(err, want) || len(store.failed) != 0 {
					t.Fatalf("retry err=%v failures=%v, want %v", err, store.failed, want)
				}
			}
			if tc.afterCancel == content.ErrIngestionRefused && store.quarantined["1"].Code != "ingestion_refused" {
				t.Fatalf("terminal sibling was not quarantined: %v", store.quarantined)
			}
			if len(d.entered) != 0 || len(store.covered) != 0 || store.activated || store.checkpoints != 0 {
				t.Fatalf("remaining calls=%d covered=%v activated=%v", len(d.entered), store.covered, store.activated)
			}
		})
	}
}
