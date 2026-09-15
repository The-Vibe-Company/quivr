// Package weaviate adapts the pinned local engine; its physical schema stays private.
package weaviate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
)

const InitialCollection = "QuivrTextV1"

var className = regexp.MustCompile(`^[A-Z][A-Za-z0-9_]*$`)

type Store struct {
	Endpoint string
	Client   *http.Client
}

func New(endpoint string) *Store {
	return &Store{Endpoint: strings.TrimRight(endpoint, "/"), Client: &http.Client{Timeout: 4 * time.Second}}
}
func (s *Store) call(ctx context.Context, method, path string, in, out any) (int, error) {
	var body []byte
	var err error
	if in != nil {
		body, err = json.Marshal(in)
		if err != nil {
			return 0, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, s.Endpoint+path, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := s.Client.Do(req)
	if err != nil {
		return 0, errors.New("projection connection unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return res.StatusCode, errors.New("projection request failed")
	}
	if out != nil {
		err = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(out)
	}
	return res.StatusCode, err
}
func (s *Store) Ready(ctx context.Context) error {
	_, err := s.call(ctx, "GET", "/v1/.well-known/ready", nil, nil)
	return err
}
func (s *Store) Bootstrap(ctx context.Context) error {
	status, err := s.call(ctx, "GET", "/v1/schema/"+InitialCollection, nil, nil)
	if err == nil {
		return nil
	}
	if status != 404 {
		return err
	}
	properties := []any{}
	for _, name := range []string{"organization", "corpusId", "segmentId", "versionId", "segmentationId"} {
		properties = append(properties, map[string]any{"name": name, "dataType": []string{"text"}, "tokenization": "field", "indexFilterable": true, "indexSearchable": false})
	}
	for _, name := range []string{"title", "body"} {
		properties = append(properties, map[string]any{"name": name, "dataType": []string{"text"}, "tokenization": "word", "indexSearchable": true})
	}
	schema := map[string]any{"class": InitialCollection, "properties": properties, "vectorizer": "none", "vectorIndexConfig": map[string]any{"skip": true}, "invertedIndexConfig": map[string]any{"stopwords": map[string]any{"preset": "none"}}, "replicationConfig": map[string]any{"factor": 1}}
	_, err = s.call(ctx, "POST", "/v1/schema", schema, nil)
	return err
}
func objectID(org, segment string) string {
	h := content.Hash([]byte(content.StableID("projection", org, segment)))
	return h[:8] + "-" + h[8:12] + "-5" + h[13:16] + "-a" + h[17:20] + "-" + h[20:32]
}
func (s *Store) Publish(ctx context.Context, g content.Generation, org, corpusID string, v content.Version, seg content.Segmentation) error {
	if !className.MatchString(g.Collection) {
		return errors.New("invalid projection route")
	}
	for _, p := range seg.Segments {
		id := objectID(org, p.ID)
		properties := map[string]any{"organization": org, "corpusId": corpusID, "versionId": v.ID, "segmentationId": seg.ID, "segmentId": p.ID, "body": p.Text, "title": ""}
		object := map[string]any{"class": g.Collection, "id": id, "properties": properties}
		var result []struct {
			Result struct {
				Status string `json:"status"`
				Errors any    `json:"errors"`
			} `json:"result"`
		}
		_, putErr := s.call(ctx, "POST", "/v1/batch/objects", map[string]any{"objects": []any{object}}, &result)
		// A lost response is reconciled by reading the deterministic object identity.
		var stored struct {
			Properties map[string]any `json:"properties"`
		}
		_, err := s.call(ctx, "GET", "/v1/objects/"+g.Collection+"/"+id, nil, &stored)
		if err != nil {
			return err
		}
		for key, value := range properties {
			if stored.Properties[key] != value {
				return errors.New("projection verification mismatch")
			}
		}
		if putErr == nil && (len(result) != 1 || result[0].Result.Status != "SUCCESS" || result[0].Result.Errors != nil) {
			return errors.New("projection publication failed")
		}
	}
	return nil
}
func quote(v string) string { b, _ := json.Marshal(v); return string(b) }
func equal(field, value string) string {
	return "{path:[" + quote(field) + "],operator:Equal,valueText:" + quote(value) + "}"
}
func (s *Store) Search(ctx context.Context, g content.Generation, scope corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
	if !className.MatchString(g.Collection) {
		return nil, errors.New("invalid projection route")
	}
	filters := []string{}
	for _, id := range q.CorpusIDs {
		filters = append(filters, equal("corpusId", id))
	}
	where := "{operator:And,operands:[" + equal("organization", scope.Organization) + ",{operator:Or,operands:[" + strings.Join(filters, ",") + "]}]}"
	query := fmt.Sprintf("{Get{%s(bm25:{query:%s,properties:[\"title^2\",\"body\"]},where:%s,limit:%d){segmentId}}}", g.Collection, quote(q.Query), where, 100)
	var response struct {
		Data struct {
			Get map[string][]struct {
				SegmentID string `json:"segmentId"`
			} `json:"Get"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	_, err := s.call(ctx, "POST", "/v1/graphql", map[string]any{"query": query}, &response)
	if err != nil {
		return nil, err
	}
	if len(response.Errors) > 0 {
		return nil, errors.New("projection query failed")
	}
	rows, ok := response.Data.Get[g.Collection]
	if !ok {
		return nil, errors.New("projection response missing")
	}
	result := make([]content.Candidate, 0, len(rows))
	for _, r := range rows {
		if r.SegmentID == "" {
			return nil, errors.New("projection candidate invalid")
		}
		result = append(result, content.Candidate{SegmentID: r.SegmentID, GenerationID: g.ID})
	}
	return result, nil
}
