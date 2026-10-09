package acceptance

import (
	"testing"
	"time"
)

// Admin commands are accepted first. Success returns their cutover result;
// discovery and coverage refusals are terminal Operation diagnostics.
func routingRequest(t *testing.T, method, path, token string, body any, want int) map[string]any {
	t.Helper()
	if want != 200 && want != 409 {
		return request(t, method, path, token, body, want)
	}
	op := request(t, method, path, token, body, 202)
	deadline := time.Now().Add(10 * time.Second)
	for op["state"] == "queued" || op["state"] == "running" {
		if time.Now().After(deadline) {
			t.Fatalf("routing Operation never terminal: %v", op)
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
