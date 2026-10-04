package backfill_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/backfill"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/processing"
)

const served, target = "p.small@1", "p.large@1"

// runStore records what a step committed.
type runStore struct {
	mu         sync.Mutex
	state      string
	registered string
	candidates []backfill.Candidate
	covered    map[string][]content.Embedding
	effectErr  error
	events     []string
}

func (s *runStore) BeginBackfill(_ context.Context, org, id, plan string) (backfill.Target, error) {
	spec := &operations.Backfill{RegistrationID: s.registered, Spaces: []string{target}, PlanID: plan}
	g := content.Generation{ID: "gen", SpaceID: served, SpacesProjected: true, Spaces: []content.GenerationSpace{{ID: served}, {ID: target}}}
	return backfill.Target{Operation: operations.Operation{ID: id, Organization: org, Kind: operations.KindBackfill, CorpusID: "corpus_a", State: s.state, Backfill: spec}, Generation: g}, nil
}
func (s *runStore) CarryBackfillSpaces(context.Context, string, string) (content.Generation, error) {
	return content.Generation{ID: "gen", SpaceID: served, SpacesProjected: true, Spaces: []content.GenerationSpace{{ID: served}, {ID: target}}}, nil
}
func (s *runStore) BackfillCandidates(_ context.Context, _, _ string, _ content.Generation, limit int) ([]backfill.Candidate, error) {
	s.events = append(s.events, "candidates")
	return s.candidates[:min(limit, len(s.candidates))], nil
}
func (s *runStore) CoveredEmbeddings(_ context.Context, _, _ string, seg content.Segmentation) ([]content.Embedding, error) {
	return s.covered[seg.VersionID], nil
}
func (s *runStore) CoverBackfill(_ context.Context, _, _ string, _ content.Generation, versionID string, artifacts []content.Embedding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.effectErr != nil {
		return s.effectErr
	}
	s.events = append(s.events, "cover "+versionID+" "+spacesOf(artifacts))
	return nil
}
func (s *runStore) CoverBackfillEvaluation(_ context.Context, _, _ string, _ content.Generation, versionID string, _ content.Segmentation, artifacts []content.Embedding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.effectErr != nil {
		return s.effectErr
	}
	s.events = append(s.events, "cover-evaluation "+versionID+" "+spacesOf(artifacts))
	return nil
}
func (s *runStore) SkipBackfill(_ context.Context, _, _, versionID, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "skip "+versionID+" "+code)
	return nil
}
func (s *runStore) CompleteBackfill(context.Context, string, string, string) error {
	s.events = append(s.events, "complete")
	return nil
}
func (s *runStore) FailBackfill(_ context.Context, _, _ string, failure operations.Error) error {
	if s.effectErr != nil {
		return s.effectErr
	}
	s.events = append(s.events, "fail "+failure.Code)
	return nil
}
func (s *runStore) ConfirmCancel(context.Context, string, string) error {
	s.events = append(s.events, "canceled")
	return nil
}

func spacesOf(artifacts []content.Embedding) string {
	var ids []string
	for _, a := range artifacts {
		ids = append(ids, a.SpaceID)
	}
	sort.Strings(ids)
	out := ""
	for _, id := range ids {
		out += "[" + id + "]"
	}
	return out
}

type runContent struct{}

func (runContent) TrustedVersion(_ context.Context, _, _ string, recordID, versionID string) (content.Version, error) {
	return content.Version{RecordID: recordID, ID: versionID}, nil
}
func (runContent) PluginSegmentationOf(_ context.Context, _ string, v content.Version, recipe string) (content.Segmentation, error) {
	return content.Segmentation{ID: "seg_" + v.ID, VersionID: v.ID, Recipe: recipe, Segments: []content.Segment{{ID: "s_" + v.ID}}}, nil
}
func (runContent) LoadEmbedding(_ context.Context, _, derivation string) (content.Embedding, []float32, error) {
	return content.Embedding{DerivationID: derivation}, []float32{1}, nil
}

// runPlugin answers per Version: an error, or a vector in the target space.
type runPlugin struct {
	errs map[string]error
	gone *content.Diagnostic
}

