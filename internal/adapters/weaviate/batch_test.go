package weaviate_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/adapters/weaviate"
	"github.com/The-Vibe-Company/quivr/internal/content"
)

// batchProjectionHTTP supplies only the remote object API. Publication,
// verification, retry selection and batch boundaries belong to the real adapter.
type batchProjectionHTTP struct {
	objects map[string]map[string]any
	batches []int
	fault   string
}

func (h *batchProjectionHTTP) RoundTrip(r *http.Request) (*http.Response, error) {
	reply := func(status int, value any) (*http.Response, error) {
		raw, _ := json.Marshal(value)
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/v1/schema/BatchObjects":
		return reply(200, map[string]any{"properties": []any{}})
	case r.Method == "POST" && r.URL.Path == "/v1/schema/BatchObjects/properties":
		return reply(200, nil)
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/objects/"):
		parts := strings.Split(r.URL.Path, "/")
		object, ok := h.objects[parts[len(parts)-1]]
		if !ok {
			return reply(404, nil)
		}
		return reply(200, object)
	case r.Method == "POST" && (r.URL.Path == "/v1/batch/objects" || r.URL.Path == "/v1/objects"):
		var objects []map[string]any
		if r.URL.Path == "/v1/objects" {
			var object map[string]any
			if err := json.NewDecoder(r.Body).Decode(&object); err != nil {
				return nil, err
			}
			objects = []map[string]any{object}
		} else {
			var body struct {
				Objects []map[string]any `json:"objects"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				return nil, err
			}
			objects = body.Objects
		}
		h.batches = append(h.batches, len(objects))
		var results []any
		for i, object := range objects {
			result := map[string]any{"status": "SUCCESS"}
			if h.fault == "object error" && i == 1 {
				result = map[string]any{"status": "FAILED", "errors": map[string]any{"error": []any{map[string]any{"message": "injected object failure"}}}}
			} else {
				h.objects[object["id"].(string)] = object
			}
			results = append(results, map[string]any{"id": object["id"], "result": result})
		}
		fault := h.fault
		h.fault = ""
		if fault == "lost response" {
			return nil, errors.New("injected lost response after write")
		}
		if fault == "incomplete response" {
			results = results[:len(results)-1]
		}
		return reply(200, results)
	case r.Method == "POST" && r.URL.Path == "/v1/graphql":
		return reply(200, map[string]any{"data": map[string]any{"Get": map[string]any{"BatchObjects": []any{}}}})
	}
	return nil, fmt.Errorf("unexpected projection request %s %s", r.Method, r.URL.Path)
}

// Both publication paths must batch, fail on an individual object/result, and
// reconcile interrupted writes without rewriting already verified identities.
func TestProjectionBatchesCheckObjectFailuresAndRetry(t *testing.T) {
	for _, mode := range []struct{ embedding, item bool }{{}, {embedding: true}, {item: true}, {embedding: true, item: true}} {
		embedding := mode.embedding
		for _, fault := range []string{"", "object error", "lost response", "incomplete response"} {
			t.Run(fmt.Sprintf("embeddings=%v/items=%v/%s", embedding, mode.item, fault), func(t *testing.T) {
				h := &batchProjectionHTTP{objects: map[string]map[string]any{}}
				s := weaviate.New("http://projection.invalid")
				s.Client = &http.Client{Transport: h}
				g := content.Generation{ID: "generation", Collection: "BatchObjects", SpaceID: "space", ItemKeywordsProjected: mode.item}
				seg := content.Segmentation{ID: "segmentation", VersionID: "version"}
				var data []content.EmbeddingData
				for i := range 101 {
					id := fmt.Sprintf("segment-%03d", i)
					seg.Segments = append(seg.Segments, content.Segment{ID: id, PartKey: "body", Text: "example text"})
					vector := []float32{1}
					raw, err := content.VectorBytes(vector)
					if err != nil {
						t.Fatal(err)
					}
					data = append(data, content.EmbeddingData{Artifact: content.Embedding{Organization: "organization", SpaceID: "space", SegmentID: id, Payload: content.Blob{SHA256: content.Hash(raw)}}, Vector: vector})
				}
				publish := func() error {
					return s.Publish(context.Background(), g, "organization", "corpus", "example-feed", content.Version{ID: "version"}, seg)
				}
				if embedding {
					if err := publish(); err != nil {
						t.Fatal(err)
					}
					h.batches = nil
					publish = func() error { return s.PublishEmbeddings(context.Background(), g, "organization", data) }
				}
				h.fault = fault
				err := publish()
				wantError := fault == "object error" || fault == "incomplete response"
				if (err != nil) != wantError {
					t.Fatalf("publication error=%v; wantError=%v", err, wantError)
				}
				if len(h.batches) == 0 || h.batches[0] != 100 {
					t.Fatalf("batch sizes=%v; want first bounded multi-object batch of 100", h.batches)
				}
				if err := publish(); err != nil {
					t.Fatalf("retry: %v", err)
				}
				for _, n := range h.batches {
					if n < 1 || n > 100 {
						t.Fatalf("batch sizes=%v exceed bounds", h.batches)
					}
				}
				wantObjects := 101
				if embedding {
					wantObjects = 202
				}
				if mode.item {
					wantObjects++
				}
				if len(h.objects) != wantObjects {
					t.Fatalf("stored objects=%d want %d", len(h.objects), wantObjects)
				}
				writes := len(h.batches)
				if err := publish(); err != nil {
					t.Fatal(err)
				}
				if len(h.batches) != writes {
					t.Fatalf("verified retry rewrote objects: %v", h.batches)
				}
			})
		}
	}
}
