package quivrplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type ingestionAnswer struct {
	segments []Segment
	vector   []float32
}

func (a ingestionAnswer) SegmentAndEmbed(context.Context, *IngestRequest) ([]Segment, error) {
	return a.segments, nil
}
func (a ingestionAnswer) EmbedQuery(context.Context, *QueryRequest) ([]float32, error) {
	return a.vector, nil
}

func callIngestion(t *testing.T, p *Plugin, h http.Handler, route string, body []byte) (int, map[string]any, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, route, bytes.NewReader(body))
	if compareVersions(p.m.pluginAPI, "0.14.0") >= 0 {
		t.Setenv(EnvSigningKeys, signingRing(t, "current", signingKey("current", currentSigningSecret, nil, nil)))
		now := time.Now().Unix()
		req.Header.Set("Authorization", engineToken(t, currentSigningSecret, "current", p.m.ID, p.m.ID, http.MethodPost, route, body, now-1, now+59))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

// The normative response oracle owns SDK ingestion semantics at the HTTP boundary.
func TestIngestionNormativeResponses(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixtures, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index struct {
		Cases []struct {
			File, Schema, Manifest, Request string
			Valid                           bool
			SchemaValid                     bool `json:"schema_valid"`
		}
	}
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	for _, c := range index.Cases {
		if !c.SchemaValid || (c.Schema != "ingestion-segment-and-embed-response.schema.json" && c.Schema != "ingestion-embed-query-response.schema.json") {
			continue // The typed SDK cannot construct unknown fields or wrong JSON types.
		}
		t.Run(c.File+"/"+c.Request, func(t *testing.T) {
			p, err := New(filepath.Join(fixtures, c.Manifest))
			if err != nil {
				t.Fatal(err)
			}
			response, err := os.ReadFile(filepath.Join(fixtures, c.File))
			if err != nil {
				t.Fatal(err)
			}
			var out struct {
				Segments []Segment `json:"segments"`
				Vector   []float32 `json:"vector"`
			}
			if err := json.Unmarshal(response, &out); err != nil {
				if c.File != "responses/ingestion/float32-overflow.json" {
					t.Fatal(err)
				}
				return // float32 overflow is unrepresentable in the SDK's vector type.
			}
			_ = p.Ingestion(ingestionAnswer{segments: out.Segments, vector: out.Vector})
			h, err := p.Handler()
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(fixtures, c.Request))
			if err != nil {
				t.Fatal(err)
			}
			route := "/v0/contributions/ingestion/segment_and_embed"
			if c.Schema == "ingestion-embed-query-response.schema.json" {
				route = "/v0/contributions/ingestion/embed_query"
			}
			status, doc, got := callIngestion(t, p, h, route, body)
			if (status == 200) != c.Valid || (!c.Valid && doc["code"] != "invalid_response") {
				t.Fatalf("valid=%t, status=%d: %s", c.Valid, status, got)
			}
		})
	}
	integers := make([]int64, 205)
	for i := range integers {
		integers[i] = 999999999999999999
	}
	for _, tc := range []struct {
		name    string
		segment Segment
	}{
		{"empty without title", Segment{PartKey: "body", Vectors: map[string][]float32{"example.words.small": {1, 0, 0, 0}}}},
		{"invalid UTF-8 lexical text", Segment{PartKey: "body", End: 1, Vectors: map[string][]float32{"example.words.small": {1, 0, 0, 0}}, LexicalText: string([]byte{0xff})}},
		{"nested provenance NUL", Segment{PartKey: "body", End: 1, Vectors: map[string][]float32{"example.words.small": {1, 0, 0, 0}}, Provenance: map[string]any{"nested": []any{map[string]string{"bad\x00": "value"}}}}},
		{"rounded integer provenance", Segment{PartKey: "body", End: 1, Vectors: map[string][]float32{"example.words.small": {1, 0, 0, 0}}, Provenance: map[string]any{"values": integers}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := New(filepath.Join(fixtures, "manifests/valid/ingestion.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			_ = p.Ingestion(ingestionAnswer{segments: []Segment{tc.segment}})
			h, err := p.Handler()
			if err != nil {
				t.Fatal(err)
			}
			body, _ := os.ReadFile(filepath.Join(fixtures, "requests/ingestion/segment-and-embed-body-only.json"))
			status, doc, got := call(h, "/v0/contributions/ingestion/segment_and_embed", body)
			if status != 500 || doc["code"] != "invalid_response" {
				t.Fatalf("got %d: %s", status, got)
			}
		})
	}
	// These values cannot appear in JSON fixtures, but a Go implementation can return them.
	for _, vector := range [][]float32{{0, 0, 0, 0}, {float32(math.NaN()), 0, 0, 0}, {float32(math.Inf(1)), 0, 0, 0}} {
		t.Run("invalid query values", func(t *testing.T) {
			p, err := New(filepath.Join(fixtures, "manifests/valid/ingestion.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			_ = p.Ingestion(ingestionAnswer{vector: vector})
			h, err := p.Handler()
			if err != nil {
				t.Fatal(err)
			}
			body, _ := os.ReadFile(filepath.Join(fixtures, "requests/ingestion/embed-query.json"))
			status, doc, got := call(h, "/v0/contributions/ingestion/embed_query", body)
			if status != 500 || doc["code"] != "invalid_response" {
				t.Fatalf("got %d: %s", status, got)
			}
		})
	}
}
