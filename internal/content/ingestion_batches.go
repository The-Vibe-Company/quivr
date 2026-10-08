package content

import (
	"context"
	"sync"
	"time"
)

// PublicationCommit carries prepared objects and a keyword projection which
// has already been written externally. Canonical visibility is atomic.
type PublicationCommit struct {
	Work         Work
	Publication  Publication
	Segmentation Segmentation
	Generation   Generation
}

type IngestionCommitKind int

const (
	CommitPublication IngestionCommitKind = iota
	CommitMaterialization
	CommitBaseline
	CommitVectors
)

// IngestionCommit is ready canonical work. Context preserves each receipt's
// pinned plan and trace; cancellation stops its transaction before visibility.
type IngestionCommit struct {
	Context                context.Context
	Kind                   IngestionCommitKind
	Organization, RecordID string
	Publication            PublicationCommit
	Segmentation           Segmentation
	Generation             Generation
	Artifacts              []Embedding
}
type IngestionCommitStore interface {
	CommitIngestion(context.Context, []IngestionCommit) []error
}
type ingestionBatchKey struct{}
type ingestionRequest struct {
	commit IngestionCommit
	result chan error
}
type ingestionBatch struct {
	requests chan ingestionRequest
	store    IngestionCommitStore
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
}

// BeginIngestionBatch shares only ready canonical commits, so a slow provider
// never prevents ready siblings from publishing. The scope belongs to one
// activity and bounds groups at sixteen, with a two-millisecond partial flush.
func (s Service) BeginIngestionBatch(ctx context.Context) (context.Context, func()) {
	store, ok := s.Materialization.(IngestionCommitStore)
	if !ok {
		return ctx, func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	b := &ingestionBatch{requests: make(chan ingestionRequest, 16), store: store, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go b.run()
	var once sync.Once
	return context.WithValue(ctx, ingestionBatchKey{}, b), func() { once.Do(func() { cancel(); <-b.done }) }
}

func IngestionBatchActive(ctx context.Context) bool { return ctx.Value(ingestionBatchKey{}) != nil }

// EnqueueIngestion returns false for ordinary callers, preserving their
// synchronous single-write path. A successful return always follows commit.
func EnqueueIngestion(ctx context.Context, commit IngestionCommit) (bool, error) {
	b, _ := ctx.Value(ingestionBatchKey{}).(*ingestionBatch)
	if b == nil {
		return false, nil
	}
	commit.Context = ctx
	r := ingestionRequest{commit: commit, result: make(chan error, 1)}
	select {
	case b.requests <- r:
	case <-ctx.Done():
		return true, ctx.Err()
	case <-b.ctx.Done():
		return true, b.ctx.Err()
	}
	// Wait for the transaction outcome even on cancellation. The group links
	// all member contexts and rolls back before reporting a cancellation.
	select {
	case err := <-r.result:
		return true, err
	case <-b.done:
		return true, b.ctx.Err()
	}
}

func (b *ingestionBatch) run() {
	defer close(b.done)
	for {
		var first ingestionRequest
		select {
		case first = <-b.requests:
		case <-b.ctx.Done():
			return
		}
		requests := []ingestionRequest{first}
		timer := time.NewTimer(2 * time.Millisecond)
	collect:
		for len(requests) < 16 {
			select {
			case r := <-b.requests:
				requests = append(requests, r)
			case <-timer.C:
				break collect
			case <-b.ctx.Done():
				break collect
			}
		}
		timer.Stop()
		// Split organizations and repeated Record identities. Keeping the first
		// occurrence in each group preserves each Record's own publication order.
		for len(requests) > 0 {
			org := requests[0].commit.Organization
			group, remaining := []ingestionRequest{}, []ingestionRequest{}
			seen := map[string]bool{}
			for _, r := range requests {
				if r.commit.Organization != org || seen[r.commit.RecordID] {
					remaining = append(remaining, r)
					continue
				}
				seen[r.commit.RecordID] = true
				group = append(group, r)
			}
			ctx, cancel := context.WithCancel(b.ctx)
			stops := make([]func() bool, 0, len(group))
			commits := make([]IngestionCommit, len(group))
			for i, r := range group {
				commits[i] = r.commit
				stops = append(stops, context.AfterFunc(r.commit.Context, cancel))
			}
			outcomes := b.store.CommitIngestion(ctx, commits)
			for _, stop := range stops {
				stop()
			}
			cancel()
			for i, r := range group {
				r.result <- outcomes[i]
			}
			requests = remaining
		}
	}
}

// PreparePublication writes external immutable objects and constructs the
// Version from accepted work. Already published and routed-Blob receipts use
// Materialize, including normalization and quarantine handling.
func (s Service) PreparePublication(ctx context.Context, org, receiptID string) (PublicationCommit, Version, bool, error) {
	w, done, err := s.Materialization.Work(ctx, org, receiptID)
	if err != nil || done || w.Command.Content.Kind == "blob" {
		return PublicationCommit{}, Version{}, false, err
	}
	if s.Supersession != nil {
		withdrawn, superseded, err := s.Supersession.Superseded(ctx, org, w.RecordID, w.VersionID)
		if err != nil || withdrawn || superseded {
			return PublicationCommit{}, Version{}, false, err
		}
	}
	manifest := ManifestFor(w.Command)
	p, err := s.objects(ctx, org, manifest)
	if err != nil {
		return PublicationCommit{}, Version{}, false, err
	}
	v := Version{ID: w.VersionID, RecordID: w.RecordID, SourceMediaType: w.Command.SourceMediaType, Manifest: manifest, Extensions: w.Command.Extensions, Provenance: w.Command.Provenance, Availability: Availability{State: "materialized"}}
	return PublicationCommit{Work: w, Publication: p}, v, true, nil
}
