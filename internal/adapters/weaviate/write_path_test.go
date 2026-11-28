package weaviate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

// writeGRPC fakes the Weaviate gRPC write and read surface. The adapter owns
// retry selection, verification and timeouts; the fake only records requests
// and replies with the per-object errors a test scripts for each call.
type writeGRPC struct {
	pb.WeaviateClient
	mu       sync.Mutex
	calls    [][]string
	record   bool // keep written objects; only tests that inspect payloads set it
	objects  []*pb.BatchObject
	failures func(call int, objects []*pb.BatchObject) []*pb.BatchObjectsReply_BatchError
	callErr  func(call int) error
	deadline []time.Duration
	anchors  map[string]*pb.Properties
}

func (f *writeGRPC) BatchObjects(ctx context.Context, req *pb.BatchObjectsRequest, _ ...grpc.CallOption) (*pb.BatchObjectsReply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := len(f.calls)
	ids := make([]string, len(req.GetObjects()))
	for i, object := range req.GetObjects() {
		ids[i] = object.GetUuid()
	}
	f.calls = append(f.calls, ids)
	if f.record {
		f.objects = append(f.objects, req.GetObjects()...)
	}
	if deadline, ok := ctx.Deadline(); ok {
		f.deadline = append(f.deadline, time.Until(deadline))
	}
	if f.callErr != nil {
		if err := f.callErr(call); err != nil {
			return nil, err
		}
	}
	reply := &pb.BatchObjectsReply{}
	if f.failures != nil {
		reply.Errors = f.failures(call, req.GetObjects())
	}
	return reply, nil
}

func (f *writeGRPC) Search(_ context.Context, req *pb.SearchRequest, _ ...grpc.CallOption) (*pb.SearchReply, error) {
	if !req.GetProperties().GetReturnAllNonrefProperties() {
		return &pb.SearchReply{}, nil
	}
	ids := req.GetFilters().GetValueTextArray().GetValues()
	results := make([]*pb.SearchResult, 0, len(ids))
	for _, id := range ids {
		if props, ok := f.anchors[id]; ok {
			results = append(results, &pb.SearchResult{Metadata: &pb.MetadataResult{Id: id}, Properties: &pb.PropertiesResult{NonRefProps: props}})
		}
	}
	return &pb.SearchReply{Results: results}, nil
}

func (f *writeGRPC) sent(call int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[call]
}

// objectREST serves schema calls and stored objects by identity, optionally
// holding each object read for latency on the caller's clock.
type objectREST struct {
	objects  map[string][]byte
	latency  time.Duration
	inFlight atomic.Int32
	peak     atomic.Int32
	reads    atomic.Int32
}

func (h *objectREST) RoundTrip(r *http.Request) (*http.Response, error) {
	reply := func(code int, body []byte) (*http.Response, error) {
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header), Request: r}, nil
	}
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/objects/") {
		n := h.inFlight.Add(1)
		defer h.inFlight.Add(-1)
		for peak := h.peak.Load(); n > peak && !h.peak.CompareAndSwap(peak, n); peak = h.peak.Load() {
		}
		h.reads.Add(1)
		if h.latency > 0 {
			time.Sleep(h.latency)
		}
		id := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]
		if body, ok := h.objects[id]; ok {
			return reply(http.StatusOK, body)
		}
		return reply(http.StatusNotFound, nil)
	}
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/schema/") {
		return reply(http.StatusOK, []byte(`{"properties":[]}`))
	}
	return reply(http.StatusOK, []byte(`{}`))
}

func writeStore(rest *objectREST, client *writeGRPC) *Store {
	s := newStore("http://projection.invalid", rest, nil)
	s.grpc = client
	return s
}

