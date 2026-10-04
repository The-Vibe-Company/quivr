package x_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/tests/fakes/process"
)

// THE-919's owner moved here with the single provider implementation: earlier
// versions stay available for lookup, with full history; timelines show latest.
func TestFakeXKeepsEditedPostsAvailableByEarlierIDs(t *testing.T) {
	srv := process.Start(t, "x")
	client := &http.Client{Timeout: 5 * time.Second}
	publish := func(id string, history []string) {
		t.Helper()
		post := map[string]any{"id": id, "text": "Example"}
		if len(history) > 0 {
			post["edit_history_tweet_ids"] = history
		}
		raw, _ := json.Marshal(map[string]any{"posts": []any{post}})
		resp, err := client.Post(srv.URL+"/_control/lists/77", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("publish: HTTP%d", resp.StatusCode)
		}
	}
	publish("100", nil)
	for _, history := range [][]string{{"100", "101"}, {"100", "101", "102"}} {
		latest := history[len(history)-1]
		publish(latest, history)
		for _, path := range []string{"/2/tweets?ids=" + strings.Join(history, ","), "/2/lists/77/tweets?expansions=author_id&tweet.fields=edit_history_tweet_ids"} {
			req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
			req.Header.Set("Authorization", "Bearer x-test-token")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Data []struct {
					ID      string   `json:"id"`
					History []string `json:"edit_history_tweet_ids"`
				} `json:"data"`
				Errors []json.RawMessage `json:"errors"`
			}
			err = json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != 200 {
				t.Fatalf("%s: HTTP%d", path, resp.StatusCode)
			}
			wantIDs := history
			if strings.HasPrefix(path, "/2/lists/") {
				wantIDs = []string{latest}
			}
			var gotIDs []string
			for _, post := range body.Data {
				gotIDs = append(gotIDs, post.ID)
				if !slices.Equal(post.History, history) {
					t.Fatalf("%s: post%s history%v, want%v", path, post.ID, post.History, history)
				}
			}
			if len(body.Errors) != 0 || !slices.Equal(gotIDs, wantIDs) {
				t.Fatalf("%s: IDs%v, errors%s; want IDs%v without errors", path, gotIDs, body.Errors, wantIDs)
			}
		}
	}
}
