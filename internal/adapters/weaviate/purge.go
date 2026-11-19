package weaviate

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/google/uuid"
	wv "github.com/weaviate/weaviate-go-client/v6"
	"github.com/weaviate/weaviate-go-client/v6/data"
	"github.com/weaviate/weaviate-go-client/v6/query"
	"github.com/weaviate/weaviate-go-client/v6/query/filter"

	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

// Deletions keep a small adaptive window even when the server admits more IDs.
const (
	generationPurgeBatch = 256
	generationPurgeFloor = 16
)

func (s *Store) purgeRequestTimeout() time.Duration {
	if s.PurgeTimeout <= 0 {
		return retrieval.DefaultPurgeTimeout
	}
	return min(s.PurgeTimeout, retrieval.MaximumPurgeTimeout)
}

// deleteSelected deletes the objects matching where, or only counts them on a
// dry run. matches is the number the index selected, which it caps per
// request; failures lists the selected objects it could not delete. Provider
// explanations are kept, with the API key removed, for the purge log.
func (s *Store) deleteSelected(ctx context.Context, collection string, where filter.Expr, dryRun bool) (matches int, failures map[uuid.UUID]error, err error) {
	operation := "delete"
	if dryRun {
		operation = "selection"
	}
	err = s.request(ctx, s.purgeRequestTimeout(), func(ctx context.Context, c *wv.Client) error {
		result, err := c.Collections.Use(collection).Data.DeleteSelected(ctx, data.DeleteSelected{Filter: where, DryRun: dryRun, Verbose: true})
		var objectErrors data.DeleteError
		if errors.As(err, &objectErrors) {
			failures, err = objectErrors.Errors, nil
		}
		if err != nil {
			return err
		}
		if result == nil || result.Matches < 0 || int(result.Matches) < len(failures) {
			return errors.New("result missing or invalid")
		}
		matches = int(result.Matches)
		return nil
	})
	if err != nil {
		return 0, nil, fmt.Errorf("projection purge %s: %w", operation, err)
	}
	return matches, failures, nil
}

// deleteFailure reports failed objects with the first provider explanation.
func (s *Store) deleteFailure(deleted int, failures map[uuid.UUID]error) error {
	reason := "provider reported failed objects"
	for _, failure := range failures {
		if failure != nil {
			reason = failure.Error()
			break
		}
	}
	return fmt.Errorf("projection delete: %d failed, %d successful: %.1024s", len(failures), deleted, s.redact(reason))
}

func (s *Store) purgeGenerationWindow(ctx context.Context, collection, org, corpusID, generationID string) (result retrieval.PurgeResult, err error) {
	if !className.MatchString(collection) {
		return result, errors.New("invalid projection collection")
	}
	batch := int(s.purgeBatch.Load())
	if batch == 0 {
		s.purgeBatch.CompareAndSwap(0, generationPurgeBatch)
		batch = int(s.purgeBatch.Load())
	}
	// A cancelled caller cannot teach us about provider capacity. Request timeouts
	// can; keep the learning between sweeps without retaining per-generation keys.
	caller := ctx
	defer func() {
		var timeout net.Error
		if caller.Err() == nil && errors.As(err, &timeout) && timeout.Timeout() {
			s.purgeBatch.CompareAndSwap(int64(batch), int64(max(generationPurgeFloor, batch/2)))
		} else if err == nil && result.Deleted == batch {
			s.purgeBatch.CompareAndSwap(int64(batch), int64(min(generationPurgeBatch, batch+16)))
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 3*s.purgeRequestTimeout()+15*time.Second)
	defer cancel()
	ownership := []filter.Expr{equal("organization", org), equal("corpusId", corpusID), equal("generationId", generationID)}
	// A zero-match dry run proves completion, including after a lost reply.
	matches, _, err := s.deleteSelected(ctx, collection, and(ownership...), true)
	if err != nil {
		return result, err
	}
	if matches == 0 {
		return retrieval.PurgeResult{Complete: true}, nil
	}
	var listed *query.Result
	if err = s.request(ctx, s.purgeRequestTimeout(), func(ctx context.Context, c *wv.Client) (err error) {
		listed, err = c.Collections.Use(collection).Query.OverAll(ctx, query.OverAll{Filter: and(ownership...), Limit: batch, ReturnProperties: []string{}})
		return err
	}); err != nil {
		return result, fmt.Errorf("projection purge selection: %w", err)
	}
	if listed == nil {
		return result, errors.New("projection purge selection missing or invalid")
	}
	result.RemainingAtLeast = matches
	// The client returns no slice for an empty reply, so an empty window
	// cannot be told from a missing one. Either way nothing is deleted and
	// the purge stays incomplete until a later dry run matches nothing.
	if len(listed.Objects) == 0 {
		return result, nil
	}
	ids := make([]filter.Expr, len(listed.Objects))
	for i, object := range listed.Objects {
		ids[i] = identity(object.UUID)
	}
	// Keep every ownership fence even though selection returned object identities.
	deleted, failures, err := s.deleteSelected(ctx, collection, and(append(append([]filter.Expr{}, ownership...), or(ids...))...), false)
	// Unknown outcomes may have deleted every attempted slot. Only IDs outside the
	// window establish a remaining lower bound; a capped match count is not a total.
	result.RemainingAtLeast = max(0, matches-len(ids))
	if err != nil {
		return result, err
	}
	result.Deleted = deleted - len(failures)
	if len(failures) > 0 {
		return result, s.deleteFailure(result.Deleted, failures)
	}
	return result, nil
}

// PurgeGeneration deletes every object of one Corpus in one logical
// generation, lexical and enriched, by filter.
func (s *Store) PurgeGeneration(ctx context.Context, collection, org, corpusID, generationID string) (retrieval.PurgeResult, error) {
	if org == "" || corpusID == "" || generationID == "" {
		return retrieval.PurgeResult{}, errors.New("incomplete generation purge filter")
	}
	return s.purgeGenerationWindow(ctx, collection, org, corpusID, generationID)
}

// PurgeVersion deletes every object of one Record Version, lexical and
// enriched, in every generation of the collection, by filter. The index
// deletes at most its configured maximum per request and does not report that
// cap, so only a following zero-match count proves completion.
func (s *Store) PurgeVersion(ctx context.Context, collection, org, versionID string) (retrieval.PurgeResult, error) {
	if org == "" || versionID == "" {
		return retrieval.PurgeResult{}, errors.New("incomplete version purge filter")
	}
	if !className.MatchString(collection) {
		return retrieval.PurgeResult{}, errors.New("invalid projection collection")
	}
	where := and(equal("organization", org), equal("versionId", versionID))
	matches, failures, err := s.deleteSelected(ctx, collection, where, false)
	if err != nil {
		return retrieval.PurgeResult{}, err
	}
	result := retrieval.PurgeResult{Deleted: matches - len(failures)}
	if len(failures) > 0 {
		return result, s.deleteFailure(result.Deleted, failures)
	}
	if matches == 0 {
		result.Complete = true
		return result, nil
	}
	remaining, _, err := s.deleteSelected(ctx, collection, where, true)
	if err != nil {
		// The deletions happened; a later sweep proves completion.
		return result, nil
	}
	result.Complete, result.RemainingAtLeast = remaining == 0, remaining
	return result, nil
}