func writeObjects(n, dims int) []projectionObject {
	objects := make([]projectionObject, n)
	for i := range objects {
		vector := make([]float32, dims)
		for j := range vector {
			vector[j] = float32(i+1) / float32(j+1)
		}
		objects[i] = projectionObject{
			Class: "WriteObjects", ID: uuidOf(fmt.Sprintf("write-%03d", i)),
			Properties: map[string]any{"segmentId": fmt.Sprintf("segment-%03d", i)},
			Vectors:    map[string][]float32{legacyVector: vector},
		}
	}
	return objects
}

// storedREST records objects as the provider would return them after a write
// whose acknowledgement was lost.
func storedREST(objects []projectionObject) *objectREST {
	rest := &objectREST{objects: make(map[string][]byte, len(objects))}
	for _, object := range objects {
		raw, _ := json.Marshal(storedObject{Properties: object.Properties, Vectors: object.Vectors})
		rest.objects[object.ID] = raw
	}
	return rest
}

func failAt(indexes ...int32) func(int, []*pb.BatchObject) []*pb.BatchObjectsReply_BatchError {
	return func(call int, _ []*pb.BatchObject) []*pb.BatchObjectsReply_BatchError {
		if call > 0 {
			return nil
		}
		errs := make([]*pb.BatchObjectsReply_BatchError, len(indexes))
		for i, index := range indexes {
			errs[i] = &pb.BatchObjectsReply_BatchError{Index: index, Error: "shard https://weaviate.invalid/?api_key=secret busy"}
		}
		return errs
	}
}

// insertUntilAccepted plays the outer job's retry of a rejected publication
// once, so the count of re-sent objects covers whichever layer retries them.
func insertUntilAccepted(s *Store, objects []projectionObject) error {
	if err := s.insertBatch(context.Background(), objects); err == nil {
		return nil
	}
	return s.insertBatch(context.Background(), objects)
}

// A provider that rejects a few objects in an otherwise accepted batch must
// cost a re-send of those objects only, never of the whole batch.
func TestGRPCBatchResendsOnlyRejectedObjects(t *testing.T) {
	objects := writeObjects(projectionBatch, 4)
	client := &writeGRPC{failures: failAt(7, 42, 99)}
	s := writeStore(&objectREST{}, client)
	if err := insertUntilAccepted(s, objects); err != nil {
		t.Fatalf("partial rejection then acceptance: %v", err)
	}
	if len(client.calls) != 2 {
		t.Fatalf("batch calls=%d, want the first batch and one re-send", len(client.calls))
	}
	want := []string{objects[7].ID, objects[42].ID, objects[99].ID}
	if got := client.sent(1); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("re-sent %d objects, want only the %d rejected %v", len(got), len(want), want)
	}
}

// Objects the provider keeps rejecting fail the publication after a bounded
// number of re-sends, with an error that carries no provider text.
func TestGRPCBatchPersistentRejectionIsBoundedAndSanitized(t *testing.T) {
	objects := writeObjects(projectionBatch, 4)
	poisoned := objects[1].ID
	for _, tc := range []struct {
		name      string
		reply     func(int, []*pb.BatchObject) []*pb.BatchObjectsReply_BatchError
		wantCalls int
		wantErr   string
	}{
		{"rejected", func(_ int, sent []*pb.BatchObject) []*pb.BatchObjectsReply_BatchError {
			for i, object := range sent {
				if object.GetUuid() == poisoned {
					return []*pb.BatchObjectsReply_BatchError{{Index: int32(i), Error: "https://weaviate.invalid/?api_key=secret"}}
				}
			}
			return nil
		}, grpcBatchAttempts, "projection publication failed (1 objects rejected)"},
		{"index out of range", func(int, []*pb.BatchObject) []*pb.BatchObjectsReply_BatchError {
			return []*pb.BatchObjectsReply_BatchError{{Index: projectionBatch, Error: "Bearer secret"}}
		}, 1, "projection publication response invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &writeGRPC{failures: tc.reply}
			s := writeStore(&objectREST{}, client)
			err := s.insertBatch(context.Background(), objects)
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("err=%v, want %q", err, tc.wantErr)
			}
			for _, leak := range []string{"https://", "weaviate.invalid", "api_key", "secret", "Bearer"} {
				if strings.Contains(err.Error(), leak) {
					t.Fatalf("error %q leaks provider text %q", err, leak)
				}
			}
			if len(client.calls) != tc.wantCalls {
				t.Fatalf("batch calls=%d, want %d", len(client.calls), tc.wantCalls)
			}
			for _, retry := range client.calls[1:] {
				if len(retry) != 1 || retry[0] != poisoned {
					t.Fatalf("re-sent %v, want only the rejected object %s", retry, poisoned)
				}
			}
		})
	}
}

