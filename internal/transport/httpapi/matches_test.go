package httpapi_test

import (
	"context"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
)

// history is a fixed Match history of subscription_s1 for transport tests.
// Organization and Corpus visibility are the service's
// (internal/monitoring engine_test) and the store's.
type history struct{}

func testMatch(i int64) monitoring.Match {
	id := "match_" + string(rune('0'+i))
	return monitoring.Match{ID: id, SubscriptionID: "subscription_s1", SubscriptionVersionID: "subscription_version_s1", SavedQueryID: "saved_query_q1", SavedQueryVersionID: "saved_query_version_q1",
		RecordID: "record_" + id, RecordVersionID: "version_" + id, Position: i * 10, Owner: map[int64]string{3: "user-123"}[i],
		Evidence: monitoring.MatchEvidence{Evaluator: monitoring.Evaluator{PluginID: "quivr.fixture", Version: "1", Configuration: map[string]any{}}, Explanation: "fixture", PartKeys: []string{"body"}, Details: map[string]any{"marker": "default"}}}
}

func (history) Match(_ context.Context, org, id string) (monitoring.Match, error) {
	for i := int64(1); i <= 3; i++ {
		if m := testMatch(i); m.ID == id {
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
	if id == "delivery_corrupt" {
		delivery, _ := history{}.Delivery(context.Background(), org, "delivery_1")
		delivery.ID, delivery.Event = id, []byte(`{"event_id":`)
		return delivery, nil
	}
	if id == "delivery_2" {
		d, _ := history{}.Delivery(context.Background(), org, "delivery_1")
		d.ID, d.AttemptCount, d.Admission = id, 3, monitoring.Admission{Allowed: true}
		d.LastOutcome, d.LastErrorCode, d.LastErrorMessage = monitoring.AttemptPermanentError, "webhook_http_status", "receiver returned HTTP 400"
		return d, nil
	}
	if id == "delivery_3" || id == "delivery_4" {
		d, _ := history{}.Delivery(context.Background(), org, "delivery_1")
		d.ID, d.AttemptCount, d.Admission = id, 2, monitoring.Admission{Allowed: true}
		d.LastOutcome, d.LastErrorCode, d.LastErrorMessage = monitoring.AttemptRetryableError, "webhook_http_status", "receiver returned HTTP 503"
		next := time.Date(2026, 9, 28, 14, 0, 15, 0, time.FixedZone("CEST", 2*3600))
		d.NextAttemptAt = &next
		if id == "delivery_4" {
			d.State, d.Admission, d.NextAttemptAt = "exhausted", monitoring.Admission{Reason: "terminal"}, nil
		}
		return d, nil
	}
	if id != "delivery_1" {
		return monitoring.Delivery{}, monitoring.ErrNotFound
	}
	return monitoring.Delivery{ID: id, MatchID: "match_1", SubscriptionID: "subscription_s1", DestinationID: "receiver_a", State: "pending", Admission: monitoring.Admission{Reason: "subscription_disabled"},
		Event: []byte(`{"event_id":"event_1","type":"match.created","schema_version":"1","occurred_at":"2026-09-28T12:00:00.123456Z","references":{"match_id":"match_1","record_id":"record_match_1","record_version_id":"version_match_1","subscription_id":"subscription_s1","subscription_version_id":"subscription_version_s1","delivery_id":"delivery_1"}}`)}, nil
}

func (history) Attempts(_ context.Context, org, id string, after, limit int) ([]monitoring.Attempt, error) {
	all := []monitoring.Attempt{
		{ID: "attempt_1", DeliveryID: id, Number: 1, Outcome: monitoring.AttemptUnknown, ErrorCode: "webhook_outcome_unknown", ErrorMessage: "attempt outcome unknown after worker interruption"},
		{ID: "attempt_2", DeliveryID: id, Number: 2, Outcome: monitoring.AttemptRetryableError, HTTPStatus: 503, ErrorCode: "webhook_http_status", ErrorMessage: "receiver returned HTTP 503"},
		{ID: "attempt_3", DeliveryID: id, Number: 3, Outcome: monitoring.AttemptPermanentError, HTTPStatus: 400, ErrorCode: "webhook_http_status", ErrorMessage: "receiver returned HTTP 400"},
		{ID: "attempt_4", DeliveryID: id, Number: 4, Outcome: monitoring.AttemptInFlight},
		{ID: "attempt_5", DeliveryID: id, Number: 5, Outcome: monitoring.AttemptAcknowledged, HTTPStatus: 204},
	}
	out := []monitoring.Attempt{}
	for _, a := range all {
		if id == "delivery_1" && a.Number > after && len(out) < limit {
			out = append(out, a)
		}
	}
	return out, nil
}

func TestDeliveryAttemptsPageBoundedHistory(t *testing.T) {
	server := monitoringServer(t)
	call(t, server, "POST", "/v0/saved-queries", monitor, savedQueryBody, 201)
	call(t, server, "POST", "/v0/subscriptions", monitor, subscriptionBody("s1", "quivr.fixture", "receiver_a"), 201)
	first, _ := call(t, server, "GET", "/v0/deliveries/delivery_1/attempts?limit=3", monitorReader, "", 200)
	next, _ := first["next_page_cursor"].(string)
	items := first["items"].([]any)
	if len(items) != 3 || next == "" {
		t.Fatalf("first page: %v", first)
	}
	want := []map[string]any{
		{"attempt_id": "attempt_1", "delivery_id": "delivery_1", "number": float64(1), "outcome": "unknown", "error": map[string]any{"code": "webhook_outcome_unknown", "message": "attempt outcome unknown after worker interruption", "retryable": true}},
		{"attempt_id": "attempt_2", "delivery_id": "delivery_1", "number": float64(2), "outcome": "retryable_error", "http_status": float64(503), "error": map[string]any{"code": "webhook_http_status", "message": "receiver returned HTTP 503", "retryable": true}},
		{"attempt_id": "attempt_3", "delivery_id": "delivery_1", "number": float64(3), "outcome": "permanent_error", "http_status": float64(400), "error": map[string]any{"code": "webhook_http_status", "message": "receiver returned HTTP 400", "retryable": false}},
	}
	for i := range want {
		if !reflect.DeepEqual(items[i], want[i]) {
			t.Fatalf("attempt %d: %v", i, items[i])
		}
	}
	second, _ := call(t, server, "GET", "/v0/deliveries/delivery_1/attempts?limit=3&page_cursor="+url.QueryEscape(next), monitorReader, "", 200)
	items = second["items"].([]any)
	if len(items) != 2 || second["next_page_cursor"] != nil || !reflect.DeepEqual(items[0], map[string]any{"attempt_id": "attempt_4", "delivery_id": "delivery_1", "number": float64(4), "outcome": "in_flight"}) ||
		!reflect.DeepEqual(items[1], map[string]any{"attempt_id": "attempt_5", "delivery_id": "delivery_1", "number": float64(5), "outcome": "acknowledged", "http_status": float64(204)}) {
		t.Fatalf("second page: %v", second)
	}
	for _, r := range []struct {
		method, path, token string
		status              int
		code                string
	}{
		// Cursors bind the Delivery and the key scope.
		{"GET", "/v0/deliveries/delivery_2/attempts?page_cursor=" + url.QueryEscape(next), monitorReader, 409, "cursor_scope_changed"},
		{"GET", "/v0/deliveries/delivery_1/attempts?page_cursor=" + url.QueryEscape(next), monitorNarrow, 409, "cursor_scope_changed"},
		{"GET", "/v0/deliveries/delivery_1/attempts?limit=0", monitorReader, 422, "invalid_limit"},
		{"GET", "/v0/deliveries/delivery_1/attempts?limit=101", monitorReader, 422, "invalid_limit"},
		{"GET", "/v0/deliveries/delivery_1/attempts?page_cursor=" + url.QueryEscape(next+"x"), monitorReader, 422, "invalid_cursor"},
		{"GET", "/v0/deliveries/delivery_1/attempts?page_cursor=", monitorReader, 422, "invalid_cursor"},
		{"GET", "/v0/deliveries/delivery_1/attempts?other=1", monitorReader, 422, "invalid_query"},
		{"GET", "/v0/deliveries/missing/attempts", monitorReader, 404, "not_found"},
		// Reading needs monitoring:read, judged before the query is.
		{"GET", "/v0/deliveries/delivery_1/attempts?limit=101", noMonitoring, 403, "forbidden"},
		{"POST", "/v0/deliveries/delivery_1/attempts", monitor, 405, "method_not_allowed"},
		{"GET", "/v0/deliveries/delivery_1/other", monitorReader, 404, "not_found"},
		{"GET", "/v0/deliveries/delivery_corrupt", monitorReader, 503, "storage_unavailable"},
	} {
		body := ""
		if r.method == "POST" {
			body = "{}"
		}
		if got, _ := call(t, server, r.method, r.path, r.token, body, r.status); got["code"] != r.code {
			t.Errorf("%s %s: %v, want %s", r.method, r.path, got, r.code)
		}
	}
	// The Delivery exposes its latest failure, distinguishable as permanent.
	failed, _ := call(t, server, "GET", "/v0/deliveries/delivery_2", monitorReader, "", 200)
	if failed["state"] != "pending" || !reflect.DeepEqual(failed["last_error"], map[string]any{"code": "webhook_http_status", "message": "receiver returned HTTP 400", "retryable": false}) ||
		!reflect.DeepEqual(failed["admission"], map[string]any{"allowed": true}) {
		t.Fatalf("failed delivery: %v", failed)
	}
	// A retrying Delivery reports its next eligibility in UTC; an exhausted one
	// keeps its last error, which nothing retries automatically any more.
	retrying, _ := call(t, server, "GET", "/v0/deliveries/delivery_3", monitorReader, "", 200)
	if retrying["next_attempt_at"] != "2026-09-28T12:00:15Z" || retrying["last_error"].(map[string]any)["retryable"] != true {
		t.Fatalf("retrying delivery: %v", retrying)
	}
	exhausted, _ := call(t, server, "GET", "/v0/deliveries/delivery_4", monitorReader, "", 200)
	if _, ok := exhausted["next_attempt_at"]; ok || exhausted["state"] != "exhausted" ||
		!reflect.DeepEqual(exhausted["last_error"], map[string]any{"code": "webhook_http_status", "message": "receiver returned HTTP 503", "retryable": false}) {
		t.Fatalf("exhausted delivery: %v", exhausted)
	}
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
	for name, r := range map[string]struct {
		query, token string
		status       int
		code         string
	}{
		// A cursor is bound to its Subscription and to the key scope.
		"another Subscription": {url.Values{"subscription_id": {"subscription_s2"}, "page_cursor": {next}}.Encode(), monitorReader, 409, "cursor_scope_changed"},
		"another key scope":    {url.Values{"subscription_id": {"subscription_s1"}, "page_cursor": {next}}.Encode(), monitor, 409, "cursor_scope_changed"},
		"missing subscription": {"", monitorReader, 422, "invalid_query"},
		"unknown parameter":    {url.Values{"subscription_id": {"subscription_s1"}, "corpus_id": {"c"}}.Encode(), monitorReader, 422, "invalid_query"},
		"limit above maximum":  {url.Values{"subscription_id": {"subscription_s1"}, "limit": {"101"}}.Encode(), monitorReader, 422, "invalid_limit"},
		"empty limit":          {url.Values{"subscription_id": {"subscription_s1"}, "limit": {""}}.Encode(), monitorReader, 422, "invalid_limit"},
		"tampered cursor":      {url.Values{"subscription_id": {"subscription_s1"}, "page_cursor": {next + "x"}}.Encode(), monitorReader, 422, "invalid_cursor"},
		"unknown Subscription": {url.Values{"subscription_id": {"missing"}}.Encode(), monitorReader, 404, "not_found"},
	} {
		if body, _ := call(t, server, "GET", "/v0/matches?"+r.query, r.token, "", r.status); body["code"] != r.code {
			t.Errorf("%s: %v, want %s", name, body, r.code)
		}
	}

	match, _ := call(t, server, "GET", "/v0/matches/match_1", monitorReader, "", 200)
	evidence := match["evidence"].(map[string]any)
	if match["record_version_id"] != "version_match_1" || evidence["explanation"] != "fixture" || !reflect.DeepEqual(evidence["part_keys"], []any{"body"}) {
		t.Fatalf("match: %v", match)
	}
	// A Match carries its Subscription's owner; a global one has none.
	if match["owner"] != nil {
		t.Fatalf("global Match: %v", match)
	}
	if m, _ := call(t, server, "GET", "/v0/matches/match_3", monitorReader, "", 200); m["owner"] != "user-123" {
		t.Fatalf("owned Match: %v", m)
	}
	call(t, server, "GET", "/v0/matches/missing", monitorReader, "", 404)
	call(t, server, "POST", "/v0/matches/match_1", monitor, "{}", 405)

	delivery, _ := call(t, server, "GET", "/v0/deliveries/delivery_1", monitorReader, "", 200)
	event := delivery["event"].(map[string]any)
	if delivery["state"] != "pending" || delivery["attempt_count"] != float64(0) || event["event_id"] != "event_1" || event["occurred_at"] != "2026-09-28T12:00:00.123456Z" ||
		!reflect.DeepEqual(delivery["admission"], map[string]any{"allowed": false, "reason": "subscription_disabled"}) {
		t.Fatalf("delivery: %v", delivery)
	}
	call(t, server, "GET", "/v0/deliveries/missing", monitorReader, "", 404)
}
