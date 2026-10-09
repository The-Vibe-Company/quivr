package processing

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
)

// IngestionPageSweeper repairs completed pages left by crashes or late writers.
// Canonical reads run outside database locks; retirement fences the snapshot.
type IngestionPageSweeper struct {
	Store   content.IngestionPageSweepStore
	Content content.Service
	Batch   int
}

func (s IngestionPageSweeper) Run(ctx context.Context) {
	for ctx.Err() == nil {
		work, ok := lifecycle.Admit(ctx)
		if !ok {
			return
		}
		if _, err := s.Sweep(work); err != nil && ctx.Err() == nil {
			slog.Warn("ingestion page sweep failed; retrying next interval", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

// Sweep scans at most Batch pages and deletes at most Batch pages. Inputs
// lacking exact source/space evidence retain their checkpoints for retries.
func (s IngestionPageSweeper) Sweep(ctx context.Context) (int, error) {
	batch := s.Batch
	if batch <= 0 {
		batch = 100
	}
	namespaces, err := s.Store.IngestionPageCandidates(ctx, batch)
	if err != nil {
		return 0, err
	}
	deleted := 0
	var failure error
	for _, n := range namespaces {
		segmentation := ""
		if !n.Settled {
			v, err := s.Content.TrustedVersion(ctx, n.Organization, n.CorpusID, n.RecordID, n.VersionID)
			if err != nil {
				if failure == nil {
					failure = err
				}
				continue
			}
			key, err := content.IngestionPageInputKey(v, n.Spaces)
			if err != nil {
				return deleted, err
			}
			// Older segments-only callers hashed either nil or an empty
			// requested-space slice. Both describe the same no-vector input.
			if key != n.InputKey && len(n.Spaces) == 0 {
				key, err = content.IngestionPageInputKey(v, nil)
				if err != nil {
					return deleted, err
				}
			}
			if key != n.InputKey {
				continue
			}
			seg, err := s.Content.PluginSegmentationOf(ctx, n.Organization, v, n.Recipe)
			if errors.Is(err, corpus.ErrNotFound) || errors.Is(err, content.ErrConflict) {
				continue
			}
			if err != nil {
				if failure == nil {
					failure = err
				}
				continue
			}
			segmentation = seg.ID
		}
		count, err := s.Store.RetireIngestionPages(ctx, n, segmentation, batch-deleted)
		deleted += count
		if err != nil && failure == nil {
			failure = err
		}
		if deleted == batch {
			break
		}
	}
	return deleted, failure
}