func (p runPlugin) Owns(_ context.Context, space string) bool {
	return space == served || space == target
}
func (p runPlugin) Fill(_ context.Context, org, _ string, v content.Version, seg content.Segmentation, spaces []string) ([]content.EmbeddingData, error) {
	if err := p.errs[v.ID]; err != nil {
		return nil, err
	}
	return []content.EmbeddingData{{Artifact: content.Embedding{Organization: org, VersionID: v.ID, SegmentID: seg.Segments[0].ID, SpaceID: spaces[0]}, Vector: []float32{2}}}, nil
}
func (p runPlugin) FillIndependent(_ context.Context, org, _ string, v content.Version, spaces []string) (content.Segmentation, []content.EmbeddingData, error) {
	if err := p.errs[v.ID]; err != nil {
		return content.Segmentation{}, nil, err
	}
	seg := content.Segmentation{ID: "independent_" + v.ID, VersionID: v.ID, Recipe: "plugin:p@1", Segments: []content.Segment{{ID: "independent_segment_" + v.ID}}}
	return seg, []content.EmbeddingData{{Artifact: content.Embedding{Organization: org, VersionID: v.ID, SegmentID: seg.Segments[0].ID, SpaceID: spaces[0]}, Vector: []float32{3}}}, nil
}
func (p runPlugin) Gone(context.Context, error) (*content.Diagnostic, error) { return p.gone, nil }

type runProjection struct {
	mu           sync.Mutex
	published    []string
	events       []string
	publishedIDs chan string
}

func (p *runProjection) Publish(_ context.Context, _ content.Generation, _, _, namespace string, _ content.Version, _ content.Segmentation) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, "publish "+namespace)
	return nil
}

func (p *runProjection) PublishEmbeddings(_ context.Context, _ content.Generation, _ string, data []content.EmbeddingData) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	artifacts := make([]content.Embedding, len(data))
	for i, d := range data {
		artifacts[i] = d.Artifact
	}
	p.published = append(p.published, spacesOf(artifacts))
	p.events = append(p.events, "vectors")
	if p.publishedIDs != nil {
		p.publishedIDs <- data[0].Artifact.VersionID
	}
	return nil
}

type pinnedPlan struct{ registration string }

func (p pinnedPlan) Ingestion(context.Context, string) (string, string, error) {
	return "plan_1", p.registration, nil
}

