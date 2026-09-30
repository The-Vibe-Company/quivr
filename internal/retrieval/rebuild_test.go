package retrieval_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
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
	loadErr    error
	loads      int
	onLoad     func()
}

func (f *fakeRebuildContent) Version(_ context.Context, _ corpus.Scope, recordID, id string) (content.Version, error) {
	if f.versionErr != nil {
		return content.Version{}, f.versionErr
	}
	return content.Version{ID: id, RecordID: recordID}, nil
}
func (f *fakeRebuildContent) LoadEmbedding(_ context.Context, org, derivation string) (content.Embedding, []float32, error) {
	f.loads++
	if f.onLoad != nil {
		f.onLoad()
	}
	if f.loadErr != nil {
		return content.Embedding{}, nil, f.loadErr
	}
	return content.Embedding{ID: "artifact-" + derivation, Organization: org, SpaceID: "space"}, []float32{1}, nil
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

type fixedIdentity struct{}

func (fixedIdentity) Space() content.VectorSpace { return content.VectorSpace{ID: "space"} }
func (fixedIdentity) Producer() string           { return "fixture-producer" }

func segmenter(_ context.Context, _ string, v content.Version) (content.Segmentation, error) {
	return content.Segmentation{ID: "seg-" + v.ID, VersionID: v.ID, Segments: []content.Segment{{ID: "segment-" + v.ID, PartKey: "body"}}}, nil
}

func rebuilder(store *fakeRebuildStore, c *fakeRebuildContent, p *fakeRebuildProjection) retrieval.Rebuilder {
	return retrieval.Rebuilder{Store: store, Content: c, Projection: p, Segment: segmenter, Artifacts: fixedIdentity{}}
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

func TestRebuildReusesStoredVectorsAndActivates(t *testing.T) {
	store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1", VectorsRequired: true}, {RecordID: "r2", VersionID: "v2"}}, covered: map[string][]content.Embedding{}}
	c, p := &fakeRebuildContent{}, &fakeRebuildProjection{}
	run(t, rebuilder(store, c, p))
	if !store.activated || len(store.failed) != 0 {
		t.Fatalf("activated=%v failed=%v", store.activated, store.failed)
	}
	if len(store.covered["v1"]) != 1 || p.lexical != 2 || p.vectors != 2 {
		t.Fatalf("covered=%v projection=%+v", store.covered, p)
	}
}

func TestRebuildFailsWithoutInferenceWhenRequiredVectorsAreMissing(t *testing.T) {
	for name, loadErr := range map[string]error{"embedding_artifact_unavailable": corpus.ErrNotFound, "embedding_artifact_corrupt": content.ErrConflict} {
		store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1", VectorsRequired: true}}, covered: map[string][]content.Embedding{}}
		run(t, rebuilder(store, &fakeRebuildContent{loadErr: loadErr}, &fakeRebuildProjection{}))
		if store.activated || len(store.failed) != 1 || store.failed[0].Code != name || store.failed[0].Retryable {
			t.Fatalf("%s: activated=%v failed=%v", name, store.activated, store.failed)
		}
	}
	// Object-storage integrity failures of the stored vector bytes are terminal too.
	for name, loadErr := range map[string]error{"embedding_artifact_unavailable": fmt.Errorf("read: %w", content.ErrArtifactMissing), "embedding_artifact_corrupt": fmt.Errorf("read: %w", content.ErrArtifactCorrupt)} {
		store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1", VectorsRequired: true}}, covered: map[string][]content.Embedding{}}
		run(t, rebuilder(store, &fakeRebuildContent{loadErr: loadErr}, &fakeRebuildProjection{}))
		if store.activated || len(store.failed) != 1 || store.failed[0].Code != name || store.failed[0].Retryable {
			t.Fatalf("%s: activated=%v failed=%v", name, store.activated, store.failed)
		}
	}
}

func TestRebuildFailsWhenCanonicalTextArtifactIsLost(t *testing.T) {
	store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1"}}, covered: map[string][]content.Embedding{}}
	run(t, rebuilder(store, &fakeRebuildContent{versionErr: content.ErrArtifactCorrupt}, &fakeRebuildProjection{}))
	if store.activated || len(store.failed) != 1 || store.failed[0].Code != "canonical_content_unavailable" {
		t.Fatalf("activated=%v failed=%v", store.activated, store.failed)
	}
}

func TestRebuildKeepsNotYetEnrichedVersionsLexical(t *testing.T) {
	store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1"}}, covered: map[string][]content.Embedding{}}
	p := &fakeRebuildProjection{}
	run(t, rebuilder(store, &fakeRebuildContent{loadErr: corpus.ErrNotFound}, p))
	if !store.activated || len(store.covered["v1"]) != 0 || p.vectors != 0 {
		t.Fatalf("activated=%v covered=%v", store.activated, store.covered)
	}
}

func TestRebuildTransientArtifactFailureRetriesWithoutFailing(t *testing.T) {
	store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1", VectorsRequired: true}}, covered: map[string][]content.Embedding{}}
	_, err := rebuilder(store, &fakeRebuildContent{loadErr: errors.New("object storage unavailable")}, &fakeRebuildProjection{}).Step(context.Background(), "org", "op")
	if err == nil || len(store.failed) != 0 {
		t.Fatalf("transient error %v recorded failures %v", err, store.failed)
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
	if err != nil || !done || store.state != operations.StateCanceled || p.lexical != 0 || c.loads != 0 {
		t.Fatalf("done=%v err=%v state=%s projection=%+v loads=%d", done, err, store.state, p, c.loads)
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

// A terminal failure discovered after cancellation was requested settles as
// canceled: the earlier operator request wins over the later failure.
func TestRebuildTerminalFailureAfterCancelRequestSettlesCanceled(t *testing.T) {
	store := &fakeRebuildStore{candidates: []retrieval.RebuildCandidate{{RecordID: "r1", VersionID: "v1", VectorsRequired: true}}, covered: map[string][]content.Embedding{}}
	c := &fakeRebuildContent{loadErr: corpus.ErrNotFound, onLoad: func() { store.state = operations.StateCancelRequested }}
	run(t, rebuilder(store, c, &fakeRebuildProjection{}))
	if store.activated || store.state != operations.StateCanceled || len(store.failed) != 0 {
		t.Fatalf("activated=%v state=%s failed=%v", store.activated, store.state, store.failed)
	}
}
