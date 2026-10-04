package client_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/client"
)

func TestConnectorClientPreservesPluginDefinedBodies(t *testing.T) {
	for _, operation := range []struct {
		name   string
		status int
		parse  func(*http.Response) ([]byte, error)
	}{
		{"push", 403, func(r *http.Response) ([]byte, error) {
			parsed, err := client.ParsePushConnectorAPIResponse(r)
			if err != nil {
				return nil, err
			}
			return parsed.Body, nil
		}},
		{"challenge", 200, func(r *http.Response) ([]byte, error) {
			parsed, err := client.ParseChallengeConnectorAPIResponse(r)
			if err != nil {
				return nil, err
			}
			return parsed.Body, nil
		}},
	} {
		for _, body := range []string{`["example"]`, `"example"`, "provider diagnostic"} {
			response := &http.Response{StatusCode: operation.status,
				Header: http.Header{"Content-Type": {"application/json"}, "Quivr-Response-Origin": {"plugin"}},
				Body:   io.NopCloser(strings.NewReader(body))}
			got, err := operation.parse(response)
			if err != nil || string(got) != body {
				t.Errorf("%s parser discarded plugin body %q: got %q, err %v", operation.name, body, got, err)
			}
		}
	}
}