// One step fills each candidate in the target space and republishes its
// segment with the vectors of the other spaces it already has; Versions it
// cannot fill are skipped with their reason; a stopped plugin, a changed
// plan, a pause and a cancellation end the step without effects.
func TestStepFillsSkipsAndStops(t *testing.T) {
	candidates := []backfill.Candidate{{RecordID: "r1", VersionID: "v1", Recipe: "plugin:p@1"}, {RecordID: "r2", VersionID: "v2", Recipe: "plugin:p@1"}}
	unavailable := errors.New("plugin unreachable")
	for _, tc := range []struct {
		name         string
		state        string
		registration string
		candidates   []backfill.Candidate
		errs         map[string]error
		gone         *content.Diagnostic
		effectErr    error
		done         bool
		events       []string
		published    []string
	}{
		{"fills and keeps the other spaces", operations.StateRunning, "reg", candidates, nil, nil, nil, false,
			[]string{"candidates", "cover v1 [p.large@1]", "cover v2 [p.large@1]"}, []string{"[p.large@1][p.small@1]", "[p.large@1][p.small@1]"}},
		{"skips what only a rebuild can fill", operations.StateRunning, "reg", candidates, map[string]error{"v1": processing.ErrSegmentsDiffer, "v2": content.ErrIngestionRefused}, nil, nil, false,
			[]string{"candidates", "skip v1 segmentation_differs", "skip v2 ingestion_refused"}, nil},
		{"skips a Version the plugin cannot embed in time", operations.StateRunning, "reg", candidates[:1], map[string]error{"v1": processing.ErrPluginDeadline}, nil, nil, false,
			[]string{"candidates", "skip v1 plugin_deadline"}, nil},
		{"skips a stored vector no retry can read", operations.StateRunning, "reg", candidates[:1], map[string]error{"v1": content.ErrConflict}, nil, nil, false,
			[]string{"candidates", "skip v1 artifact_unavailable"}, nil},
		{"completes when nothing is left", operations.StateRunning, "reg", nil, nil, nil, nil, true, []string{"candidates", "complete"}, nil},
		{"fails when the pinned plugin is gone", operations.StateRunning, "reg", candidates, map[string]error{"v1": unavailable}, &content.Diagnostic{Code: "pinned_plugin_unavailable"}, nil, true,
			[]string{"candidates", "fail pinned_plugin_unavailable"}, nil},
		{"keeps a terminal failure resumable after a pause", operations.StateRunning, "reg", candidates, map[string]error{"v1": unavailable}, &content.Diagnostic{Code: "pinned_plugin_unavailable"}, operations.ErrNotRunning, false, []string{"candidates"}, nil},
		{"fails when the plan names another plugin", operations.StateRunning, "reg_other", candidates, nil, nil, nil, true, []string{"fail plan_changed"}, nil},
		{"stops at a pause taken meanwhile", operations.StateRunning, "reg", candidates, nil, nil, operations.ErrNotRunning, false, []string{"candidates"}, []string{"[p.large@1][p.small@1]"}},
		{"waits while paused", operations.StatePaused, "reg", candidates, nil, nil, nil, false, nil, nil},
		{"settles a cancellation", operations.StateCancelRequested, "reg", candidates, nil, nil, nil, true, []string{"canceled"}, nil},
	} {
		store := &runStore{state: tc.state, registered: "reg", candidates: tc.candidates, effectErr: tc.effectErr, covered: map[string][]content.Embedding{
			// v1 already holds a stale target vector, which is replaced.
			"v1": {{SpaceID: served, DerivationID: "d1"}, {SpaceID: target, DerivationID: "d1_old"}},
			"v2": {{SpaceID: served, DerivationID: "d2"}},
		}}
		projection := &runProjection{}
		b := backfill.Backfiller{Store: store, Cancellation: store, Content: runContent{}, Plugin: runPlugin{errs: tc.errs, gone: tc.gone}, Projection: projection, Pinned: pinnedPlan{tc.registration}, Settings: backfill.Settings{Rate: 100, Concurrency: 1, Poll: 3 * time.Second}}
		progress, err := b.Step(context.Background(), "org", "op")
		if err != nil || progress.Done != tc.done {
			t.Errorf("%s: progress %+v %v", tc.name, progress, err)
		}
		if tc.state == operations.StatePaused && progress.Wait != 3*time.Second {
			t.Errorf("%s: waits %v, want the poll interval", tc.name, progress.Wait)
		}
		if !equal(store.events, tc.events) || !equal(projection.published, tc.published) {
			t.Errorf("%s: events %v published %v, want %v %v", tc.name, store.events, projection.published, tc.events, tc.published)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A step never outpaces the rate: after a batch it waits for the rest of the
// time the batch's Versions are allowed.
func TestStepPacesTheNextBatch(t *testing.T) {
	store := &runStore{state: operations.StateRunning, registered: "reg", candidates: []backfill.Candidate{{RecordID: "r1", VersionID: "v1"}, {RecordID: "r2", VersionID: "v2"}}, covered: map[string][]content.Embedding{}}
	b := backfill.Backfiller{Store: store, Cancellation: store, Content: runContent{}, Plugin: runPlugin{}, Projection: &runProjection{}, Pinned: pinnedPlan{"reg"}, Settings: backfill.Settings{Rate: 0.5}}
	progress, err := b.Step(context.Background(), "org", "op")
	// One Version per step at 0.5 per second: up to two seconds before the next.
	if err != nil || progress.Done || progress.Wait < time.Second || progress.Wait > 2*time.Second {
		t.Fatalf("progress %+v %v", progress, err)
	}
}

// An absent owner projection derives its own cuts, publishes the lexical
// anchor before vectors, and records the independent coverage seam.
func TestStepFillsIndependentOwnerInPublicationOrder(t *testing.T) {
	store := &runStore{
		state:      operations.StateRunning,
		registered: "reg",
		candidates: []backfill.Candidate{{RecordID: "r1", VersionID: "v1", Namespace: "docs", Independent: true}},
		covered:    map[string][]content.Embedding{},
	}
	projection := &runProjection{}
	b := backfill.Backfiller{
		Store: store, Cancellation: store,
		Content:    runContent{},
		Plugin:     runPlugin{},
		Projection: projection,
		Pinned:     pinnedPlan{"reg"},
		Settings:   backfill.Settings{Rate: 100},
	}
	progress, err := b.Step(context.Background(), "org", "op")
	if err != nil || progress.Done {
		t.Fatalf("progress %+v %v", progress, err)
	}
	if !equal(projection.events, []string{"publish docs", "vectors"}) {
		t.Fatalf("publication order %v", projection.events)
	}
	if !equal(store.events, []string{"candidates", "cover-evaluation v1 [p.large@1]"}) {
		t.Fatalf("store events %v", store.events)
	}
}

// Independent Versions overlap within the configured cap. A failed attempt
// cancels and joins in-flight work before the caller can retry the checkpoint.
type gatedPlugin struct {
	runPlugin
	entered      chan string
	release      chan struct{}
	firstRelease chan struct{}
	exited       chan string
	err          error
}

func (p gatedPlugin) FillIndependent(ctx context.Context, org, corpus string, v content.Version, spaces []string) (content.Segmentation, []content.EmbeddingData, error) {
	p.entered <- v.ID
	defer func() { p.exited <- v.ID }()
	release := p.release
	if v.ID == "0" {
		release = p.firstRelease
	}
	select {
	case <-ctx.Done():
		return content.Segmentation{}, nil, ctx.Err()
	case <-release:
	}
	if p.err != nil && v.ID == "0" {
		return content.Segmentation{}, nil, p.err
	}
	return p.runPlugin.FillIndependent(ctx, org, corpus, v, spaces)
}
func TestStepBoundsConcurrentVersionsAndJoinsFailures(t *testing.T) {
	for _, failure := range []error{nil, errors.New("dependency down")} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			candidates := []backfill.Candidate{}
			for i := 0; i < 5; i++ {
				candidates = append(candidates, backfill.Candidate{RecordID: fmt.Sprint(i), VersionID: fmt.Sprint(i), Independent: true})
			}
			store := &runStore{state: operations.StateRunning, registered: "reg", candidates: candidates}
			plugin := gatedPlugin{entered: make(chan string, 5), release: make(chan struct{}), firstRelease: make(chan struct{}), exited: make(chan string, 5), err: failure}
			projection := &runProjection{publishedIDs: make(chan string, 5)}
			b := backfill.Backfiller{Store: store, Cancellation: store, Content: runContent{}, Plugin: plugin, Projection: projection, Pinned: pinnedPlan{"reg"}, Settings: backfill.Settings{Rate: 100, Concurrency: 2}}
			result := make(chan error, 1)
			go func() { _, err := b.Step(ctx, "org", "op"); result <- err }()
			for i := 0; i < 2; i++ {
				select {
				case <-plugin.entered:
				case <-ctx.Done():
					t.Fatal("second Version never entered while the first was in flight")
				}
			}
			select {
			case id := <-plugin.entered:
				t.Fatalf("Version %s exceeded concurrency cap", id)
			default:
			}
			close(plugin.release)
			select {
			case id := <-projection.publishedIDs:
				if id != "1" {
					t.Fatalf("prepared %s before releasing the first Version", id)
				}
			case <-ctx.Done():
				t.Fatal("second Version did not prepare")
			}
			store.mu.Lock()
			committed := len(store.events) != 1
			store.mu.Unlock()
			if committed {
				t.Fatal("checkpoint passed the unfinished first Version")
			}
			close(plugin.firstRelease)
			var err error
			select {
			case err = <-result:
			case <-ctx.Done():
				t.Fatal("step did not settle")
			}
			if !errors.Is(err, failure) {
				t.Fatalf("step error %v, want %v", err, failure)
			}
			if len(plugin.entered) != len(plugin.exited)-2 {
				t.Fatalf("returned with in-flight work: entered after first two %d, exited %d", len(plugin.entered), len(plugin.exited))
			}
			want := []string{"candidates"}
			if failure == nil {
				for i := 0; i < 5; i++ {
					want = append(want, fmt.Sprintf("cover-evaluation %d [p.large@1]", i))
				}
			}
			if !equal(store.events, want) {
				t.Fatalf("committed %v, want ordered checkpoints %v", store.events, want)
			}
		})
	}
}
