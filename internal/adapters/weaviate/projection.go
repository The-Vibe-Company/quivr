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
func uuidOf(stable string) string {
	h := content.Hash([]byte(stable))
	return h[:8] + "-" + h[8:12] + "-5" + h[13:16] + "-a" + h[17:20] + "-" + h[20:32]
}

// objectID names the lexical object promotion publishes for a segment.
func objectID(org, generation, segment string) string {
	return uuidOf(content.StableID("projection", org, generation, segment))
}

// enrichedID names the object carrying one embedding payload for a segment. It
// is scoped like objectID: segments belong to one Version, and neither another
// generation nor another Version can share the identity.
func enrichedID(org, generation, segment, payloadSHA string) string {
	return uuidOf(content.StableID("projection-embedding", org, generation, segment, payloadSHA))
}

var lexicalProperties = []string{"organization", "corpusId", "generationId", "versionId", "segmentationId", "segmentId", "body", "title"}

type storedObject struct {
	Properties map[string]any       `json:"properties"`
	Vectors    map[string][]float32 `json:"vectors"`
}

// object reads one object by identity; found is false only on a definite 404.
func (s *Store) object(ctx context.Context, collection, id string) (storedObject, bool, error) {
	var stored storedObject
	status, err := s.call(ctx, "GET", "/v1/objects/"+collection+"/"+id+"?include=vector", nil, &stored)
	if status == http.StatusNotFound {
		return stored, false, nil
	}
	return stored, err == nil, err
}

func sameProperties(stored, want map[string]any) bool {
	for key, value := range want {
		if stored[key] != value {
			return false
		}
	}
	return true
}

// insert writes one object. A lost response is not an error here: the caller
// reconciles it by reading the deterministic identity.
func (s *Store) insert(ctx context.Context, object map[string]any) error {
	var result []struct {
		Result struct {
			Status string `json:"status"`
			Errors any    `json:"errors"`
		} `json:"result"`
	}
	if _, err := s.call(ctx, "POST", "/v1/batch/objects", map[string]any{"objects": []any{object}}, &result); err != nil {
		return nil
	}
	if len(result) != 1 || result[0].Result.Status != "SUCCESS" || result[0].Result.Errors != nil {
		return errors.New("projection publication failed")
	}
	return nil
}