// A lost acknowledgement is reconciled by reading every object back. With a
// 5 ms read, 100 serial reads take 500 ms of virtual time; bounded parallel
// reads must finish within a quarter of that and never exceed the bound.
func TestGRPCLostResponseVerifiesInBoundedParallel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		objects := writeObjects(projectionBatch, 4)
		rest := storedREST(objects)
		rest.latency = 5 * time.Millisecond
		client := &writeGRPC{callErr: func(int) error { return status.Error(codes.Unavailable, "connection reset") }}
		s := writeStore(rest, client)
		start := time.Now()
		if err := s.insertBatch(context.Background(), objects); err != nil {
			t.Fatalf("lost response verification: %v", err)
		}
		elapsed := time.Since(start)
		t.Logf("verified %d objects in %v virtual (peak %d concurrent reads)", rest.reads.Load(), elapsed, rest.peak.Load())
		if rest.reads.Load() != projectionBatch {
			t.Fatalf("reads=%d, want %d", rest.reads.Load(), projectionBatch)
		}
		if elapsed > 125*time.Millisecond {
			t.Fatalf("verification took %v virtual, want <= 125ms", elapsed)
		}
		if peak := rest.peak.Load(); peak > verifyConcurrency {
			t.Fatalf("peak concurrent reads=%d, want <= %d", peak, verifyConcurrency)
		}

		// One corrupt stored vector still fails the reconciliation.
		raw, _ := json.Marshal(storedObject{Properties: objects[60].Properties, Vectors: map[string][]float32{legacyVector: {9, 9, 9, 9}}})
		rest.objects[objects[60].ID] = raw
		if err := s.insertBatch(context.Background(), objects); err == nil || err.Error() != "embedding projection verification failed" {
			t.Fatalf("corrupt lost-response object: err=%v", err)
		}
	})
}

// The write deadline grows with the encoded request so a large batch is not
// cut off by a deadline sized for an empty one, and stays bounded.
func TestGRPCWriteDeadlineScalesWithBatchBytes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &writeGRPC{}
		s := writeStore(&objectREST{}, client)
		for _, dims := range []int{4, 4096} {
			if err := s.insertBatch(context.Background(), writeObjects(projectionBatch, dims)); err != nil {
				t.Fatal(err)
			}
		}
		small, large := client.deadline[0], client.deadline[1]
		if small < s.Client.Timeout || large <= small || large > maxWriteTimeout {
			t.Fatalf("write deadlines small=%v large=%v, want base %v <= small < large <= %v", small, large, s.Client.Timeout, maxWriteTimeout)
		}
	})
}

