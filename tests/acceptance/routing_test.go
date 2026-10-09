package acceptance

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// The fake transport models a fast API while its Operation runs for six
// virtual seconds. Each real request still writes its contract capture.
type routingOperationTransport struct{ started time.Time }

func (transport routingOperationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	status, body := http.StatusAccepted, `{"operation_id":"op_test","state":"queued"}`
	if req.Method == http.MethodGet {
		time.Sleep(time.Millisecond) // dependency latency advances only the synctest clock
		status, body = http.StatusOK, `{"operation_id":"op_test","state":"running"}`
		if time.Since(transport.started) >= 6*time.Second {
			body = `{"operation_id":"op_test","state":"succeeded","admin":{"vector_space_id":"example.space@1"}}`
		}
	}
	return &http.Response{StatusCode: status, Header: http.Header{"X-Request-Id": {"test-request"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestRoutingWaiterBoundsContractCaptures(t *testing.T) {
	captures := t.TempDir()
	t.Setenv("QUIVR_TEST_URL", "http://example.org")
	t.Setenv("QUIVR_TEST_CAPTURES", captures)
	synctest.Test(t, func(t *testing.T) {
		previous := http.DefaultTransport
		http.DefaultTransport = routingOperationTransport{started: time.Now()}
		defer func() { http.DefaultTransport = previous }()
		result := routingRequest(t, "POST", "/v0/admin/spaces/example.space@1/promote", "operator", map[string]any{}, 200)
		if result["vector_space_id"] != "example.space@1" {
			t.Fatalf("routing result: %v", result)
		}
	})
	files, err := os.ReadDir(captures)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("six-second Operation wrote %d contract captures", len(files))
	if len(files) > 400 {
		t.Fatalf("six-second Operation wrote %d captures, want at most 400; unpaced polling can overflow the artifact uploader", len(files))
	}
}

// Admin commands are accepted first. Success returns their cutover result;
// discovery and coverage refusals are terminal Operation diagnostics.
func routingRequest(t *testing.T, method, path, token string, body any, want int) map[string]any {
	t.Helper()
	if want != 200 && want != 409 {
		return request(t, method, path, token, body, want)
	}
	op := request(t, method, path, token, body, 202)
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for op["state"] == "queued" || op["state"] == "running" {
		select {
		case <-deadline.C:
			t.Fatalf("routing Operation never terminal: %v", op)
		case <-poll.C:
		}
		op = request(t, "GET", "/v0/operations/"+op["operation_id"].(string), token, nil, 200)
	}
	if want == 409 {
		if op["state"] != "failed" || len(op["errors"].([]any)) == 0 {
			t.Fatalf("expected refused routing Operation: %v", op)
		}
		return op["errors"].([]any)[0].(map[string]any)
	}
	if op["state"] != "succeeded" {
		t.Fatalf("routing Operation failed: %v", op)
	}
	result := op["admin"].(map[string]any)
	if plan, ok := result["plan_id"].(string); ok {
		return request(t, "GET", "/v0/admin/plugins/plans/"+plan, token, nil, 200)
	}
	return result
}
