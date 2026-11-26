package weaviate_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/weaviate"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

// batchProjectionHTTP supplies only the remote object API. Publication,
// verification, retry selection and batch boundaries belong to the real adapter.
type batchProjectionHTTP struct {
	objects   map[string]map[string]any
	batches   []int
	submitted []string
	reads     int
	lists     int
	removeID  string
	fault     string
	nullAt    int
	nullGet   bool
}

var batchInventory = regexp.MustCompile(`path:\["(id|segmentId)"\],operator:ContainsAny,valueText:(\[[^]]*\])`)

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
		h.reads++
		parts := strings.Split(r.URL.Path, "/")
		object, ok := h.objects[parts[len(parts)-1]]
		if !ok {
			return reply(404, nil)
		}
		return reply(200, object)
	case r.Method == "POST" && r.URL.Path == "/v1/batch/objects":
		var body struct {
			Objects []map[string]any `json:"objects"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		objects := body.Objects
		h.batches = append(h.batches, len(objects))
		for _, object := range objects {
			h.submitted = append(h.submitted, object["id"].(string))
		}
		if h.fault == "HTTP 413" || h.fault == "HTTP 500" {
			status := 413
			if h.fault == "HTTP 500" {
				status = 500
			}
			h.fault = ""
			return reply(status, nil)
		}
		if h.fault == "lost request" {
			h.fault = ""
			return nil, errors.New("injected failure before write")
		}
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
		if fault == "removed while batching" {
			delete(h.objects, h.removeID)
		}
		h.fault = ""
		if fault == "lost response with bad vector" {
			h.objects[objects[0]["id"].(string)]["vectors"] = map[string]any{"semantic_text_v1": []float32{2}}
		}
		if strings.HasPrefix(fault, "lost response") {
			return nil, errors.New("injected lost response after write")
		}
		if fault == "incomplete response" {
			results = results[:len(results)-1]
		}
		return reply(200, results)
	case r.Method == "DELETE" && r.URL.Path == "/v1/batch/objects":
		var body struct {
			Match struct {
				Where struct {
					Operands []struct {
						ID string `json:"valueText"`
					} `json:"operands"`
				} `json:"where"`
			} `json:"match"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		deleted := 0
		for _, operand := range body.Match.Where.Operands {
			if _, ok := h.objects[operand.ID]; ok {
				delete(h.objects, operand.ID)
				deleted++
			}
		}
		return reply(200, map[string]any{"results": map[string]any{
			"matches": deleted, "successful": deleted, "failed": 0, "limit": 10000,
		}})
	case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/v1/objects/"):
		id := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]
		if _, ok := h.objects[id]; !ok {
			return reply(404, nil)
		}
		delete(h.objects, id)
		return reply(204, nil)
	case r.Method == "POST" && r.URL.Path == "/v1/graphql":
		h.lists++
		if h.lists == h.nullAt {
			var get any
			if !h.nullGet {
				get = map[string]any{"BatchObjects": nil}
			}
			return reply(200, map[string]any{"data": map[string]any{"Get": get}})
		}
		var body struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		filter := batchInventory.FindStringSubmatch(body.Query)
		if filter == nil {
			return nil, fmt.Errorf("unexpected inventory query %s", body.Query)
		}
		var values []string
		if err := json.Unmarshal([]byte(filter[2]), &values); err != nil {
			return nil, err
		}
		selected := make(map[string]bool, len(values))
		for _, value := range values {
			selected[value] = true
		}
		rows := make([]any, 0)
		for id, object := range h.objects {
			props := object["properties"].(map[string]any)
			segment, _ := props["segmentId"].(string)
			if strings.Contains(body.Query, `path:["itemKind"],operator:Equal,valueText:"segment"`) && props["itemKind"] != "segment" {
				continue
			}
			if filter[1] == "id" && selected[id] || filter[1] == "segmentId" && selected[segment] {
				rows = append(rows, map[string]any{"segmentId": segment, "itemKind": props["itemKind"], "embeddingRevision": props["embeddingRevision"], "_additional": map[string]any{"id": id}})
			}
		}
		return reply(200, map[string]any{"data": map[string]any{"Get": map[string]any{"BatchObjects": rows}}})
	}
	return nil, fmt.Errorf("unexpected projection request %s %s", r.Method, r.URL.Path)
}