// Publish projects each segment's lexical object. The lexical object is the
// segment's permanent keyword-search anchor: once projected it is never
// rewritten or deleted. Weaviate re-indexes an updated object under a new
// document id, and a BM25 query does not read its index atomically, so an
// update or a delete of the object serving a segment can hide it.
func (s *Store) Publish(ctx context.Context, g content.Generation, org, corpusID string, v content.Version, seg content.Segmentation) error {
	if !className.MatchString(g.Collection) {
		return errors.New("invalid projection route")
	}
	texts, _ := content.ProjectionText(v, seg, g.Fields)
	for i, p := range seg.Segments {
		id := objectID(org, g.ID, p.ID)
		properties := map[string]any{"organization": org, "corpusId": corpusID, "generationId": g.ID, "versionId": v.ID, "segmentationId": seg.ID, "segmentId": p.ID, "body": texts[i].Body, "title": texts[i].Title}
		existing, found, err := s.object(ctx, g.Collection, id)
		if err != nil {
			return err
		}
		if found && sameProperties(existing.Properties, properties) {
			continue
		}
		if err = s.insert(ctx, map[string]any{"class": g.Collection, "id": id, "properties": properties}); err != nil {
			return err
		}
		stored, found, err := s.object(ctx, g.Collection, id)
		if err != nil {
			return err
		}
		if !found || !sameProperties(stored.Properties, properties) {
			return errors.New("projection verification mismatch")
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
	query := fmt.Sprintf("{Get{%s(%s,where:%s,limit:%d){segmentId generationId}}}", collection, branch, where, retrieval.CandidateLimit)
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

// PublishEmbeddings attaches vectors without updating or deleting the segment's
// lexical anchor. For each segment it creates an enriched object beside the
// anchor, verifies it, then removes any other enriched object of the segment.
// Search sees the anchor throughout; retrieval deduplicates by segment.
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
		id := enrichedID(org, g.ID, e.SegmentID, e.Payload.SHA256)
		stored, found, err := s.object(ctx, g.Collection, id)
		if err != nil {
			return err
		}
		if !found {
			// Never create an enriched object for a segment without its lexical anchor.
			anchor, projected, err := s.object(ctx, g.Collection, objectID(org, g.ID, e.SegmentID))
			if err != nil {
				return err
			}
			if !projected {
				return retrieval.ErrProjectionMissing
			}
			lexical := map[string]any{}
			for _, key := range lexicalProperties {
				lexical[key] = anchor.Properties[key]
			}
			// Create-only: an object already written by a concurrent attachment is
			// never re-indexed. A rejected or lost create is reconciled by the read below.
			_, _ = s.call(ctx, "POST", "/v1/objects", map[string]any{"class": g.Collection, "id": id, "properties": lexical, "vectors": map[string]any{"semantic_text_v1": p.Vector}}, nil)
			if stored, found, err = s.object(ctx, g.Collection, id); err != nil {
				return err
			}
		}
		recovered, err := content.VectorBytes(stored.Vectors["semantic_text_v1"])
		if !found || err != nil || content.Hash(recovered) != e.Payload.SHA256 || stored.Properties["segmentId"] != e.SegmentID {
			return errors.New("embedding projection verification failed")
		}
		// Cleanup runs on every attempt, so a retry after an interrupted attachment converges.
		if err = s.removeStaleEnriched(ctx, g, org, e.SegmentID, id); err != nil {
			return err
		}
	}
	return nil
}

// removeStaleEnriched deletes the segment's other enriched objects in the
// generation, by filter. The lexical anchor is never deleted, so keyword search
// keeps the segment visible even while an enriched object is replaced. Segments
// belong to one Version, so no other Version's objects match.
func (s *Store) removeStaleEnriched(ctx context.Context, g content.Generation, org, segment, keep string) error {
	where := map[string]any{"operator": "And", "operands": []any{
		map[string]any{"path": []string{"organization"}, "operator": "Equal", "valueText": org},
		map[string]any{"path": []string{"generationId"}, "operator": "Equal", "valueText": g.ID},
		map[string]any{"path": []string{"segmentId"}, "operator": "Equal", "valueText": segment},
		map[string]any{"path": []string{"id"}, "operator": "NotEqual", "valueText": keep},
		map[string]any{"path": []string{"id"}, "operator": "NotEqual", "valueText": objectID(org, g.ID, segment)},
	}}
	_, err := s.deleteWhere(ctx, s.Client, g.Collection, where)
	return err
}

// purgeTimeout bounds one purge delete; a large match can exceed the default
// request timeout, and an interrupted delete is simply repeated.
const purgeTimeout = 30 * time.Second

// deleteWhere runs one batch delete by filter. Weaviate deletes at most its
// configured maximum matches per call; the result is complete only when fewer
// objects matched than that limit and none failed.
func (s *Store) deleteWhere(ctx context.Context, client *http.Client, collection string, where map[string]any) (retrieval.PurgeResult, error) {
	if !className.MatchString(collection) {
		return retrieval.PurgeResult{}, errors.New("invalid projection collection")
	}
	var response struct {
		Results struct {
			Matches    int `json:"matches"`
			Limit      int `json:"limit"`
			Successful int `json:"successful"`
			Failed     int `json:"failed"`
		} `json:"results"`
	}
	scoped := *s
	scoped.Client = client
	if _, err := scoped.call(ctx, "DELETE", "/v1/batch/objects", map[string]any{"match": map[string]any{"class": collection, "where": where}, "output": "minimal"}, &response); err != nil {
		return retrieval.PurgeResult{}, err
	}
	r := response.Results
	if r.Failed > 0 {
		return retrieval.PurgeResult{Deleted: r.Successful}, errors.New("projection delete failed")
	}
	// Fail closed: without a reported limit only an empty match proves nothing is left.
	return retrieval.PurgeResult{Deleted: r.Successful, Complete: r.Matches == 0 || (r.Limit > 0 && r.Matches < r.Limit)}, nil
}

func (s *Store) purgeClient() *http.Client {
	c := *s.Client
	c.Timeout = purgeTimeout
	return &c
}

func equalText(path, value string) map[string]any {
	return map[string]any{"path": []string{path}, "operator": "Equal", "valueText": value}
}

// PurgeGeneration deletes every object of one Corpus in one logical
// generation, lexical and enriched, by filter.
func (s *Store) PurgeGeneration(ctx context.Context, collection, org, corpusID, generationID string) (retrieval.PurgeResult, error) {
	if org == "" || corpusID == "" || generationID == "" {
		return retrieval.PurgeResult{}, errors.New("incomplete generation purge filter")
	}
	return s.deleteWhere(ctx, s.purgeClient(), collection, map[string]any{"operator": "And", "operands": []any{
		equalText("organization", org), equalText("corpusId", corpusID), equalText("generationId", generationID)}})
}

// PurgeVersion deletes every object of one Record Version, lexical and
// enriched, in every generation of the collection, by filter.
func (s *Store) PurgeVersion(ctx context.Context, collection, org, versionID string) (retrieval.PurgeResult, error) {
	if org == "" || versionID == "" {
		return retrieval.PurgeResult{}, errors.New("incomplete version purge filter")
	}
	return s.deleteWhere(ctx, s.purgeClient(), collection, map[string]any{"operator": "And", "operands": []any{
		equalText("organization", org), equalText("versionId", versionID)}})
}

var _ retrieval.PurgeProjection = (*Store)(nil)
