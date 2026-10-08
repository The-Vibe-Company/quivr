package monitoring

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
)

type matchRequest struct {
	ctx     context.Context
	matches []MatchCommit
	result  chan matchResult
}
type matchResult struct {
	outcomes []string
	err      error
}

// Only already validated positives wait here. Provider calls and negative
// decisions retain their existing paths. Each flush shares an Organization's
// journal across at most sixteen ready groups, with a two-millisecond partial
// flush; the evaluation worker count bounds outstanding requests.
type readyMatchQueue struct {
	requests []matchRequest
	ready    chan struct{}
}

type matchCommitter struct {
	EvaluationStore
	store  MatchBatchStore
	ctx    context.Context
	mu     sync.Mutex
	queues map[string]*readyMatchQueue
	wg     sync.WaitGroup
}

func startMatchCommitter(ctx context.Context, store EvaluationStore) (EvaluationStore, func()) {
	batched, ok := store.(MatchBatchStore)
	if !ok {
		return store, func() {}
	}
	// Engine.loop controls admission; ready requests already own work that
	// may finish during managed shutdown, until the shared grace deadline.
	ctx, cancel := context.WithCancel(lifecycle.WorkContext(ctx))
	b := &matchCommitter{EvaluationStore: store, store: batched, ctx: ctx, queues: map[string]*readyMatchQueue{}}
	return b, func() { cancel(); b.wg.Wait() }
}

func (b *matchCommitter) CommitMatches(ctx context.Context, matches []MatchCommit) ([]string, error) {
	if len(matches) == 0 {
		return []string{}, nil
	}
	r := matchRequest{ctx: ctx, matches: append([]MatchCommit(nil), matches...), result: make(chan matchResult, 1)}
	for i := range r.matches {
		if r.matches[i].Context == nil {
			r.matches[i].Context = ctx
		}
	}
	b.mu.Lock()
	if err := ctx.Err(); err != nil {
		b.mu.Unlock()
		return nil, err
	}
	if err := b.ctx.Err(); err != nil {
		b.mu.Unlock()
		return nil, err
	}
	org := matches[0].Intent.Organization
	q := b.queues[org]
	if q == nil {
		q = &readyMatchQueue{ready: make(chan struct{}, 1)}
		b.queues[org] = q
		b.wg.Add(1)
		go b.run(org, q)
	}
	q.requests = append(q.requests, r)
	select {
	case q.ready <- struct{}{}:
	default:
	}
	b.mu.Unlock()
	// Every enqueued request receives its durable outcome, including on
	// cancellation. A queue exits only after fulfilling its last request.
	result := <-r.result
	return result.outcomes, result.err
}

func (b *matchCommitter) run(org string, q *readyMatchQueue) {
	defer b.wg.Done()
	for {
		b.mu.Lock()
		if len(q.requests) == 0 {
			delete(b.queues, org)
			b.mu.Unlock()
			return
		}
		n := min(16, len(q.requests))
		requests := append([]matchRequest(nil), q.requests[:n]...)
		q.requests = q.requests[n:]
		b.mu.Unlock()
		timer := time.NewTimer(2 * time.Millisecond)
	collect:
		for len(requests) < 16 {
			select {
			case <-q.ready:
				b.mu.Lock()
				n := min(16-len(requests), len(q.requests))
				requests = append(requests, q.requests[:n]...)
				q.requests = q.requests[n:]
				b.mu.Unlock()
			case <-timer.C:
				break collect
			case <-b.ctx.Done():
				break collect
			}
		}
		timer.Stop()
		group := make([]matchRequest, 0, len(requests))
		for _, r := range requests {
			if err := r.ctx.Err(); err != nil {
				r.result <- matchResult{err: err}
			} else {
				group = append(group, r)
			}
		}
		if len(group) > 0 {
			b.commit(group)
		}
	}
}

func (b *matchCommitter) commit(group []matchRequest) {
	ctx, cancel := context.WithCancel(b.ctx)
	stops := make([]func() bool, 0, len(group))
	var matches []MatchCommit
	for _, r := range group {
		stops = append(stops, context.AfterFunc(r.ctx, cancel))
		matches = append(matches, r.matches...)
	}
	outcomes, err := b.store.CommitMatches(ctx, matches)
	for _, stop := range stops {
		stop()
	}
	cancel()
	if err == nil && len(outcomes) != len(matches) {
		err = errors.New("shared match outcomes differ from inputs")
	}
	if err == nil {
		offset := 0
		for _, r := range group {
			next := offset + len(r.matches)
			r.result <- matchResult{outcomes: outcomes[offset:next]}
			offset = next
		}
		return
	}
	// A failed transaction exposes none of its events. Isolate each original
	// ready group using its own context so cancellation or invalid evidence in
	// one evaluation cannot stop healthy siblings from committing.
	for _, r := range group {
		result := matchResult{err: err}
		if r.ctx.Err() != nil {
			result.err = r.ctx.Err()
		} else if len(group) > 1 && b.ctx.Err() == nil {
			result.outcomes, result.err = b.store.CommitMatches(r.ctx, r.matches)
		}
		r.result <- result
	}
}
