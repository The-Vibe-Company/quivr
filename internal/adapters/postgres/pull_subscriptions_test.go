package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/changes"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/monitoring"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost/fakeplugin"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
)

// One owner scenario exercises the HTTP contract and real durable writes, both
// alone and in a batch containing a webhook subscription. No network attempts
// or wall-clock waits are needed to prove that a pull-only alert sends nothing.
func TestSubscriptionsWithoutDestination(t *testing.T) {
	for _, batch := range []bool{false, true} {
		name := "single"
		if batch {
			name = "mixed_batch"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			f := ownerFixture{newEditFixture(t, ctx, "adapter-pull-")}
			f.scope.Actions = append(f.scope.Actions, "changes:read")
			cursorKey := []byte("adapter-pull-cursor-key-012345678901")
			h, err := httpapi.New(postgres.Store{Pool: f.pool}, f.contents, retrieval.Service{}, uploads.Service{},
				map[string]corpus.Scope{"key": f.scope}, cursorKey, httpapi.WithMonitoring(f.service),
				httpapi.WithChanges(changes.Service{Corpora: postgres.Store{Pool: f.pool}, Journal: f.store, Key: cursorKey}, 0))
			if err != nil {
				t.Fatal(err)
			}
			call := func(method, path string, body any, want int) map[string]any {
				t.Helper()
				payload, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				r := httptest.NewRequest(method, path, bytes.NewReader(payload)).WithContext(ctx)
				r.Header.Set("Authorization", "Bearer key")
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != want {
					t.Fatalf("%s %s: want %d, got %d: %s", method, path, want, w.Code, w.Body.String())
				}
				var out map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				return out
			}
			pin := map[string]any{"plugin_id": fakeplugin.FixtureEvaluator, "version": fakeplugin.FixtureEvaluatorVersion,
				"configuration": map[string]any{"decisions": map[string]any{"default": "match"}}}
			body := map[string]any{"idempotency_key": "pull", "name": "Pull alert", "saved_query_id": f.query.ID,
				"saved_query_version_id": f.query.Current.VersionID, "evaluator": pin}
			created := call("POST", "/v0/subscriptions", body, 201)
			subID := created["subscription_id"].(string)
			if _, has := created["current_version"].(map[string]any)["destination_id"]; has {
				t.Fatal("pull-only Version exposes a destination", created)
			}
			if replay := call("POST", "/v0/subscriptions", body, 201); replay["subscription_id"] != subID {
				t.Fatal("pull-only replay changed identity", replay)
			}
			path := "/v0/subscriptions/" + subID
			// Full Version replacement can add and then remove delivery; omission
			// must not silently retain the previous destination.
			edit := map[string]any{"idempotency_key": "push", "saved_query_version_id": f.query.Current.VersionID,
				"evaluator": pin, "destination_id": "dest"}
			pushed := call("POST", path+"/versions", edit, 201)
			delete(edit, "destination_id")
			edit["idempotency_key"] = "pull-again"
			pulled := call("POST", path+"/versions", edit, 201)
			if _, has := pulled["destination_id"]; has {
				t.Fatal("omitting the destination retained delivery", pulled)
			}
			if old := call("GET", path+"/versions/"+pushed["version_id"].(string), nil, 200); old["destination_id"] != "dest" {
				t.Fatal("historical destination changed", old)
			}
			sub, err := f.service.Subscription(ctx, f.scope, subID)
			if err != nil {
				t.Fatal(err)
			}
			var push monitoring.Subscription
			if batch {
				push = f.subscribe("webhook")
			}
			feedPath := "/v0/changes?corpus_id=" + url.QueryEscape(f.corpusID)
			start := call("GET", feedPath, nil, 200)["next_cursor"].(string)
			decide := func(version string, positive bool) {
				t.Helper()
				f.drain()
				in := f.intentsBySubscription(version)[subID]
				if in.SubscriptionID == "" {
					t.Fatal("pull-only Subscription was not dispatched", version)
				}
				if !positive {
					f.commit(monitoring.OutcomeNoLongerMatches, func() (string, error) { return f.evaluation.CommitNoMatch(ctx, in) })
					f.commit(monitoring.OutcomeDuplicate, func() (string, error) { return f.evaluation.CommitNoMatch(ctx, in) })
					return
				}
				if batch {
					inputs := []monitoring.MatchCommit{{Intent: in, Evidence: f.evidence(sub)},
						{Intent: f.intentsBySubscription(version)[push.ID], Evidence: f.evidence(push)}}
					for _, want := range []string{monitoring.OutcomeMatched, monitoring.OutcomeDuplicate} {
						out, err := f.evaluation.CommitMatches(ctx, inputs)
						if err != nil || len(out) != 2 || out[0] != want || out[1] != want {
							t.Fatalf("mixed batch: want %s, got %v %v", want, out, err)
						}
					}
				} else {
					f.commit(monitoring.OutcomeMatched, func() (string, error) { return f.evaluation.CommitMatch(ctx, in, f.evidence(sub)) })
					f.commit(monitoring.OutcomeDuplicate, func() (string, error) { return f.evaluation.CommitMatch(ctx, in, f.evidence(sub)) })
				}
			}
			record, v1 := f.publish("r", "r-1", "First text")
			decide(v1, true)
			_, v2 := f.publish("r", "r-2", "Corrected text")
			decide(v2, true)
			_, v3 := f.publish("r", "r-3", "Unmatched text")
			decide(v3, false)
			if _, err := f.contents.Withdraw(ctx, f.scope, content.Withdrawal{Key: "withdraw", Source: content.Source{
				CorpusID: f.corpusID, Namespace: "corrections", RecordKey: "r"}}); err != nil {
				t.Fatal(err)
			}
			f.drain()
			in := monitoring.Intent{Kind: monitoring.IntentWithdrawal, Organization: f.org}
			if err := f.pool.QueryRow(ctx, `SELECT subscription_id,subscription_version_id,sequence,corpus_id,record_id,record_version_id
FROM evaluation_intents WHERE organization=$1 AND kind='withdrawal' AND subscription_id=$2`, f.org, subID).Scan(
				&in.SubscriptionID, &in.SubscriptionVersionID, &in.Sequence, &in.CorpusID, &in.RecordID, &in.VersionID); err != nil {
				t.Fatal("pull-only withdrawal was not dispatched", err)
			}
			f.commit(monitoring.OutcomeWithdrawalNotified, func() (string, error) { return f.evaluation.CommitWithdrawal(ctx, in) })
			f.commit(monitoring.OutcomeDuplicate, func() (string, error) { return f.evaluation.CommitWithdrawal(ctx, in) })
			listed := call("GET", "/v0/matches?subscription_id="+subID, nil, 200)["items"].([]any)
			if len(listed) != 2 {
				t.Fatal("pull-only Match history", listed)
			}
			for _, item := range listed {
				m := item.(map[string]any)
				read := call("GET", "/v0/matches/"+m["match_id"].(string), nil, 200)
				if read["record_id"] != record {
					t.Fatal("Match detail lost the Record", read)
				}
			}
			seen := map[string]int{}
			for _, item := range call("GET", feedPath+"&cursor="+url.QueryEscape(start)+"&limit=100", nil, 200)["items"].([]any) {
				e := item.(map[string]any)
				refs, ok := e["monitoring"].(map[string]any)
				if !ok || refs["subscription_id"] != subID {
					continue
				}
				if _, has := refs["delivery_id"]; has {
					t.Fatal("pull-only notice fabricates a Delivery reference", e)
				}
				seen[e["type"].(string)]++
			}
			for _, kind := range []string{"match.created", "match.corrected", "match.no_longer_matches", "match.withdrawn"} {
				if seen[kind] != 1 {
					t.Fatalf("want one %s, got %v", kind, seen)
				}
			}
			if n := f.count(`SELECT count(*) FROM deliveries d JOIN matches m ON (m.organization,m.id)=(d.organization,d.match_id)
WHERE d.organization=$1 AND m.subscription_id=$2`, f.org, subID); n != 0 {
				t.Fatalf("pull-only alert has %d deliveries", n)
			}
			wantPush := 0
			if batch {
				wantPush = 2
			}
			if n := f.count(`SELECT count(*) FROM delivery_outbox WHERE organization=$1`, f.org); n != wantPush {
				t.Fatalf("want only %d webhook outbox rows, got %d", wantPush, n)
			}
		})
	}
}
