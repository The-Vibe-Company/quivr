package weaviate_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

// Both publication paths must batch, fail on an individual object or a
// rejected request, and reconcile interrupted writes without rewriting already
// verified identities. The fake index supplies only the remote object API.
func TestProjectionBatchesCheckObjectFailuresAndRetry(t *testing.T) {
	for _, mode := range []struct{ embedding, item bool }{{}, {embedding: true}, {item: true}, {embedding: true, item: true}} {
		embedding := mode.embedding
		for _, fault := range []string{"", "object error", "lost response", codes.ResourceExhausted.String(), codes.Internal.String(), "removed while batching"} {
			if fault == "removed while batching" && !embedding {
				continue
			}
			t.Run(fmt.Sprintf("embeddings=%v/items=%v/%s", embedding, mode.item, fault), func(t *testing.T) {
				h := newFakeIndex(t)
				var mu sync.Mutex
				var batches []int
				var submitted []string
				pending, removeID := "", ""
				h.Call = func(_ context.Context, _ string, request proto.Message, apply func() (proto.Message, error)) (proto.Message, error) {
					insert, ok := request.(*pb.BatchObjectsRequest)
					if !ok {
						return apply()
					}
					mu.Lock()
					batches = append(batches, len(insert.Objects))
					for _, object := range insert.Objects {
						submitted = append(submitted, object.Uuid)
					}
					injected := pending
					pending = ""
					mu.Unlock()
					switch injected {
					case codes.ResourceExhausted.String():
						return nil, status.Error(codes.ResourceExhausted, "injected rejection")
					case codes.Internal.String():
						return nil, status.Error(codes.Internal, "injected rejection")
					case "object error":
						// The index stores every object but the second.
						insert.Objects = append(insert.Objects[:1:1], insert.Objects[2:]...)
						if _, err := apply(); err != nil {
							return nil, err
						}
						return &pb.BatchObjectsReply{Errors: []*pb.BatchObjectsReply_BatchError{{Index: 1, Error: "injected object failure"}}}, nil
					case "lost response":
						if _, err := apply(); err != nil {
							return nil, err
						}
						return nil, status.Error(codes.Unavailable, "injected lost response after write")
					case "removed while batching":
						reply, err := apply()
						h.remove(removeID)
						return reply, err
					}
					return apply()
				}
				s := h.open(t)
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
					if fault == "removed while batching" {
						if err := s.PublishEmbeddings(context.Background(), g, "organization", data[:1]); err != nil {
							t.Fatal(err)
						}
						enriched := h.vectorIDs()
						if len(enriched) != 1 {
							t.Fatalf("enriched objects before the batch: %v", enriched)
						}
						removeID = enriched[0]
					}
					mu.Lock()
					batches = nil
					mu.Unlock()
					publish = func() error { return s.PublishEmbeddings(context.Background(), g, "organization", data) }
				}
				sizes := func() []int {
					mu.Lock()
					defer mu.Unlock()
					return append([]int(nil), batches...)
				}
				mu.Lock()
				pending = fault
				mu.Unlock()
				err := publish()
				rejected := fault == codes.ResourceExhausted.String() || fault == codes.Internal.String()
				wantError := fault == "object error" || rejected || fault == "removed while batching"
				if (err != nil) != wantError {
					t.Fatalf("publication error=%v; wantError=%v", err, wantError)
				}
				if rejected && !strings.Contains(err.Error(), fault) {
					t.Fatalf("rejection error=%v; want status %s", err, fault)
				}
				firstBatch := 100
				if fault == "removed while batching" {
					firstBatch = 99
				}
				if got := sizes(); len(got) == 0 || got[0] != firstBatch {
					t.Fatalf("batch sizes=%v; want first bounded multi-object batch of %d", got, firstBatch)
				}
				prior := map[string]bool{}
				for _, id := range h.ids() {
					prior[id] = true
				}
				mu.Lock()
				beforeRetry := len(submitted)
				mu.Unlock()
				if err := publish(); err != nil {
					t.Fatalf("retry: %v", err)
				}
				mu.Lock()
				retried := append([]string(nil), submitted[beforeRetry:]...)
				mu.Unlock()
				for _, id := range retried {
					if prior[id] {
						t.Fatalf("partial retry rewrote verified object %s", id)
					}
				}
				for _, n := range sizes() {
					if n < 1 || n > 100 {
						t.Fatalf("batch sizes=%v exceed bounds", sizes())
					}
				}
				wantObjects := 101
				if embedding {
					wantObjects = 202
				}
				if mode.item {
					wantObjects++
				}
				if stored := len(h.ids()); stored != wantObjects {
					t.Fatalf("stored objects=%d want %d", stored, wantObjects)
				}
				writes := len(sizes())
				if err := publish(); err != nil {
					t.Fatal(err)
				}
				if got := sizes(); len(got) != writes {
					t.Fatalf("verified retry rewrote objects: %v", got)
				}
			})
		}
	}
}