// Both publication paths must batch, fail on an individual object/result, and
// reconcile interrupted writes without rewriting already verified identities.
func TestProjectionBatchesCheckObjectFailuresAndRetry(t *testing.T) {
	for _, mode := range []struct{ embedding, item bool }{{}, {embedding: true}, {item: true}, {embedding: true, item: true}} {
		embedding := mode.embedding
		for _, fault := range []string{"", "object error", "lost response", "lost request", "lost response with bad vector", "incomplete response", "HTTP 413", "HTTP 500", "removed while batching"} {
			if (fault == "removed while batching" || fault == "lost response with bad vector") && !embedding {
				continue
			}
			t.Run(fmt.Sprintf("embeddings=%v/items=%v/%s", embedding, mode.item, fault), func(t *testing.T) {
				h := &batchProjectionHTTP{objects: map[string]map[string]any{}}
				s := weaviate.New("http://projection.invalid")
				s.Client = &http.Client{Transport: h}
				g := content.Generation{ID: "generation", Collection: "BatchObjects", SpaceID: "space", ItemKeywordsProjected: mode.item}
				seg := content.Segmentation{ID: "segmentation", VersionID: "version"}
				var data []content.EmbeddingData
				segmentCount := 101
				itemAtBoundary := mode.item && !embedding && fault == ""
				if itemAtBoundary {
					segmentCount = 100
				}
				for i := range segmentCount {
					id := fmt.Sprintf("segment-%03d", i)
					seg.Segments = append(seg.Segments, content.Segment{ID: id, PartKey: "body", Text: "example text"})
					vector := []float32{1}
					raw, err := content.VectorBytes(vector)
					if err != nil {
						t.Fatal(err)
					}
					data = append(data, content.EmbeddingData{Artifact: content.Embedding{Organization: "organization", SpaceID: "space", SegmentID: id, Payload: content.Blob{SHA256: content.Hash(raw)}}, Vector: vector})
				}
				publishSegments := func() error {
					return s.Publish(context.Background(), g, "organization", "corpus", "example-feed", content.Version{ID: "version"}, seg)
				}
				publish := publishSegments
				if embedding {
					if err := publish(); err != nil {
						t.Fatal(err)
					}
					if fault == "removed while batching" {
						if err := s.PublishEmbeddings(context.Background(), g, "organization", data[:1]); err != nil {
							t.Fatal(err)
						}
						for id, object := range h.objects {
							if object["vectors"] != nil {
								h.removeID = id
							}
						}
					}
					h.batches, h.submitted = nil, nil
					publish = func() error { return s.PublishEmbeddings(context.Background(), g, "organization", data) }
				}
				h.reads, h.lists = 0, 0
				h.fault = fault
				err := publish()
				wantError := fault != "" && fault != "lost response"
				if (err != nil) != wantError {
					t.Fatalf("publication error=%v; wantError=%v", err, wantError)
				}
				if strings.HasPrefix(fault, "HTTP ") && !strings.Contains(err.Error(), fault) {
					t.Fatalf("HTTP rejection error=%v; want status %s", err, fault)
				}
				firstBatch := 100
				if fault == "removed while batching" || itemAtBoundary {
					firstBatch = 99
				}
				if len(h.batches) == 0 || h.batches[0] != firstBatch {
					t.Fatalf("batch sizes=%v; want first bounded multi-object batch of %d", h.batches, firstBatch)
				}
				if fault == "" {
					wantReads := 0
					if embedding {
						wantReads = segmentCount // Only the anchors supplying text and metadata.
					}
					lastBatch := 1
					if mode.item && !embedding {
						lastBatch = 2
					}
					wantLists := 2
					if mode.item {
						wantLists = 4 // Check snapshots on publish, or confirm replacements after attachment.
					}
					if h.reads != wantReads || h.lists != wantLists || fmt.Sprint(h.batches) != fmt.Sprintf("[%d %d]", firstBatch, lastBatch) {
						t.Fatalf("acknowledged publication: reads=%d lists=%d batches=%v, want %d reads, %d lists, [%d %d] batches",
							h.reads, h.lists, h.batches, wantReads, wantLists, firstBatch, lastBatch)
					}
				}
				if fault == "lost response with bad vector" {
					if !strings.Contains(err.Error(), "embedding projection verification failed") {
						t.Fatalf("lost response with wrong vector: %v", err)
					}
					// Repair the corrupted provider fixture before exercising the normal restart.
					h.objects[h.submitted[0]]["vectors"] = map[string]any{"semantic_text_v1": []float32{1}}
				}
				if mode.item && fault == "removed while batching" {
					// A unified segment disappeared with its vectors. A
					// subsequent embedding replay requires publication first.
					if err := publish(); !errors.Is(err, retrieval.ErrProjectionMissing) {
						t.Fatalf("retry without segment = %v", err)
					}
					if err := publishSegments(); err != nil {
						t.Fatalf("republish seed: %v", err)
					}
				}
				// Every existing identity is immutable, including the BM25 item.
				prior := map[string]bool{}
				for id := range h.objects {
					prior[id] = true
				}
				beforeRetry := len(h.submitted)
				s = weaviate.New("http://projection.invalid")
				s.Client = &http.Client{Transport: h}
				if err := publish(); err != nil {
					t.Fatalf("retry: %v", err)
				}
				for _, id := range h.submitted[beforeRetry:] {
					if prior[id] {
						t.Fatalf("partial retry rewrote verified object %s", id)
					}
				}
				for _, n := range h.batches {
					if n < 1 || n > 100 {
						t.Fatalf("batch sizes=%v exceed bounds", h.batches)
					}
				}
				wantObjects := segmentCount
				if embedding && !mode.item {
					wantObjects *= 2
				}
				if mode.item {
					wantObjects++
				}
				if len(h.objects) != wantObjects {
					t.Fatalf("stored objects=%d want %d", len(h.objects), wantObjects)
				}
				if embedding {
					vectorsBySegment := make(map[string]int)
					for _, object := range h.objects {
						if object["vectors"] != nil {
							properties := object["properties"].(map[string]any)
							vectorsBySegment[properties["segmentId"].(string)]++
						}
					}
					for _, segment := range seg.Segments {
						if count := vectorsBySegment[segment.ID]; count != 1 {
							t.Fatalf("segment %s has %d vector objects, want 1", segment.ID, count)
						}
					}
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

func TestProjectionRejectsNullInventory(t *testing.T) {
	for _, nullGet := range []bool{true, false} {
		t.Run(fmt.Sprintf("nullGet=%t", nullGet), func(t *testing.T) {
			ctx := context.Background()
			h := &batchProjectionHTTP{objects: map[string]map[string]any{}}
			s := weaviate.New("http://projection.invalid")
			s.Client = &http.Client{Transport: h}
			g := content.Generation{ID: "generation", Collection: "BatchObjects", SpaceID: "space"}
			seg := content.Segmentation{ID: "segmentation", VersionID: "version", Segments: []content.Segment{{ID: "segment", PartKey: "body", Text: "example"}}}
			publish := func() error {
				return s.Publish(ctx, g, "organization", "corpus", "feed", content.Version{ID: "version"}, seg)
			}
			if err := publish(); err != nil {
				t.Fatal(err)
			}
			h.nullGet, h.nullAt = nullGet, h.lists+1
			if err := publish(); err == nil || len(h.submitted) != 1 {
				t.Fatalf("null anchor inventory: err=%v, writes=%d, want error and no replay write", err, len(h.submitted))
			}

			attach := func(value float32) error {
				return s.PublishEmbeddings(ctx, g, "organization", []content.EmbeddingData{batchEmbedding(t, "segment", value)})
			}
			if err := attach(1); err != nil {
				t.Fatal(err)
			}
			h.nullAt = h.lists + 2 // After the replacement write.
			if err := attach(2); err == nil || len(h.objects) != 3 {
				t.Fatalf("null cleanup inventory: err=%v, objects=%d, want error and both embeddings", err, len(h.objects))
			}
			if err := attach(2); err != nil || len(h.objects) != 2 {
				t.Fatalf("cleanup retry: err=%v, objects=%d, want anchor and replacement", err, len(h.objects))
			}
		})
	}
}

func batchEmbedding(t *testing.T, segment string, value float32) content.EmbeddingData {
	t.Helper()
	vector := []float32{value}
	raw, err := content.VectorBytes(vector)
	if err != nil {
		t.Fatal(err)
	}
	return content.EmbeddingData{Artifact: content.Embedding{
		Organization: "organization", SpaceID: "space", SegmentID: segment,
		Payload: content.Blob{SHA256: content.Hash(raw)},
	}, Vector: vector}
}

// Hold each batch until both writers have finished preflight, then commit
// both batches before acknowledging either write.
type overlappingBatches struct {
	mu        sync.Mutex
	next      http.RoundTripper
	arrived   int
	committed int
	ready     chan struct{}
	both      chan struct{}
}

func (h *overlappingBatches) RoundTrip(r *http.Request) (*http.Response, error) {
	write := r.Method == http.MethodPost && r.URL.Path == "/v1/batch/objects"
	if write {
		h.mu.Lock()
		h.arrived++
		if h.arrived == 2 {
			close(h.ready)
		}
		h.mu.Unlock()
		select {
		case <-h.ready:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}

	h.mu.Lock()
	res, err := h.next.RoundTrip(r)
	if write {
		h.committed++
		if h.committed == 2 {
			close(h.both)
		}
	}
	h.mu.Unlock()
	if write && err == nil {
		select {
		case <-h.both:
		case <-r.Context().Done():
			res.Body.Close()
			return nil, r.Context().Err()
		}
	}
	return res, err
}

func TestConcurrentEmbeddingReplacementsConverge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h := &batchProjectionHTTP{objects: map[string]map[string]any{}}
	store := weaviate.New("http://projection.invalid")
	store.Client = &http.Client{Transport: h}
	g := content.Generation{ID: "generation", Collection: "BatchObjects", SpaceID: "space"}
	seg := content.Segmentation{ID: "segmentation", VersionID: "version"}
	var inputs [3][]content.EmbeddingData
	for _, id := range []string{"first", "second", "third"} {
		seg.Segments = append(seg.Segments, content.Segment{ID: id, PartKey: "body", Text: "example"})
		for i := range inputs {
			inputs[i] = append(inputs[i], batchEmbedding(t, id, float32(i+1)))
		}
	}
	if err := store.Publish(ctx, g, "organization", "corpus", "feed", content.Version{ID: "version"}, seg); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishEmbeddings(ctx, g, "organization", inputs[0]); err != nil {
		t.Fatal(err)
	}
	before := make(map[string]bool)
	for id, object := range h.objects {
		before[id] = object["vectors"] == nil
	}
	lists := h.lists
	gate := &overlappingBatches{next: h, ready: make(chan struct{}), both: make(chan struct{})}
	done := make(chan error, 2)
	for i := 1; i < len(inputs); i++ {
		worker := weaviate.New("http://projection.invalid")
		worker.Client = &http.Client{Transport: gate}
		go func() { done <- worker.PublishEmbeddings(ctx, g, "organization", inputs[i]) }()
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
	if h.lists-lists != 4 {
		t.Errorf("two replacement batches used %d listings, want two batched listings each", h.lists-lists)
	}
	survivors := make(map[string]int)
	for id, object := range h.objects {
		segment := object["properties"].(map[string]any)["segmentId"].(string)
		if anchor, old := before[id]; old && !anchor {
			t.Errorf("original embedding %s for %s survived", id, segment)
		}
		if object["vectors"] != nil {
			survivors[segment]++
		}
	}
	for id, anchor := range before {
		if anchor && h.objects[id] == nil {
			t.Errorf("lexical anchor %s was deleted", id)
		}
	}
	for _, segment := range seg.Segments {
		if survivors[segment.ID] != 1 {
			t.Errorf("segment %s has %d surviving embeddings, want one", segment.ID, survivors[segment.ID])
		}
	}
}
