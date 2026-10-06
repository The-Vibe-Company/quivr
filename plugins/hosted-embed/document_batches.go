package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

type documentResult struct {
	vectors [][]float32
	err     error
}

type documentRequest struct {
	ctx        context.Context
	inputs     []string
	invocation string
	result     chan documentResult
}

type documentBatch struct {
	organization  string
	requests      []documentRequest
	items, tokens int
	ctx           context.Context
	timer         *time.Timer
	once          sync.Once
}

// documentBatcher combines only document inputs from the same Organization and
// configured plugin process. Provider admission and throttling remain shared
// with queries. No worker is kept alive when the queue is idle.
type documentBatcher struct {
	provider provider
	slots    chan struct{}
	mu       sync.Mutex
	pending  map[string]*documentBatch
}

func (b *documentBatcher) embed(ctx context.Context, organization string, inputs []string, invocation string) ([][]float32, error) {
	result, err := b.submit(ctx, organization, inputs, invocation)
	if err != nil {
		return nil, err
	}
	select {
	case out := <-result:
		return out.vectors, out.err
	case <-ctx.Done():
		return nil, quivrplugin.RetryableIngestError("provider_unavailable", "document embedding cancelled")
	}
}

// submit transfers a bounded subrequest to the queue. Its slot is retained
// until dispatch completes, even when the caller cancels while waiting.
func (b *documentBatcher) submit(ctx context.Context, organization string, inputs []string, invocation string) (<-chan documentResult, error) {
	select {
	case b.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, quivrplugin.RetryableIngestError("provider_unavailable", "document admission cancelled")
	}
	job := documentRequest{ctx: ctx, inputs: inputs, invocation: invocation, result: make(chan documentResult, 1)}
	c := b.provider.config
	tokens := 0
	for _, input := range inputs {
		tokens += len(input) + specialTokens
	}
	b.mu.Lock()
	batch := b.pending[organization]
	if batch != nil && (batch.items+len(inputs) > c.BatchSize || batch.tokens+tokens > c.BatchTokens) {
		delete(b.pending, organization)
		batch.timer.Stop()
		go b.flush(batch)
		batch = nil
	}
	if batch == nil {
		batch = &documentBatch{organization: organization, ctx: context.WithoutCancel(ctx)}
		b.pending[organization] = batch
		batch.timer = time.AfterFunc(time.Duration(c.BatchWaitMS)*time.Millisecond, func() { b.flush(batch) })
	}
	batch.requests = append(batch.requests, job)
	batch.items += len(inputs)
	batch.tokens += tokens
	if batch.items == c.BatchSize || batch.tokens == c.BatchTokens {
		delete(b.pending, organization)
		batch.timer.Stop()
		go b.flush(batch)
	}
	b.mu.Unlock()
	return job.result, nil
}

func (b *documentBatcher) flush(batch *documentBatch) {
	batch.once.Do(func() {
		ctx, cancel := context.WithTimeout(batch.ctx, time.Duration(b.provider.config.CallBudgetMS)*time.Millisecond)
		defer cancel()
		// Keep collecting into this bounded batch while another provider call
		// occupies admission. Sparse traffic waits only the configured window.
		err := b.provider.gate.acquire(ctx)
		b.mu.Lock()
		if b.pending[batch.organization] == batch {
			delete(b.pending, batch.organization)
		}
		requests := batch.requests
		b.mu.Unlock()
		if err != nil {
			for _, job := range requests {
				b.reply(job, documentResult{err: quivrplugin.RetryableIngestError("provider_unavailable", "provider admission cancelled")})
			}
			return
		}
		active := requests[:0]
		for _, job := range requests {
			if job.ctx.Err() == nil {
				active = append(active, job)
			} else {
				b.reply(job, documentResult{err: job.ctx.Err()})
			}
		}
		if len(active) == 0 {
			b.provider.gate.release()
			return
		}
		b.send(ctx, active, true)
	})
}

func (b *documentBatcher) send(ctx context.Context, jobs []documentRequest, admitted bool) {
	// A caller may cancel during a shared call or an earlier refusal probe.
	// Remove it before making another request, and settle its queue slot once.
	active := jobs[:0]
	for _, job := range jobs {
		if job.ctx.Err() != nil {
			b.reply(job, documentResult{err: job.ctx.Err()})
		} else {
			active = append(active, job)
		}
	}
	jobs = active
	if len(jobs) == 0 {
		if admitted {
			b.provider.gate.release()
		}
		return
	}
	var inputs, invocations []string
	for _, job := range jobs {
		inputs = append(inputs, job.inputs...)
		invocations = append(invocations, job.invocation)
	}
	vectors, err := b.provider.request(ctx, inputs, "document", invocations, admitted)
	var refusal *inputRefusal
	if err != nil && len(jobs) > 1 && errors.As(err, &refusal) {
		// A provider may refuse one member of a combined request. Isolate it
		// so healthy Versions do not inherit another Version's refusal.
		half := len(jobs) / 2
		b.send(ctx, jobs[:half], false)
		b.send(ctx, jobs[half:], false)
		return
	}
	offset := 0
	for _, job := range jobs {
		result := documentResult{err: err}
		if err == nil {
			result.vectors = vectors[offset : offset+len(job.inputs)]
		}
		offset += len(job.inputs)
		b.reply(job, result)
	}
}

func (b *documentBatcher) reply(job documentRequest, result documentResult) {
	job.result <- result
	<-b.slots
}
