package processing

import (
	"context"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"time"
)

type preparedIndexer interface {
	PrepareIndex(context.Context, string, content.Version, content.Segmentation) (content.Generation, error)
}

func (s Service) BeginIngestionBatch(ctx context.Context) (context.Context, func()) {
	return s.Content.BeginIngestionBatch(ctx)
}

func (s Service) runPrepared(ctx context.Context, org, receiptID string) (bool, error) {
	index, ok := s.Retrieval.(preparedIndexer)
	if !ok || s.Plugin == nil || !content.IngestionBatchActive(ctx) {
		return false, nil
	}
	// Reserve time for durable ordinary materialization if preparation fails.
	budget := 20 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		budget = min(budget, time.Until(deadline)*2/3)
	}
	prepare, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	started := time.Now()
	entry, v, eligible, err := s.Content.PreparePublication(prepare, org, receiptID)
	if err != nil || !eligible {
		return false, nil
	}
	rt, err := s.route(prepare, org, v)
	if err != nil {
		return false, nil
	}
	if !rt.legacy && !rt.recipeMismatch {
		if err = s.Plugin.Serves(prepare, v, rt.generation); err != nil {
			return false, nil
		}
	}
	seg, supported, err := s.Plugin.PrepareSegments(prepare, org, entry.Work.Command.Source.CorpusID, v)
	if err != nil || !supported {
		return false, nil
	}
	g, err := index.PrepareIndex(prepare, org, v, seg)
	if err != nil {
		return false, nil
	}
	entry.Segmentation, entry.Generation = seg, g
	handled, err := content.EnqueueIngestion(ctx, content.IngestionCommit{Kind: content.CommitPublication, Organization: org, RecordID: v.RecordID, Publication: entry})
	if !handled || err != nil {
		return false, nil
	}
	s.outcome(ctx, org, "baseline", "succeeded", receiptID, v, started, "")
	if s.Observer != nil {
		s.Observer.Searchable(ctx, org, receiptID)
	}
	return true, nil
}
