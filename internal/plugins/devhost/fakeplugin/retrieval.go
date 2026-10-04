package fakeplugin

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
)

// retrievalRoutes serves the retrieval Contribution. The well-behaved plugin
// asks in round 1 for keyword candidates and, when a space is offered, vector
// candidates in the first one, then fuses what was served by reciprocal rank.
// Broken modes of tests/plugin-contract: retrieval-unserved (ranks a segment
// never served), retrieval-endless (never ranks), retrieval-too-many-requests,
// retrieval-nondeterministic (scores depend on the invocation),
// retrieval-slow (answers every profile but default after the client gave up), retrieval-over-budget
// (reports more cost than any profile allows), retrieval-secret-leak (echoes
// the first declared secret in an explanation) and accept-invalid.
func retrievalRoutes(mux *http.ServeMux, mode string, m *plugins.Manifest, write func(http.ResponseWriter, int, any)) {
	contribution := m.Contributions.Retrieval
	mux.HandleFunc("POST "+plugins.SearchRoute, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request plugins.SearchRequest
		invalid := ""
		if issues := plugins.ValidateDocument("retrieval-search-request.schema.json", body); len(issues) > 0 {
			invalid = issues[0].Path + " " + issues[0].Message
		} else if _ = json.Unmarshal(body, &request); contribution.Profiles[request.Profile] == (plugins.RetrievalProfile{}) {
			invalid = fmt.Sprintf("profile %q is not declared", request.Profile)
		}
		if invalid != "" {
			slog.Warn("invalid retrieval request", "detail", invalid)
			status := 400
			if mode == "accept-invalid" {
				status = 202
			}
			write(w, status, map[string]any{"code": "invalid_request", "message": invalid, "retryable": false})
			return
		}
		if mode == "retrieval-slow" && request.Profile != plugins.DefaultProfile {
			<-r.Context().Done()
			return
		}

		if mode == "retrieval-compose" {
			if request.Round == 1 {
				name := m.Requires[0].Plugin + "/" + m.Requires[0].Profiles[0]
				write(w, 200, map[string]any{"requests": []any{map[string]any{"primitive": "profile", "profile": map[string]any{"name": name, "query": "rewritten query", "limit": request.Limit}}}})
				return
			}
			hits := []plugins.RankedHit{}
			candidates := request.Served[0].Candidates
			for i := len(candidates) - 1; i >= 0; i-- {
				c := candidates[i]
				hits = append(hits, plugins.RankedHit{SegmentID: c.SegmentID, Score: c.Score, Explanation: "reordered: " + c.Explanation})
			}
			write(w, 200, map[string]any{"ranking": map[string]any{"hits": hits}, "usage": map[string]any{"paid_calls": 1, "cost_cents": 0.25}})
			return
		}
		k := min(10, contribution.Limits.MaxCandidates)
		if request.Round == 1 || mode == "retrieval-endless" || mode == "retrieval-too-many-requests" {
			requests := []any{map[string]any{"primitive": "bm25", "query_text": request.Query.Text, "k": k}}
			if len(request.Spaces) > 0 {
				requests = append(requests, map[string]any{"primitive": "near_vector", "space": request.Spaces[0].ID, "query_text": request.Query.Text, "k": k})
			}
			for mode == "retrieval-too-many-requests" && len(requests) <= contribution.Limits.MaxRequests {
				requests = append(requests, requests[0])
			}
			write(w, 200, map[string]any{"requests": requests})
			return
		}
		scores := map[string]float64{}
		for _, s := range request.Served {
			for rank, c := range s.Candidates {
				scores[c.SegmentID] += 1 / float64(60+rank+1)
			}
		}
		ids := make([]string, 0, len(scores))
		for id := range scores {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			if scores[ids[i]] != scores[ids[j]] {
				return scores[ids[i]] > scores[ids[j]]
			}
			return ids[i] < ids[j]
		})
		if mode == "retrieval-unserved" {
			ids = append([]string{"never-served"}, ids...)
		}
		hits := []any{}
		for _, id := range ids[:min(len(ids), request.Limit)] {
			hit := map[string]any{"segment_id": id, "score": scores[id], "explanation": m.ID + ": reciprocal rank fusion"}
			switch mode {
			case "retrieval-nondeterministic":
				hit["explanation"] = "invocation " + request.InvocationID
			case "retrieval-secret-leak":
				if len(m.Secrets) > 0 {
					hit["explanation"] = "key " + os.Getenv(m.Secrets[0].Name)
				}
			}
			hits = append(hits, hit)
		}
		answer := map[string]any{"ranking": map[string]any{"hits": hits}}
		if mode == "retrieval-paid" {
			answer["usage"] = map[string]any{"paid_calls": 1, "cost_cents": 0.125}
		}
		if mode == "retrieval-over-budget" {
			answer["usage"] = map[string]any{"paid_calls": 1, "cost_cents": 100}
		}
		write(w, 200, answer)
	})
}
