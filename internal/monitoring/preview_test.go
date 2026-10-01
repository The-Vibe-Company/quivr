package monitoring_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
)

// recentRecords lists articles newest first; each one's text is its record id.
type recentRecords struct {
	ids []string
	// asked records the Corpora and limit of the last listing.
	corpora []string
	limit   int
}

func (r *recentRecords) Recent(_ context.Context, _ string, corpora []string, _ time.Time, limit int) ([]monitoring.RecentVersion, error) {
	r.corpora, r.limit = corpora, limit
	var out []monitoring.RecentVersion
	for i, id := range r.ids {
		if i == limit {
			break
		}
		out = append(out, monitoring.RecentVersion{CorpusID: "corpus_a", RecordID: id, VersionID: "v_" + id, AcceptedAt: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC).Add(-time.Duration(i) * time.Hour)})
	}
	return out, nil
}

type articleText struct{}

func (articleText) Article(_ context.Context, _, _, recordID, _ string) (monitoring.Article, error) {
	return monitoring.Article{Parts: []monitoring.Part{{Key: "body", Role: "body", Text: recordID}}}, nil
}

// blocking decides like the fixture, except that it answers nothing for a
// Record whose text says "slow" until the preview's budget runs out.
type blocking struct{ monitoring.Fixture }

func (b blocking) Evaluate(ctx context.Context, batch monitoring.Batch) ([]monitoring.Outcome, error) {
	if batch.Article.Parts[0].Text == "slow" {
		<-ctx.Done()
		return nil, fmt.Errorf("%w: %v", monitoring.ErrEvaluatorUnavailable, ctx.Err())
	}
	return b.Fixture.Evaluate(ctx, batch)
}

// sent records the batches a preview sends.
type sent struct {
	monitoring.Fixture
	batches chan monitoring.Batch
}

func (s sent) Evaluate(ctx context.Context, batch monitoring.Batch) ([]monitoring.Outcome, error) {
	s.batches <- batch
	return s.Fixture.Evaluate(ctx, batch)
}

func previewService(ids ...string) (monitoring.Service, *memoryStore, *recentRecords) {
	s, store := service()
	recent := &recentRecords{ids: ids}
	s.Recent, s.Versions = recent, articleText{}
	s.Evaluators.(monitoring.Evaluators)["test.blocking@1"] = blocking{}
	return s, store, recent
}

func decisions(d map[string]any) monitoring.Evaluator {
	e := fixture()
	e.Configuration = map[string]any{"decisions": d}
	return e
}

func inline(corpora ...string) *monitoring.Definition {
	d := query("preview", corpora...).Definition
	return &d
}

