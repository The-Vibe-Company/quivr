package kubernetes_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/autoscaling/kubernetes"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const scaleURL = "https://203.0.113.1:443/apis/apps/v1/namespaces/quivr/deployments/quivr-bulk/scale"

// This owner test exercises the scale subresource wire contract: bearer token
// read per request, merge patch on spec.replicas, and sanitized failures.
func TestKubernetesScaleAPI(t *testing.T) {
	token := filepath.Join(t.TempDir(), "token")
	for _, tc := range []struct {
		name, method, response string
		status, set, want      int
		bad                    bool
	}{
		{name: "read", method: "GET", status: 200, response: `{"kind":"Scale","spec":{"replicas":3},"status":{"replicas":2}}`, want: 3},
		{name: "read zero omitted", method: "GET", status: 200, response: `{"kind":"Scale","spec":{},"status":{}}`, want: 0},
		{name: "read not a scale", method: "GET", status: 200, response: `{"kind":"Status"}`, bad: true},
		{name: "read negative", method: "GET", status: 200, response: `{"kind":"Scale","spec":{"replicas":-1}}`, bad: true},
		{name: "read forbidden", method: "GET", status: 403, response: `sensitive RBAC message`, bad: true},
		{name: "scale", method: "PATCH", set: 5, status: 200, response: `{"kind":"Scale","spec":{"replicas":5}}`},
		{name: "scale not applied", method: "PATCH", set: 5, status: 200, response: `{"kind":"Scale","spec":{"replicas":4}}`, bad: true},
		{name: "scale conflict", method: "PATCH", set: 5, status: 409, response: `sensitive conflict`, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A rotated token must be used by the next request.
			if err := os.WriteFile(token, []byte(" token-"+tc.name+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(r.Body)
				if r.Method != tc.method || r.URL.String() != scaleURL || r.Header.Get("Authorization") != "Bearer token-"+tc.name || r.Header.Get("User-Agent") != "quivr-autoscaler" {
					t.Fatalf("unexpected request %s %s", r.Method, r.URL)
				}
				if tc.method == "PATCH" && (r.Header.Get("Content-Type") != "application/merge-patch+json" || string(body) != `{"spec":{"replicas":5}}`) {
					t.Fatalf("unexpected patch %q %s", r.Header.Get("Content-Type"), body)
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.response))}, nil
			})}
			backend := kubernetes.Backend{URL: scaleURL, TokenFile: token, Client: client}
			var got int
			var err error
			if tc.method == "GET" {
				got, err = backend.Replicas(context.Background())
			} else {
				err = backend.SetReplicas(context.Background(), tc.set)
			}
			if (err != nil) != tc.bad || got != tc.want {
				t.Fatalf("got %d/%v, want %d/error %v", got, err, tc.want, tc.bad)
			}
			if err != nil && strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("leaked remote error: %v", err)
			}
		})
	}
	t.Run("missing token", func(t *testing.T) {
		backend := kubernetes.Backend{URL: scaleURL, TokenFile: filepath.Join(t.TempDir(), "absent"), Client: &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("request sent without a token")
		})}}
		if _, err := backend.Replicas(context.Background()); err == nil || !strings.Contains(err.Error(), "token") {
			t.Fatalf("want token error, got %v", err)
		}
	})
}