// Embedding writes keep their deterministic identity and send exactly the
// payload whose checksum was validated, so a replay is byte-identical.
func TestGRPCEmbeddingWriteReplaysIdenticalPayload(t *testing.T) {
	g := content.Generation{ID: "generation", Collection: "WriteObjects", SpaceID: "space"}
	data, anchors := benchmarkEmbeddings(t, g, "organization", 2, 3)
	client := &writeGRPC{anchors: anchors, record: true}
	s := writeStore(&objectREST{}, client)
	for range 2 {
		if err := s.PublishEmbeddings(context.Background(), g, "organization", data); err != nil {
			t.Fatal(err)
		}
	}
	if len(client.objects) != 4 {
		t.Fatalf("written objects=%d, want 2 per publication", len(client.objects))
	}
	if got := client.objects[0].GetUuid(); got != "82e41200-a004-572d-ab8e-153957fe525f" {
		t.Fatalf("enriched identity=%s changed", got)
	}
	for i, object := range client.objects {
		first := client.objects[i%2]
		vectors := object.GetVectors()
		if object.GetUuid() != first.GetUuid() || len(vectors) != 1 || vectors[0].GetName() != legacyVector {
			t.Fatalf("object %d: uuid=%s vectors=%v, want replay of %s with one %s vector", i, object.GetUuid(), vectors, first.GetUuid(), legacyVector)
		}
		if sum := content.Hash(vectors[0].GetVectorBytes()); sum != data[i%2].Artifact.Payload.SHA256 {
			t.Fatalf("object %d sent payload %s, want validated %s", i, sum, data[i%2].Artifact.Payload.SHA256)
		}
	}
}

func benchmarkEmbeddings(b testing.TB, g content.Generation, org string, n, dims int) ([]content.EmbeddingData, map[string]*pb.Properties) {
	b.Helper()
	data := make([]content.EmbeddingData, n)
	anchors := make(map[string]*pb.Properties, n)
	text := func(v string) *pb.Value { return &pb.Value{Kind: &pb.Value_TextValue{TextValue: v}} }
	for i := range data {
		segment := fmt.Sprintf("segment-%03d", i)
		vector := make([]float32, dims)
		for j := range vector {
			vector[j] = float32(i+1) / float32(j+1)
		}
		raw, err := content.VectorBytes(vector)
		if err != nil {
			b.Fatal(err)
		}
		data[i] = content.EmbeddingData{Artifact: content.Embedding{Organization: org, SpaceID: g.SpaceID, SegmentID: segment, Payload: content.Blob{SHA256: content.Hash(raw)}}, Vector: vector}
		anchors[objectID(org, g.ID, segment)] = &pb.Properties{Fields: map[string]*pb.Value{
			"organization": text(org), "corpusId": text("corpus"), "generationId": text(g.ID),
			"segmentId": text(segment), "body": text("example body"), "title": text("example title"),
		}}
	}
	return data, anchors
}

func BenchmarkPublishEmbeddings(b *testing.B) {
	g := content.Generation{ID: "generation", Collection: "WriteObjects", SpaceID: "space"}
	data, anchors := benchmarkEmbeddings(b, g, "organization", projectionBatch, 768)
	client := &writeGRPC{anchors: anchors}
	s := writeStore(&objectREST{}, client)
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if err := s.PublishEmbeddings(ctx, g, "organization", data); err != nil {
			b.Fatal(err)
		}
		client.calls = client.calls[:0]
	}
}

func BenchmarkGRPCPartialBatchFailure(b *testing.B) {
	objects := writeObjects(projectionBatch, 768)
	b.ReportAllocs()
	var resent int
	for b.Loop() {
		client := &writeGRPC{failures: failAt(7, 42, 99)}
		s := writeStore(&objectREST{}, client)
		if err := insertUntilAccepted(s, objects); err != nil {
			b.Fatal(err)
		}
		for _, call := range client.calls[1:] {
			resent += len(call)
		}
	}
	b.ReportMetric(float64(resent)/float64(b.N), "resent-objects/op")
}

func BenchmarkGRPCLostResponseVerify(b *testing.B) {
	objects := writeObjects(projectionBatch, 768)
	rest := storedREST(objects)
	client := &writeGRPC{callErr: func(int) error { return status.Error(codes.Unavailable, "connection reset") }}
	s := writeStore(rest, client)
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if err := s.insertBatch(ctx, objects); err != nil {
			b.Fatal(err)
		}
		client.calls = client.calls[:0]
	}
}
