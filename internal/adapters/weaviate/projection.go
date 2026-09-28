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

// InitialCollection is shared by every logical Projection Generation; objects carry
// their generation so PostgreSQL routing, not aliases, selects what a Corpus serves.
const InitialCollection = "QuivrTextV4"

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
func (s *Store) Bootstrap(ctx context.Context, collection string) error {
	if !className.MatchString(collection) {
		return errors.New("invalid projection route")
	}
	status, err := s.call(ctx, "GET", "/v1/schema/"+collection, nil, nil)
	if err == nil {
		return nil
	}
	if status != 404 {
		return err
	}
	properties := []any{}
	for _, name := range []string{"organization", "corpusId", "generationId", "segmentId", "versionId", "segmentationId"} {
		properties = append(properties, map[string]any{"name": name, "dataType": []string{"text"}, "tokenization": "field", "indexFilterable": true, "indexSearchable": false})
	}
	for _, name := range []string{"title", "body"} {
		properties = append(properties, map[string]any{"name": name, "dataType": []string{"text"}, "tokenization": "word", "indexSearchable": true})
	}
	schema := map[string]any{"class": collection, "properties": properties, "vectorConfig": map[string]any{"semantic_text_v1": map[string]any{"vectorizer": map[string]any{"none": nil}, "vectorIndexType": "hnsw", "vectorIndexConfig": map[string]any{"distance": "cosine"}}}, "invertedIndexConfig": map[string]any{"stopwords": map[string]any{"preset": "none"}}, "replicationConfig": map[string]any{"factor": 1}}
	_, err = s.call(ctx, "POST", "/v1/schema", schema, nil)
	return err
}
func objectID(org, generation, segment string) string {
	h := content.Hash([]byte(content.StableID("projection", org, generation, segment)))
	return h[:8] + "-" + h[8:12] + "-5" + h[13:16] + "-a" + h[17:20] + "-" + h[20:32]
}
func (s *Store) Publish(ctx context.Context, g content.Generation, org, corpusID string, v content.Version, seg content.Segmentation) error {
	if !className.MatchString(g.Collection) {
		return errors.New("invalid projection route")
	}
	texts, _ := content.ProjectionText(v, seg, g.Fields)
	for i, p := range seg.Segments {
		id := objectID(org, g.ID, p.ID)
		properties := map[string]any{"organization": org, "corpusId": corpusID, "generationId": g.ID, "versionId": v.ID, "segmentationId": seg.ID, "segmentId": p.ID, "body": texts[i].Body, "title": texts[i].Title}
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

// Search queries every routed (Corpus, generation) pair in one request so hybrid
// fusion sees a single candidate set. Routes must share one physical collection.
func (s *Store) Search(ctx context.Context, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
	if len(routes) == 0 {
		return nil, errors.New("projection route missing")
	}
	collection := routes[0].Generation.Collection
	filters := []string{}
	for _, r := range routes {
		if r.Generation.Collection != collection || !className.MatchString(collection) || r.Generation.ID == "" {
			return nil, errors.New("invalid projection route")
		}
		filters = append(filters, "{operator:And,operands:["+equal("corpusId", r.CorpusID)+","+equal("generationId", r.Generation.ID)+"]}")
	}
	where := "{operator:And,operands:[" + equal("organization", scope.Organization) + ",{operator:Or,operands:[" + strings.Join(filters, ",") + "]}]}"
	branch := fmt.Sprintf("bm25:{query:%s,properties:[\"title^2\",\"body\"]}", quote(q.Query))
	vector, _ := json.Marshal(q.Vector)
	if q.Mode == "semantic" {
		branch = fmt.Sprintf("nearVector:{vector:%s,targetVectors:[\"semantic_text_v1\"]}", vector)
	}
	if q.Mode == "hybrid" {
		branch = fmt.Sprintf("hybrid:{query:%s,vector:%s,alpha:0.5,fusionType:relativeScoreFusion,properties:[\"title^2\",\"body\"],targetVectors:[\"semantic_text_v1\"]}", quote(q.Query), vector)
	}
	query := fmt.Sprintf("{Get{%s(%s,where:%s,limit:%d){segmentId generationId}}}", collection, branch, where, 100)
	var response struct {
		Data struct {
			Get map[string][]struct {
				SegmentID    string `json:"segmentId"`
				GenerationID string `json:"generationId"`
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
	rows, ok := response.Data.Get[collection]
	if !ok {
		return nil, errors.New("projection response missing")
	}
	result := make([]content.Candidate, 0, len(rows))
	for _, r := range rows {
		if r.SegmentID == "" || r.GenerationID == "" {
			return nil, errors.New("projection candidate invalid")
		}
		result = append(result, content.Candidate{SegmentID: r.SegmentID, GenerationID: r.GenerationID})
	}
	return result, nil
}

func (s *Store) PublishEmbeddings(ctx context.Context, g content.Generation, org string, data []content.EmbeddingData) error {
	if !className.MatchString(g.Collection) {
		return errors.New("invalid embedding projection")
	}
	for _, p := range data {
		e := p.Artifact
		if e.Organization != org || e.SpaceID != g.SpaceID {
			return errors.New("incompatible embedding projection")
		}
		raw, err := content.VectorBytes(p.Vector)
		if err != nil || content.Hash(raw) != e.Payload.SHA256 {
			return errors.New("embedding payload mismatch")
		}
		path := "/v1/objects/" + g.Collection + "/" + objectID(org, g.ID, e.SegmentID)
		// Merge preserves lexical fields and cannot create a vector-only object.
		_, _ = s.call(ctx, "PATCH", path, map[string]any{"class": g.Collection, "vectors": map[string]any{"semantic_text_v1": p.Vector}}, nil)
		var stored struct {
			Vectors    map[string][]float32 `json:"vectors"`
			Properties map[string]any       `json:"properties"`
		}
		if _, err = s.call(ctx, "GET", path+"?include=vector", nil, &stored); err != nil {
			return err
		}
		recovered, err := content.VectorBytes(stored.Vectors["semantic_text_v1"])
		if err != nil || content.Hash(recovered) != e.Payload.SHA256 || stored.Properties["segmentId"] != e.SegmentID {
			return errors.New("embedding projection verification failed")
		}
	}
	return nil
}
