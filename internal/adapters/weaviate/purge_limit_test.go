package weaviate_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// slowPurgeStore is an index that times out deletes of more than threshold
// objects. It records what the adapter asked of it; adaptation belongs to the
// real Store, and no wait or production-only hook is needed at the wire.
type slowPurgeStore struct {
	*fakeIndex
	mu               sync.Mutex
	threshold        int
	windows          []int
	budgets          []time.Duration
	failure          error
	selectionFailure error
}

// errIndexTimeout is the reply of an index that gave up on a request.
var errIndexTimeout = status.Error(codes.DeadlineExceeded, "context deadline exceeded")

func newSlowPurgeStore(t *testing.T, objects, threshold int) *slowPurgeStore {
	t.Helper()
	s := &slowPurgeStore{fakeIndex: newFakeIndex(t), threshold: threshold}
	for i := 1; i <= objects; i++ {
		s.seed(fmt.Sprintf("00000000-0000-0000-0000-%012d", i), "organization", "org", "corpusId", "corpus", "generationId", "generation")
	}
	s.Call = func(ctx context.Context, method string, request proto.Message, apply func() (proto.Message, error)) (proto.Message, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Errorf("%s has no deadline", method)
		}
		s.budgets = append(s.budgets, time.Until(deadline))
		var filters *pb.Filters
		switch r := request.(type) {
		case *pb.SearchRequest:
			filters = r.Filters
		case *pb.BatchDeleteRequest:
			filters = r.Filters
		}
		// Every selection and delete stays inside one Corpus's generation.
		if got := fences(filters); got["organization"] != "org" || got["corpusId"] != "corpus" || got["generationId"] != "generation" {
			t.Errorf("%s without the ownership fence: %v", method, got)
		}
		if deletion, ok := request.(*pb.BatchDeleteRequest); ok {
			if deletion.DryRun {
				if s.selectionFailure != nil {
					return nil, s.selectionFailure
				}
			} else {
				window := len(filterIDs(deletion.Filters))
				s.windows = append(s.windows, window)
				if s.failure != nil {
					return nil, s.failure
				}
				if window > s.threshold {
					return nil, errIndexTimeout
				}
			}
		}
		return apply()
	}
	return s
}

func (s *slowPurgeStore) set(change func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change()
}

func (s *slowPurgeStore) seen() (windows []int, budgets []time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.windows...), append([]time.Duration(nil), s.budgets...)
}

// The dependency times out large deletes; the window halves on each timeout
// and grows again after full windows.
func TestGenerationPurgeAdaptsToSlowStore(t *testing.T) {
	provider := newSlowPurgeStore(t, 700, 64)
	store := provider.open(t)
	deleted, complete := 0, false
	for attempt := 0; attempt < 80; attempt++ {
		before := len(provider.ids())
		result, err := store.PurgeGeneration(context.Background(), "QuivrTextV4", "org", "corpus", "generation")
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unexpected purge failure: %v", err)
		}
		if err == nil && result.RemainingAtLeast != before-result.Deleted {
			t.Fatalf("window of %d out of %d reported %d remaining", result.Deleted, before, result.RemainingAtLeast)
		}
		deleted += result.Deleted
		if deleted >= 128 {
			provider.set(func() { provider.threshold = 256 })
		}
		if result.Complete {
			complete = true
			break
		}
	}
	windows, budgets := provider.seen()
	if survivors := len(provider.ids()); !complete || deleted != 700 || survivors != 0 {
		t.Fatalf("slow store did not drain: complete=%v confirmed=%d survivors=%d windows=%v", complete, deleted, survivors, windows)
	}
	if len(windows) < 4 || windows[0] != 256 || windows[1] != 128 || windows[2] != 64 {
		t.Fatalf("timeout windows %v, want initial 256 then 128 then 64", windows)
	}
	grew := false
	for _, n := range windows[3:] {
		grew = grew || n > 64
		if n > 256 {
			t.Fatalf("unbounded delete window %d", n)
		}
	}
	for _, budget := range budgets {
		if budget < 59*time.Second || budget > time.Minute {
			t.Fatalf("purge request budget %s, want 60s independently of ordinary 4s requests", budget)
		}
	}
	if !grew {
		t.Fatalf("successful windows never recovered: %v", windows)
	}
}

func TestGenerationPurgeTimeoutClassification(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		floor   bool
	}{
		{"request timeout floor", errIndexTimeout, true},
		{"other provider error", status.Error(codes.Unavailable, "index unavailable"), false},
		{"cancelled request", status.Error(codes.Canceled, "context canceled"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := newSlowPurgeStore(t, 300, 256)
			provider.failure = tc.failure
			store := provider.open(t)
			store.PurgeTimeout = 75 * time.Second
			for i := 0; i < 7; i++ {
				result, err := store.PurgeGeneration(context.Background(), "QuivrTextV4", "org", "corpus", "generation")
				if err == nil || result.Complete || result.Deleted != 0 {
					t.Fatalf("failed window: %+v %v", result, err)
				}
			}
			want := 256
			if tc.floor {
				want = 16
			}
			windows, budgets := provider.seen()
			if windows[6] != want {
				t.Fatalf("failure %v: windows %v, want last window %d", tc.failure, windows, want)
			}
			for _, budget := range budgets {
				if budget < 74*time.Second || budget > 75*time.Second {
					t.Fatalf("configured purge deadline: %s", budget)
				}
			}
			provider.set(func() { provider.failure = nil })
			for i := 0; i < 40; i++ {
				result, err := store.PurgeGeneration(context.Background(), "QuivrTextV4", "org", "corpus", "generation")
				if err != nil {
					t.Fatal(err)
				}
				if result.Complete {
					return
				}
			}
			t.Fatalf("did not recover after %s", tc.name)
		})
	}
}

