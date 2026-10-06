package quivrplugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Manifest admission owns the SDK's promise to serve every Contribution.
func TestNormalizerAndSubscriptionManifestAdmission(t *testing.T) {
	for _, name := range []string{"minimal.yaml", "subscription.yaml"} {
		t.Run(name, func(t *testing.T) {
			p, err := New(filepath.Join(fixtures, "manifests/valid", name))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Handler(); err == nil {
				t.Fatal("unregistered Contribution served")
			}
		})
	}
	p, err := New(filepath.Join(fixtures, "manifests/valid/subscription.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	t.Run("vectors before API", func(t *testing.T) {
		if _, err := New(filepath.Join(fixtures, "manifests/invalid/subscription-vectors.yaml")); err == nil || !strings.Contains(err.Error(), "subscription vectors require Plugin API") {
			t.Fatalf("vector declaration before supported API: %v", err)
		}
	})
	doc := generic(t, p.m.doc)
	sub := doc["contributions"].(map[string]any)["subscription"].(map[string]any)
	delete(sub, "configuration_schema")
	path := filepath.Join(t.TempDir(), "manifest.json")
	for _, tc := range []struct {
		name, api string
		vectors   bool
		valid     bool
	}{
		{"optional configuration", ">=0.2.0 <0.3.0", false, true},
		{"vectors supported", ">=0.10.0 <0.11.0", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc["compatibility"].(map[string]any)["plugin_api"] = tc.api
			if tc.vectors {
				sub["vectors"] = map[string]any{"parts": true, "query": true, "query_text_pointer": "/text"}
			} else {
				delete(sub, "vectors")
			}
			body, _ := json.Marshal(doc)
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := New(path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t, err=%v", tc.valid, err)
			}
			if tc.valid && tc.vectors {
				encoded, _ := json.Marshal(got.m.Subscription)
				if !strings.Contains(string(encoded), `"query_text_pointer":"/text"`) {
					t.Fatalf("vector declaration lost: %s", encoded)
				}
			}
		})
	}
}

type subscriptionFunc func(*SubscriptionRequest) (*SubscriptionResponse, error)

func (f subscriptionFunc) Evaluate(_ context.Context, r *SubscriptionRequest) (*SubscriptionResponse, error) {
	return f(r)
}

type normalizerFunc func(context.Context, *NormalizerRequest) (*NormalizerResponse, error)

func (f normalizerFunc) Normalize(ctx context.Context, r *NormalizerRequest) (*NormalizerResponse, error) {
	return f(ctx, r)
}

// The normative oracle owns decision coverage, evidence bounds and Part references.
func TestSubscriptionNormativeResponses(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixtures, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index struct {
		Cases []struct {
			File, Schema, Request string
			Valid                 bool
		}
	}
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	p, err := New(filepath.Join(fixtures, "manifests/valid/subscription.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range index.Cases {
		if c.Schema != "subscription-response.schema.json" {
			continue
		}
		t.Run(c.File, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join(fixtures, c.Request))
			if err != nil {
				t.Fatal(err)
			}
			response, err := os.ReadFile(filepath.Join(fixtures, c.File))
			if err != nil {
				t.Fatal(err)
			}
			var out SubscriptionResponse
			if err := json.Unmarshal(response, &out); err != nil {
				t.Fatal(err)
			}
			if err := p.Subscription(subscriptionFunc(func(*SubscriptionRequest) (*SubscriptionResponse, error) { return &out, nil })); err != nil {
				t.Fatal(err)
			}
			h, err := p.Handler()
			if err != nil {
				t.Fatal(err)
			}
			status, _, got := call(h, "/v0/contributions/subscription", body)
			if (status == 200) != c.Valid {
				t.Fatalf("valid=%t, status=%d, body=%s", c.Valid, status, got)
			}
		})
	}
	for _, details := range []map[string]any{{"nested": []any{map[string]any{"value": "bad\x00"}}}, {"bad\x00": "value"}} {
		t.Run("nested evidence NUL", func(t *testing.T) {
			body, _ := os.ReadFile(filepath.Join(fixtures, "requests/subscription/valid.json"))
			var req SubscriptionRequest
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatal(err)
			}
			out := &SubscriptionResponse{}
			for _, e := range req.Evaluations {
				d := Match(e.ID, "matched")
				d.Evidence.Details = details
				out.Decisions = append(out.Decisions, d)
			}
			_ = p.Subscription(subscriptionFunc(func(*SubscriptionRequest) (*SubscriptionResponse, error) { return out, nil }))
			h, err := p.Handler()
			if err != nil {
				t.Fatal(err)
			}
			status, doc, got := call(h, "/v0/contributions/subscription", body)
			if status != 500 || doc["code"] != "invalid_response" {
				t.Fatalf("got %d: %s", status, got)
			}
		})
	}
}

// Input corruption is refused through the same route a caller uses.
func TestNormalizerReadsVerifiedInputAndChecksRequests(t *testing.T) {
	input := []byte("Document body")
	path := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(path, input, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(input)
	raw, err := os.ReadFile(filepath.Join(fixtures, "requests/file-reference.json"))
	if err != nil {
		t.Fatal(err)
	}
	var req NormalizerRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	req.Input.MediaType = "application/x-example"
	req.Input.Reference.URL = "file://" + path
	req.Input.SizeBytes = int64(len(input))
	req.Input.SHA256 = hex.EncodeToString(sum[:])
	p, err := New(filepath.Join(fixtures, "manifests/valid/minimal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Normalizer(normalizerFunc(func(ctx context.Context, r *NormalizerRequest) (*NormalizerResponse, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing manifest deadline")
		}
		b, err := r.ReadInput(ctx)
		if err != nil {
			return nil, err
		}
		return &NormalizerResponse{Manifest: NormalizedManifest{Kind: "manifest", Parts: []NormalizedPart{{Key: "body", Role: "body", Content: NormalizedContent{Kind: "text", Text: string(b)}}}}}, nil
	})); err != nil {
		t.Fatal(err)
	}
	h, err := p.Handler()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		edit   func(*NormalizerRequest)
		status int
		code   string
	}{
		{"valid", func(*NormalizerRequest) {}, 200, ""},
		{"size", func(r *NormalizerRequest) { r.Input.SizeBytes++ }, 422, "input_integrity"},
		{"checksum", func(r *NormalizerRequest) { r.Input.SHA256 = strings.Repeat("0", 64) }, 422, "input_integrity"},
		{"media", func(r *NormalizerRequest) { r.Input.MediaType = "image/png" }, 400, "unsupported_media_type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := req
			tc.edit(&r)
			body, _ := json.Marshal(r)
			status, doc, got := call(h, "/v0/contributions/normalizer", body)
			if status != tc.status || (tc.code != "" && doc["code"] != tc.code) {
				t.Fatalf("want %d/%s got %d: %s", tc.status, tc.code, status, got)
			}
			if status == 200 && !strings.Contains(got, string(input)) {
				t.Fatalf("input missing: %s", got)
			}
		})
	}
}
