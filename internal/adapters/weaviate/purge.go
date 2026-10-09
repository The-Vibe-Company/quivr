package weaviate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

// Deletions keep a small adaptive window even when the server admits more IDs.
const (
	generationPurgeBatch  = 256
	generationPurgeFloor  = 16
	maximumPurgeSelection = 10000
)

type purgeResponse struct {
	DryRun  *bool `json:"dryRun"`
	Results *struct {
		Matches    *int `json:"matches"`
		Limit      int  `json:"limit"`
		Successful *int `json:"successful"`
		Failed     *int `json:"failed"`
		Objects    []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Errors *struct {
				Error []struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"errors"`
		} `json:"objects"`
	} `json:"results"`
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
	ctx, cancel := context.WithTimeout(ctx, 2*s.purgeRequestTimeout()+15*time.Second)
	defer cancel()
	ownership := []any{equalText("organization", org), equalText("corpusId", corpusID), equalText("generationId", generationID)}
	where := map[string]any{"operator": "And", "operands": ownership}
	if s.purgeLimit.Load() == 0 {
		// Read the global cap using at most one UUID, before resolving a generation.
		probe := map[string]any{"operator": "And", "operands": append(append([]any{}, ownership...), equalText("id", "00000000-0000-0000-0000-000000000000"))}
		var capReply purgeResponse
		capReply, err = s.selectPurgeIDs(ctx, collection, probe)
		if err != nil {
			return result, err
		}
		s.purgeLimit.Store(int64(capReply.Results.Limit))
	}
	selection, err := s.selectPurgeIDs(ctx, collection, where)
	if err != nil {
		return result, err
	}
	rows := selection.Results
	if *rows.Matches == 0 {
		return retrieval.PurgeResult{Complete: true}, nil
	}
	ids := make([]any, 0, min(len(rows.Objects), batch))
	for _, row := range rows.Objects[:min(len(rows.Objects), batch)] {
		ids = append(ids, equalText("id", row.ID))
	}
	// Keep every ownership fence even though selection returned object identities.
	filter := map[string]any{"operator": "And", "operands": append(append([]any{}, ownership...), map[string]any{"operator": "Or", "operands": ids})}
	result, err = s.deleteWhere(ctx, s.purgeClient(), collection, filter)
	// Unknown outcomes may have deleted every attempted slot. Only IDs outside the
	// window establish a remaining lower bound; a capped match count is not a total.
	result.RemainingAtLeast = max(0, *rows.Matches-len(ids))
	// A later zero-match dry run proves completion, including after a lost reply.
	result.Complete = false
	return result, err
}

func (s *Store) selectPurgeIDs(ctx context.Context, collection string, where map[string]any) (purgeResponse, error) {
	var response purgeResponse
	scoped := Store{Endpoint: s.Endpoint, Client: s.purgeClient()}
	if err := scoped.purgeCall(ctx, "DELETE", "/v1/batch/objects", map[string]any{"match": map[string]any{"class": collection, "where": where}, "dryRun": true, "output": "verbose"}, &response); err != nil {
		return response, err
	}
	r := response.Results
	if response.DryRun == nil || !*response.DryRun || r == nil || r.Matches == nil || r.Failed == nil || *r.Failed != 0 || r.Successful == nil || *r.Successful < 0 || *r.Matches < 0 || *r.Successful > *r.Matches {
		return response, errors.New("projection purge selection missing or invalid")
	}
	// The server resolves at most limit+1 per shard, then clamps globally. Reject
	// disabled or unusually large caps; shard count still multiplies selection IO.
	if r.Limit <= 0 || r.Limit > maximumPurgeSelection {
		s.purgeLimit.Store(0)
		return response, errors.New("projection purge selection requires QUERY_MAXIMUM_RESULTS between 1 and 10000")
	}
	if *r.Matches > r.Limit+1 || len(r.Objects) != min(*r.Matches, r.Limit) {
		return response, errors.New("projection purge selection identities missing or invalid")
	}
	seen := make(map[string]bool, len(r.Objects))
	for _, object := range r.Objects {
		if object.ID == "" || seen[object.ID] || object.Status != "DRYRUN" || object.Errors != nil {
			return response, errors.New("projection purge selection identity missing or invalid")
		}
		seen[object.ID] = true
	}
	return response, nil
}

func (s *Store) purgeRequestTimeout() time.Duration {
	if s.PurgeTimeout <= 0 {
		return retrieval.DefaultPurgeTimeout
	}
	return min(s.PurgeTimeout, retrieval.MaximumPurgeTimeout)
}

// purgeCall retains bounded provider explanations. The purge service logs them
// as strings through the normal secret/URL sanitizer, not as opaque errors.
func (s *Store) purgeCall(ctx context.Context, method, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, s.Endpoint+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := s.Client.Do(req)
	if err != nil {
		return fmt.Errorf("projection purge %s: %w", method, err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var response struct {
			Error []struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&response) == nil && len(response.Error) > 0 {
			return fmt.Errorf("projection purge HTTP %d: %.1024s", res.StatusCode, strings.TrimSpace(response.Error[0].Message))
		}
		return fmt.Errorf("projection purge HTTP %d (%s)", res.StatusCode, http.StatusText(res.StatusCode))
	}
	if out != nil {
		if err = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(out); err != nil {
			return fmt.Errorf("projection purge response: %w", err)
		}
	}
	return nil
}