func TestGenerationPurgeSelectionTimeoutRecovers(t *testing.T) {
	provider := newSlowPurgeStore(t, 1, 256)
	provider.selectionFailure = errIndexTimeout
	store := provider.open(t)
	result, err := store.PurgeGeneration(context.Background(), "QuivrTextV4", "org", "corpus", "generation")
	if windows, _ := provider.seen(); err == nil || result.Complete || result.Deleted != 0 || len(windows) != 0 {
		t.Fatalf("selection timeout: %+v %v deletes=%v", result, err, windows)
	}
	provider.set(func() { provider.selectionFailure = nil })
	result, err = store.PurgeGeneration(context.Background(), "QuivrTextV4", "org", "corpus", "generation")
	if err != nil || result.Deleted != 1 || result.Complete {
		t.Fatalf("selection recovery: %+v %v", result, err)
	}
	// Only a zero-match selection proves completion.
	result, err = store.PurgeGeneration(context.Background(), "QuivrTextV4", "org", "corpus", "generation")
	if err != nil || !result.Complete {
		t.Fatalf("empty confirmation: %+v %v", result, err)
	}
}

// A Version purge is complete only when the index proves nothing is left: a
// count after the delete finds no match, or the delete matched nothing. A
// delete the index capped, a failed object or a count that cannot be read
// keeps the purge item for the next sweep.
func TestPurgeCompletenessFailsClosed(t *testing.T) {
	const key = "index-secret"
	// failed is a delete reply that could not delete the first failures
	// objects out of matches.
	failed := func(matches, failures int) *pb.BatchDeleteReply {
		reason := "context deadline exceeded for " + key
		reply := &pb.BatchDeleteReply{Matches: int64(matches), Successful: int64(matches - failures), Failed: int64(failures)}
		for i := 1; i <= failures; i++ {
			id := uuid.MustParse(fmt.Sprintf("00000000-0000-0000-0000-%012d", i))
			reply.Objects = append(reply.Objects, &pb.BatchDeleteObject{Uuid: id[:], Error: &reason})
		}
		return reply
	}
	for _, tc := range []struct {
		name string
		// objects of the Version in the index, and the most one delete removes.
		objects, deleteCap int
		// delete and count answer for the index when set.
		delete    func() (*pb.BatchDeleteReply, error)
		count     func() (*pb.BatchDeleteReply, error)
		deleted   int
		remaining int
		complete  bool
		err       bool
		reason    string
	}{
		{name: "all deleted", objects: 3, deleted: 3, complete: true},
		{name: "capped by the index", objects: 3, deleteCap: 2, deleted: 2, remaining: 1},
		{name: "nothing left", complete: true},
		{name: "failed objects", objects: 3, delete: func() (*pb.BatchDeleteReply, error) {
			return failed(3, 1), nil
		}, deleted: 2, err: true, reason: "context deadline exceeded"},
		{name: "more failures than matches", objects: 3, delete: func() (*pb.BatchDeleteReply, error) {
			return failed(1, 2), nil
		}, err: true, reason: "result missing"},
		{name: "count unavailable", objects: 3, count: func() (*pb.BatchDeleteReply, error) {
			return nil, status.Error(codes.Unavailable, "index unavailable")
		}, deleted: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeIndex(t)
			fake.APIKey, fake.DeleteCap = key, tc.deleteCap
			for i := 1; i <= tc.objects; i++ {
				fake.seed(fmt.Sprintf("00000000-0000-0000-0000-%012d", i), "organization", "org", "versionId", "version")
			}
			equal := func(property, value string) *pb.Filters {
				return &pb.Filters{Operator: pb.Filters_OPERATOR_EQUAL, Target: &pb.FilterTarget{Target: &pb.FilterTarget_Property{Property: property}}, TestValue: &pb.Filters_ValueText{ValueText: value}}
			}
			want := &pb.Filters{Operator: pb.Filters_OPERATOR_AND, Filters: []*pb.Filters{equal("organization", "org"), equal("versionId", "version")}}
			fake.Call = func(_ context.Context, method string, request proto.Message, apply func() (proto.Message, error)) (proto.Message, error) {
				deletion, ok := request.(*pb.BatchDeleteRequest)
				if !ok {
					t.Errorf("unexpected %s", method)
					return apply()
				}
				if !proto.Equal(deletion.Filters, want) {
					t.Errorf("filter %v, want %v", deletion.Filters, want)
				}
				if answer := tc.delete; answer != nil && !deletion.DryRun {
					return answer()
				}
				if answer := tc.count; answer != nil && deletion.DryRun {
					return answer()
				}
				return apply()
			}
			r, err := fake.open(t).PurgeVersion(context.Background(), "QuivrTextV4", "org", "version")
			if (err != nil) != tc.err || r.Deleted != tc.deleted || r.Complete != tc.complete || r.RemainingAtLeast != tc.remaining {
				t.Fatalf("result %+v %v", r, err)
			}
			if tc.reason != "" && (err == nil || !strings.Contains(err.Error(), tc.reason)) {
				t.Fatalf("provider reason lost: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), key) {
				t.Fatalf("API key in the purge error: %v", err)
			}
		})
	}
}
