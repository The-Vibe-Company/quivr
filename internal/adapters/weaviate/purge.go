package weaviate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

// A generation purge admits one small window per sweep, independently of the
// server-wide QUERY_MAXIMUM_RESULTS. No offset is needed: deleted objects no
// longer appear in the next window, including after a lost response/restart.
const generationPurgeBatch = 256

func (s *Store) purgeGenerationWindow(ctx context.Context, collection, org, corpusID, generationID string) (retrieval.PurgeResult, error) {
	if !className.MatchString(collection) {
		return retrieval.PurgeResult{}, errors.New("invalid projection collection")
	}
	ctx, cancel := context.WithTimeout(ctx, purgeTimeout)
	defer cancel()
	where := "{operator:And,operands:[" + equal("organization", org) + "," + equal("corpusId", corpusID) + "," + equal("generationId", generationID) + "]}"
	query := fmt.Sprintf("{Get{%s(where:%s,limit:%d){_additional{id}}}}", collection, where, generationPurgeBatch+1)
	var response struct {
		Data struct {
			Get map[string][]struct {
				Additional struct {
					ID string `json:"id"`
				} `json:"_additional"`
			} `json:"Get"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := s.purgeCall(ctx, "POST", "/v1/graphql", map[string]any{"query": query}, &response); err != nil {
		return retrieval.PurgeResult{}, err
	}
	if len(response.Errors) > 0 {
		return retrieval.PurgeResult{}, fmt.Errorf("projection purge listing: %.1024s", response.Errors[0].Message)
	}
	rows, ok := response.Data.Get[collection]
	if !ok {
		return retrieval.PurgeResult{}, errors.New("projection purge listing missing")
	}
	if len(rows) == 0 {
		return retrieval.PurgeResult{Complete: true}, nil
	}
	ids := make([]any, 0, min(len(rows), generationPurgeBatch))
	for _, row := range rows[:min(len(rows), generationPurgeBatch)] {
		if row.Additional.ID == "" {
			return retrieval.PurgeResult{}, errors.New("projection purge listing has no object identity")
		}
		ids = append(ids, equalText("id", row.Additional.ID))
	}
	// Retain every ownership fence when restricting the delete to this window.
	filter := map[string]any{"operator": "And", "operands": []any{
		equalText("organization", org), equalText("corpusId", corpusID), equalText("generationId", generationID),
		map[string]any{"operator": "Or", "operands": ids},
	}}
	result, err := s.deleteWhere(ctx, s.purgeClient(), collection, filter)
	// A failed or lost reply may still have deleted every attempted object.
	// Only rows outside the attempted window establish a conservative bound.
	result.RemainingAtLeast = max(0, len(rows)-len(ids))
	result.Complete = result.Complete && len(rows) <= generationPurgeBatch
	return result, err
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
