package retrieval_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
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
	quarantined          map[string]content.Diagnostic
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
	return retrieval.RebuildTarget{Operation: operations.Operation{ID: "op", CorpusID: "corpus", State: f.current()}, Generation: g}, nil
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
	out := []retrieval.RebuildCandidate{}
	for _, c := range f.candidates {
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
	return out, nil
}
func (f *fakeRebuildStore) CheckpointRebuild(context.Context, string, string, string) error {

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.current() != operations.StateRunning {
		return operations.ErrNotRunning
	}
	f.checkpoints++
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
	if f.versionErr != nil {
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
}

func (d *gatedRebuildDeriver) Derive(ctx context.Context, org, corpusID string, v content.Version, g content.Generation) (content.Segmentation, []content.EmbeddingData, error) {
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
	return d.fakeDeriver.Derive(ctx, org, corpusID, v, g)
}
func TestRebuildCoversVersionsConcurrentlyWithinTheLimit(t *testing.T) {
	for _, concurrency := range []int{3, 32} {
		t.Run(fmt.Sprint(concurrency), func(t *testing.T) {
			versions := concurrency * 3
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			store := &fakeRebuildStore{covered: map[string][]content.Embedding{}}
			for i := range versions {
				store.candidates = append(store.candidates, retrieval.RebuildCandidate{RecordID: fmt.Sprint(i), VersionID: fmt.Sprint(i), VectorsRequired: true})
			}
			release := make(chan struct{})
			d := &gatedRebuildDeriver{entered: make(chan string, versions), release: release}
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
			select {
			case id := <-d.entered:
				t.Errorf("Version %s exceeded configured concurrency", id)
			default:
			}
			close(release)
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			run(t, r)
			if !store.activated || len(store.covered) != versions || d.derived != versions {
				t.Fatalf("activated=%v coverage=%d embedding calls=%d", store.activated, len(store.covered), d.derived)
			}
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
