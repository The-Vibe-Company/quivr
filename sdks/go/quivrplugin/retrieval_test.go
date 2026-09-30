package quivrplugin_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/sdks/go/quivrplugin"
)

// deadlineRecorder answers an empty ranking and records whether its context
// had a deadline.
type deadlineRecorder struct{ bounded, called bool }

func (d *deadlineRecorder) Search(ctx context.Context, _ *quivrplugin.SearchRequest) (*quivrplugin.SearchAnswer, error) {
	_, d.bounded = ctx.Deadline()
	d.called = true
	return &quivrplugin.SearchAnswer{Ranking: []quivrplugin.RankedHit{}}, nil
}

// A profile's max_latency_ms is its latency objective, not a deadline
// (THE-813): the SDK does not cut a round there. The engine ends the request,
// and so the round's context, at the profile's hard bound.
func TestSearchRoundsAreNotCutAtTheObjective(t *testing.T) {
	plugin, err := quivrplugin.New("../examples/fusion-retriever/quivr-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	recorder := &deadlineRecorder{}
	if err = plugin.Retrieval(recorder); err != nil {
		t.Fatal(err)
	}
	handler, err := plugin.Handler()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("../../../contracts/plugins/v0/fixtures/requests/retrieval/search-round-1.json")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v0/contributions/retrieval/search", bytes.NewReader(body)))
	if rec.Code != 200 || !recorder.called {
		t.Fatalf("search answered %d %s", rec.Code, rec.Body)
	}
	if recorder.bounded {
		t.Fatal("the SDK gave the round a deadline of its own; only the engine's request bounds it")
	}
}
