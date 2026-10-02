package backfill_test

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/backfill"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	"github.com/The-Vibe-Company/quivr-v2/internal/processing"
)

const served, target = "p.small@1", "p.large@1"

// runStore records what a step committed.
type runStore struct {
	state      string
	registered string
	candidates []backfill.Candidate
	covered    map[string][]content.Embedding
	coverErr   error
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
	if s.coverErr != nil {
		return s.coverErr
	}
	s.events = append(s.events, "cover "+versionID+" "+spacesOf(artifacts))
	return nil
}
func (s *runStore) SkipBackfill(_ context.Context, _, _, versionID, code string) error {
	s.events = append(s.events, "skip "+versionID+" "+code)
	return nil
}
func (s *runStore) CompleteBackfill(context.Context, string, string, string) error {
	s.events = append(s.events, "complete")
	return nil
}
func (s *runStore) FailBackfill(_ context.Context, _, _ string, failure operations.Error) error {
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

func (runContent) Version(_ context.Context, _ corpus.Scope, recordID, versionID string) (content.Version, error) {
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
func (p runPlugin) Gone(context.Context, error) (*content.Diagnostic, error) { return p.gone, nil }

type runProjection struct{ published []string }

func (p *runProjection) PublishEmbeddings(_ context.Context, _ content.Generation, _ string, data []content.EmbeddingData) error {
	artifacts := make([]content.Embedding, len(data))
	for i, d := range data {
		artifacts[i] = d.Artifact
	}
	p.published = append(p.published, spacesOf(artifacts))
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
		coverErr     error
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
		{"fails when the plan names another plugin", operations.StateRunning, "reg_other", candidates, nil, nil, nil, true, []string{"fail plan_changed"}, nil},
		{"stops at a pause taken meanwhile", operations.StateRunning, "reg", candidates, nil, nil, operations.ErrNotRunning, false, []string{"candidates"}, []string{"[p.large@1][p.small@1]"}},
		{"waits while paused", operations.StatePaused, "reg", candidates, nil, nil, nil, false, nil, nil},
		{"settles a cancellation", operations.StateCancelRequested, "reg", candidates, nil, nil, nil, true, []string{"canceled"}, nil},
	} {
		store := &runStore{state: tc.state, registered: "reg", candidates: tc.candidates, coverErr: tc.coverErr, covered: map[string][]content.Embedding{
			// v1 already holds a stale target vector, which is replaced.
			"v1": {{SpaceID: served, DerivationID: "d1"}, {SpaceID: target, DerivationID: "d1_old"}},
			"v2": {{SpaceID: served, DerivationID: "d2"}},
		}}
		projection := &runProjection{}
		b := backfill.Backfiller{Store: store, Content: runContent{}, Plugin: runPlugin{errs: tc.errs, gone: tc.gone}, Projection: projection, Pinned: pinnedPlan{tc.registration}, Settings: backfill.Settings{Rate: 100, Poll: 3 * time.Second}}
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
	b := backfill.Backfiller{Store: store, Content: runContent{}, Plugin: runPlugin{}, Projection: &runProjection{}, Pinned: pinnedPlan{"reg"}, Settings: backfill.Settings{Rate: 0.5}}
	progress, err := b.Step(context.Background(), "org", "op")
	// One Version per step at 0.5 per second: up to two seconds before the next.
	if err != nil || progress.Done || progress.Wait < time.Second || progress.Wait > 2*time.Second {
		t.Fatalf("progress %+v %v", progress, err)
	}
}
