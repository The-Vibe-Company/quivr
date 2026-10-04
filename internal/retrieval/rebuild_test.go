package retrieval_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/processing"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

type fakeRebuildStore struct {
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
}

func (f *fakeRebuildStore) current() string {
	if f.state == "" {
		return operations.StateRunning
	}
	return f.state
}

func (f *fakeRebuildStore) BeginRebuild(context.Context, string, string) (retrieval.RebuildTarget, error) {
	return retrieval.RebuildTarget{Operation: operations.Operation{ID: "op", CorpusID: "corpus", State: f.current()}, Generation: content.Generation{ID: "target", Collection: "Shared", SpaceID: "space"}}, nil
}
func (f *fakeRebuildStore) ConfirmCancel(context.Context, string, string) error {
	if f.current() == operations.StateCancelRequested {
		f.state = operations.StateCanceled
		f.confirms++
	}
	return nil
}
func (f *fakeRebuildStore) RebuildCandidates(context.Context, string, string, int) ([]retrieval.RebuildCandidate, error) {
	out := []retrieval.RebuildCandidate{}
	for _, c := range f.candidates {
		if _, ok := f.covered[c.VersionID]; !ok {
			out = append(out, c)
		}
	}
	return out, nil
}
func (f *fakeRebuildStore) CoverRebuild(_ context.Context, _, _ string, seg content.Segmentation, artifacts []content.Embedding) (bool, error) {
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
	// Like the adapter, a failure only lands on a queued or running Operation.
	if f.current() == operations.StateRunning || f.current() == operations.StateQueued {
		f.failed = append(f.failed, e)
	}
	return nil
}

type fakeRebuildContent struct {
	versionErr error
	reads      int
	timeouts   int
}

func (f *fakeRebuildContent) TrustedVersion(_ context.Context, _, _ string, recordID, id string) (content.Version, error) {
	f.reads++
	if f.versionErr != nil {
		return content.Version{}, f.versionErr
	}
	return content.Version{ID: id, RecordID: recordID}, nil
}

func (f *fakeRebuildContent) CountEnrichmentTimeout(context.Context, string, string) (int, error) {
	f.timeouts++
	return f.timeouts, nil
}

type fakeRebuildProjection struct{ lexical, vectors int }

func (f *fakeRebuildProjection) Publish(context.Context, content.Generation, string, string, string, content.Version, content.Segmentation) error {
	f.lexical++
	return nil
}
func (f *fakeRebuildProjection) PublishEmbeddings(_ context.Context, _ content.Generation, _ string, data []content.EmbeddingData) error {
	f.vectors += len(data)
	return nil
}
func (f *fakeRebuildProjection) Search(context.Context, []retrieval.Route, corpus.Scope, retrieval.Request) ([]content.Candidate, error) {
	return nil, errors.New("unused")
}

// fakeDeriver stands for the pinned ingestion plugin that owns "space".
type fakeDeriver struct {
	err               error
	derived, segments int
	onDerive          func()
}

func (*fakeDeriver) Owns(_ context.Context, space string) bool { return space == "space" }
func (*fakeDeriver) Gone(context.Context, error) (*content.Diagnostic, error) {
	return nil, nil
}
func (d *fakeDeriver) segmentation(v content.Version) content.Segmentation {
	return content.Segmentation{ID: "plugin-seg-" + v.ID, VersionID: v.ID, Segments: []content.Segment{{ID: "plugin-segment-" + v.ID, PartKey: "body"}}}
}
func (d *fakeDeriver) Segment(_ context.Context, _, _ string, v content.Version, _ content.Generation) (content.Segmentation, error) {
	d.segments++
	return d.segmentation(v), d.err
}
func (d *fakeDeriver) Derive(_ context.Context, org, _ string, v content.Version, g content.Generation) (content.Segmentation, []content.EmbeddingData, error) {
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

func rebuilder(store *fakeRebuildStore, c *fakeRebuildContent, p *fakeRebuildProjection) retrieval.Rebuilder {
	return retrieval.Rebuilder{Store: store, Cancellation: store, Content: c, Projection: p, Plugin: &fakeDeriver{}, Routing: routedTo("space")}
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

// A target whose served space the pinned plugin does not own cannot be built.
func TestRebuildFailsForATargetNoPinnedPluginServes(t *testing.T) {
	store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1"}}, covered: map[string][]content.Embedding{}}
	r := rebuilder(store, &fakeRebuildContent{}, &fakeRebuildProjection{})
	r.Plugin = nil
	run(t, r)
	if store.activated || len(store.failed) != 1 || store.failed[0].Code != "unsupported_vector_space" {
		t.Fatalf("activated=%v failed=%v", store.activated, store.failed)
	}
}

func TestRebuildFailsWhenCanonicalTextArtifactIsLost(t *testing.T) {
	store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1"}}, covered: map[string][]content.Embedding{}}
	run(t, rebuilder(store, &fakeRebuildContent{versionErr: content.ErrArtifactCorrupt}, &fakeRebuildProjection{}))
	if store.activated || len(store.failed) != 1 || store.failed[0].Code != "canonical_content_unavailable" {
		t.Fatalf("activated=%v failed=%v", store.activated, store.failed)
	}
}

func TestRebuildRejectsSegmentationDifferingFromDurableArtifact(t *testing.T) {
	store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1"}}, covered: map[string][]content.Embedding{}, coverErr: content.ErrConflict}
	run(t, rebuilder(store, &fakeRebuildContent{}, &fakeRebuildProjection{}))
	if store.activated || len(store.failed) != 1 || store.failed[0].Code != "segmentation_mismatch" {
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

// A plugin refusal or a segmentation that differs from the stored one fails
// the rebuild; an outage retries it.
func TestRebuildPluginFailures(t *testing.T) {
	for code, err := range map[string]error{"ingestion_refused": fmt.Errorf("%w: terminal", content.ErrIngestionRefused), "segmentation_mismatch": content.ErrConflict} {
		store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1", VectorsRequired: true}}, covered: map[string][]content.Embedding{}}
		r := rebuilder(store, &fakeRebuildContent{}, &fakeRebuildProjection{})
		r.Plugin = &fakeDeriver{err: err}
		run(t, r)
		if store.activated || len(store.failed) != 1 || store.failed[0].Code != code || store.failed[0].Retryable {
			t.Fatalf("%s: activated=%v failed=%v", code, store.activated, store.failed)
		}
	}
	store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1"}}, covered: map[string][]content.Embedding{}}
	r := rebuilder(store, &fakeRebuildContent{}, &fakeRebuildProjection{})
	r.Plugin = &fakeDeriver{err: errors.New("plugin unavailable")}
	if _, err := r.Step(context.Background(), "org", "op"); err == nil || len(store.failed) != 0 {
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

// A rebuild shares the vector deadline budget with enrichment: outages retry
// freely; the third reached deadline fails the Operation without cutover.
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
	if err != nil || !done || len(store.failed) != 1 || store.failed[0].Code != content.CodeEnrichmentTimeout || store.activated {
		t.Fatalf("deadline budget: done=%v err=%v failures=%v activated=%v", done, err, store.failed, store.activated)
	}
}
