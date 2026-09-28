package httpapi_test

import (
	"context"
	"net/url"
	"reflect"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
)

// history is a fixed Match history of subscription_s1 for transport tests.
type history struct{}

func testMatch(i int64) monitoring.Match {
	id := "match_" + string(rune('0'+i))
	return monitoring.Match{ID: id, SubscriptionID: "subscription_s1", SubscriptionVersionID: "subscription_version_s1", SavedQueryID: "saved_query_q1", SavedQueryVersionID: "saved_query_version_q1",
		RecordID: "record_" + id, RecordVersionID: "version_" + id, Position: i * 10,
		Evidence: monitoring.MatchEvidence{Evaluator: monitoring.Evaluator{PluginID: "quivr.fixture", Version: "1", Configuration: map[string]any{}}, Explanation: "fixture", PartKeys: []string{"body"}, Details: map[string]any{"marker": "default"}}}
}

func (history) Match(_ context.Context, org, id string) (monitoring.Match, error) {
	for i := int64(1); i <= 3; i++ {
		if m := testMatch(i); m.ID == id && org == "org_a" {
			return m, nil
		}
	}
	return monitoring.Match{}, monitoring.ErrNotFound
}
func (history) Matches(_ context.Context, org, sub string, after int64, limit int) ([]monitoring.Match, error) {
	out := []monitoring.Match{}
	for i := int64(1); i <= 3 && len(out) < limit; i++ {
		if m := testMatch(i); m.Position > after && m.SubscriptionID == sub {
			out = append(out, m)
		}
	}
	return out, nil
}
func (history) Delivery(_ context.Context, org, id string) (monitoring.Delivery, error) {
	if id != "delivery_1" || org != "org_a" {
		return monitoring.Delivery{}, monitoring.ErrNotFound
	}
	return monitoring.Delivery{ID: id, MatchID: "match_1", SubscriptionID: "subscription_s1", DestinationID: "receiver_a", State: "pending", Admission: monitoring.Admission{Reason: "subscription_disabled"},
		Event: []byte(`{"event_id":"event_1","type":"match.created","schema_version":"1","occurred_at":"2026-09-28T12:00:00.123456Z","references":{"match_id":"match_1","record_id":"record_match_1","record_version_id":"version_match_1","subscription_id":"subscription_s1","subscription_version_id":"subscription_version_s1","delivery_id":"delivery_1"}}`)}, nil
}

func TestMatchHistoryPagesAndDeliveryRead(t *testing.T) {
	server := monitoringServer(t)
	call(t, server, "POST", "/v0/saved-queries", monitor, savedQueryBody, 201)
	call(t, server, "POST", "/v0/subscriptions", monitor, subscriptionBody("s1", "quivr.fixture", "receiver_a"), 201)
	list := func(query url.Values, token string, want int) map[string]any {
		body, _ := call(t, server, "GET", "/v0/matches?"+query.Encode(), token, "", want)
		return body
	}
	first := list(url.Values{"subscription_id": {"subscription_s1"}, "limit": {"2"}}, monitorReader, 200)
	next, _ := first["next_page_cursor"].(string)
	if len(first["items"].([]any)) != 2 || next == "" {
		t.Fatalf("first page: %v", first)
	}
	second := list(url.Values{"subscription_id": {"subscription_s1"}, "limit": {"2"}, "page_cursor": {next}}, monitorReader, 200)
	items := second["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["match_id"] != "match_3" || second["next_page_cursor"] != nil {
		t.Fatalf("second page: %v", second)
	}
	// A cursor is bound to its Subscription and scope.
	if body := list(url.Values{"subscription_id": {"subscription_s1"}, "page_cursor": {next}}, monitor, 409); body["code"] != "cursor_scope_changed" {
		t.Fatal(body)
	}
	for name, q := range map[string]url.Values{
		"missing subscription": {},
		"unknown parameter":    {"subscription_id": {"subscription_s1"}, "corpus_id": {"c"}},
		"limit above maximum":  {"subscription_id": {"subscription_s1"}, "limit": {"101"}},
		"tampered cursor":      {"subscription_id": {"subscription_s1"}, "page_cursor": {next + "x"}},
	} {
		if body := list(q, monitorReader, 422); body["code"] == nil {
			t.Fatal(name, body)
		}
	}
	list(url.Values{"subscription_id": {"subscription_s1"}}, monitorOther, 404)
	list(url.Values{"subscription_id": {"subscription_s1"}}, monitorNarrow, 200)
	list(url.Values{"subscription_id": {"subscription_s1"}}, noMonitoring, 403)

	match, _ := call(t, server, "GET", "/v0/matches/match_1", monitorReader, "", 200)
	evidence := match["evidence"].(map[string]any)
	if match["record_version_id"] != "version_match_1" || evidence["explanation"] != "fixture" || !reflect.DeepEqual(evidence["part_keys"], []any{"body"}) {
		t.Fatalf("match: %v", match)
	}
	call(t, server, "GET", "/v0/matches/match_1", monitorOther, "", 404)
	call(t, server, "GET", "/v0/matches/missing", monitorReader, "", 404)
	call(t, server, "POST", "/v0/matches/match_1", monitor, "{}", 405)

	delivery, _ := call(t, server, "GET", "/v0/deliveries/delivery_1", monitorReader, "", 200)
	event := delivery["event"].(map[string]any)
	if delivery["state"] != "pending" || delivery["attempt_count"] != float64(0) || event["event_id"] != "event_1" || event["occurred_at"] != "2026-09-28T12:00:00.123456Z" ||
		!reflect.DeepEqual(delivery["admission"], map[string]any{"allowed": false, "reason": "subscription_disabled"}) {
		t.Fatalf("delivery: %v", delivery)
	}
	call(t, server, "GET", "/v0/deliveries/delivery_1", monitorOther, "", 404)
	call(t, server, "GET", "/v0/deliveries/delivery_1/attempts", monitorReader, "", 404)
}