// A preview judges the newest Records with the proposed pair, as a
// Subscription would, and reports what it would have matched; it reaches no
// store command, so nothing is saved and nothing can be delivered.
func TestPreviewJudgesRecentRecordsAndWritesNothing(t *testing.T) {
	ctx := context.Background()
	s, store, recent := previewService("strike-new", "calm", "wait", "strike-old")
	got, err := s.Preview(ctx, writer, monitoring.PreviewInput{Definition: inline("corpus_a"),
		Evaluator: decisions(map[string]any{"strike": "match", "wait": "not_ready"})})
	if err != nil {
		t.Fatal(err)
	}
	if recent.limit != monitoring.DefaultPreviewRecords || len(recent.corpora) != 1 || recent.corpora[0] != "corpus_a" {
		t.Fatalf("a preview without limit lists %d Records of the Saved Query's Corpora, got %d of %v", monitoring.DefaultPreviewRecords, recent.limit, recent.corpora)
	}
	if got.Evaluated != 4 || got.NotReady != 1 || !got.Complete || len(got.Matches) != 2 ||
		got.Matches[0].RecordID != "strike-new" || got.Matches[1].RecordID != "strike-old" || !got.Oldest.Equal(time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("want strike-new and strike-old matched, newest first, of 4 decided Records: %+v", got)
	}
	if ev := got.Matches[0].Evidence; ev.Evaluator.PluginID != monitoring.FixtureEvaluator || ev.Explanation == "" || len(ev.PartKeys) != 1 {
		t.Fatalf("a previewed match carries the evidence a Match would: %+v", ev)
	}
	if len(store.writes) != 0 || len(store.created) != 0 {
		t.Fatalf("a preview must write nothing, got writes %v and creations %v", store.writes, store.created)
	}

	// An existing Saved Query Version previews the same way, and the limit is capped.
	q, err := s.CreateSavedQuery(ctx, writer, query("existing", "corpus_a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Preview(ctx, writer, monitoring.PreviewInput{SavedQueryID: q.ID, SavedQueryVersionID: q.Current.VersionID, Evaluator: fixture(), Limit: 500}); err != nil {
		t.Fatal(err)
	}
	if recent.limit != monitoring.MaxPreviewRecords {
		t.Fatalf("a preview judges at most %d Records, listed %d", monitoring.MaxPreviewRecords, recent.limit)
	}
}

// Each evaluation carries one synthetic Subscription reference, so the
// plugin sees a preview, never a real Subscription.
func TestPreviewSendsASyntheticSubscription(t *testing.T) {
	s, _, _ := previewService("only")
	batches := make(chan monitoring.Batch, 1)
	s.Evaluators.(monitoring.Evaluators)["test.sent@1"] = sent{batches: batches}
	evaluator := monitoring.Evaluator{PluginID: "test.sent", Version: "1", Configuration: map[string]any{}}
	if _, err := s.Preview(context.Background(), writer, monitoring.PreviewInput{Definition: inline("corpus_a"), Evaluator: evaluator}); err != nil {
		t.Fatal(err)
	}
	b := <-batches
	ref := b.Items[0].Subscriptions
	if len(b.Items) != 1 || len(ref) != 1 || ref[0].SubscriptionID != monitoring.PreviewID || ref[0].SavedQueryID != monitoring.PreviewID || b.Items[0].Expression["fixture"] != "match" {
		t.Fatalf("want one evaluation of the proposed expression for the synthetic preview Subscription, got %+v", b.Items)
	}
}

// A preview validates the proposed pair as a Subscription creation does.
func TestPreviewRefusesWhatASubscriptionWould(t *testing.T) {
	ctx := context.Background()
	s, _, _ := previewService("a")
	both := inline("corpus_a")
	for name, tc := range map[string]struct {
		scope corpus.Scope
		in    monitoring.PreviewInput
		want  error
	}{
		"read-only key":        {reader, monitoring.PreviewInput{Definition: inline("corpus_a"), Evaluator: fixture()}, monitoring.ErrForbidden},
		"Corpus not granted":   {narrow, monitoring.PreviewInput{Definition: inline("corpus_b"), Evaluator: fixture()}, monitoring.ErrForbidden},
		"no Saved Query":       {writer, monitoring.PreviewInput{Evaluator: fixture()}, monitoring.ErrUnknownSavedQuery},
		"two Saved Queries":    {writer, monitoring.PreviewInput{Definition: both, SavedQueryID: "sq", SavedQueryVersionID: "v", Evaluator: fixture()}, monitoring.ErrUnknownSavedQuery},
		"unknown Saved Query":  {writer, monitoring.PreviewInput{SavedQueryID: "sq", SavedQueryVersionID: "v", Evaluator: fixture()}, monitoring.ErrUnknownSavedQuery},
		"evaluator not pinned": {writer, monitoring.PreviewInput{Definition: inline("corpus_a"), Evaluator: monitoring.Evaluator{PluginID: "acme.other", Version: "1"}}, monitoring.ErrUnsupportedEvaluator},
		"expression refused by the plugin schema": {writer, monitoring.PreviewInput{Definition: inline("corpus_a"),
			Evaluator: monitoring.Evaluator{PluginID: "acme.alerts", Version: "0.1.0"}}, monitoring.ErrInvalidExpression},
	} {
		if _, err := s.Preview(ctx, tc.scope, tc.in); !errors.Is(err, tc.want) {
			t.Errorf("%s: want %v, got %v", name, tc.want, err)
		}
	}
}

// An evaluator error fails the whole preview, never a partial "no match";
// Records still undecided when the time budget runs out are left out.
func TestPreviewFailuresAndBudget(t *testing.T) {
	ctx := context.Background()
	s, _, _ := previewService("strike", "slow", "boom")
	s.PreviewBudget = 100 * time.Millisecond
	if _, err := s.Preview(ctx, writer, monitoring.PreviewInput{Definition: inline("corpus_a"), Evaluator: decisions(map[string]any{"boom": "error"})}); !errors.Is(err, monitoring.ErrPreviewFailed) {
		t.Fatalf("an evaluation the plugin cannot decide fails the preview with evaluator_error, got %v", err)
	}
	unavailable := monitoring.Evaluator{PluginID: "acme.alerts", Version: "0.1.0", Configuration: map[string]any{}}
	withText := inline("corpus_a")
	withText.Expression = map[string]any{"text": "x"}
	if _, err := s.Preview(ctx, writer, monitoring.PreviewInput{Definition: withText, Evaluator: unavailable}); !errors.Is(err, monitoring.ErrPreviewUnavailable) {
		t.Fatalf("an unreachable evaluator fails the preview with evaluator_unavailable, got %v", err)
	}
	slow := monitoring.Evaluator{PluginID: "test.blocking", Version: "1", Configuration: map[string]any{"decisions": map[string]any{"strike": "match"}}}
	got, err := s.Preview(ctx, writer, monitoring.PreviewInput{Definition: inline("corpus_a"), Evaluator: slow})
	if err != nil || got.Complete || got.Evaluated != 2 || len(got.Matches) != 1 || got.Matches[0].RecordID != "strike" {
		t.Fatalf("want the two Records decided in time, strike matched, and complete false; got %+v, %v", got, err)
	}
}
