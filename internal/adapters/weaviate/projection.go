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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
)

// InitialCollection is shared by every logical Projection Generation; objects carry
// their generation so PostgreSQL routing, not aliases, selects what a Corpus serves.
const InitialCollection = "QuivrTextV4"

var className = regexp.MustCompile(`^[A-Z][A-Za-z0-9_]*$`)

type Store struct {
	Endpoint string
	Client   *http.Client
	// LegacySpace is the built-in space, whose named vector keeps the name it
	// had before named spaces (legacyVector), so generations built before and
	// after them stay searchable in one query.
	LegacySpace string
	// vectors remembers the named vectors known to exist, by collection.
	vectors            *sync.Map
	metadataMu         sync.Mutex
	metadataProperties map[string]bool
}

// legacyVector is the named vector of the built-in space and of every
// generation built before named spaces.
const legacyVector = "semantic_text_v1"

// lexicalProperty holds an ingestion plugin's keyword-search text.
const lexicalProperty = "lexicalText"

// VectorName is the named vector a generation stores a space's vectors
// under: legacyVector for the built-in space and for a generation built
// before named spaces, otherwise a name derived from the space key.
func (s *Store) VectorName(g content.Generation, space string) string {
	if !g.SpacesProjected || space == s.LegacySpace {
		return legacyVector
	}
	return "s_" + content.Hash([]byte("quivr/named-vector/v1\x00" + space))[:24]
}

// distance maps a registry metric to the Weaviate distance.
func distance(metric string) string {
	switch metric {
	case "dot":
		return "dot"
	case "l2":
		return "l2-squared"
	}
	return "cosine"
}

func vectorConfig(metric string) map[string]any {
	return map[string]any{"vectorizer": map[string]any{"none": nil}, "vectorIndexType": "hnsw", "vectorIndexConfig": map[string]any{"distance": distance(metric)}}
}

// ensureVectors adds the named vectors of a generation's spaces that its
// collection lacks. Weaviate adds a named vector to an existing collection
// in place (since 1.31), so a new space never recreates the collection.
func (s *Store) ensureVectors(ctx context.Context, g content.Generation) error {
	if !g.SpacesProjected {
		return nil
	}
	known := s.vectors
	if known == nil {
		known = &sync.Map{}
	}
	missing := false
	for _, sp := range g.Spaces {
		if _, ok := known.Load(g.Collection + "/" + s.VectorName(g, sp.ID)); !ok {
			missing = true
		}
	}
	if !missing {
		return nil
	}
	var class map[string]any
	if _, err := s.call(ctx, "GET", "/v1/schema/"+g.Collection, nil, &class); err != nil {
		return err
	}
	configs, _ := class["vectorConfig"].(map[string]any)
	if configs == nil {
		configs = map[string]any{}
	}
	added := false
	for _, sp := range g.Spaces {
		name := s.VectorName(g, sp.ID)
		if _, ok := configs[name]; !ok {
			configs[name] = vectorConfig(sp.Metric)
			added = true
		}
	}
	if added {
		class["vectorConfig"] = configs
		// A concurrent writer may have added it first; the read below decides.
		_, _ = s.call(ctx, "PUT", "/v1/schema/"+g.Collection, class, nil)
		class = nil
		if _, err := s.call(ctx, "GET", "/v1/schema/"+g.Collection, nil, &class); err != nil {
			return err
		}
		configs, _ = class["vectorConfig"].(map[string]any)
	}
	for _, sp := range g.Spaces {
		name := s.VectorName(g, sp.ID)
		if _, ok := configs[name]; !ok {
			return errors.New("projection named vector missing")
		}
		known.Store(g.Collection+"/"+name, true)
	}
	return nil
}

func New(endpoint string) *Store {
	return &Store{Endpoint: strings.TrimRight(endpoint, "/"), Client: &http.Client{Timeout: 4 * time.Second, Transport: telemetry.Transport(nil, "weaviate.request")}, vectors: &sync.Map{}}
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
	var existing struct {
		Properties []struct {
			Name string `json:"name"`
		} `json:"properties"`
	}
	status, err := s.call(ctx, "GET", "/v1/schema/"+collection, nil, &existing)
	if err == nil {
		present := map[string]bool{}
		for _, p := range existing.Properties {
			present[p.Name] = true
		}
		// A collection created before source routing, named spaces, or lexical
		// plugin metadata gains the missing properties. Older objects lack a
		// value, so their immutable anchors keep their original bytes.
		properties := []struct {
			name   string
			config map[string]any
		}{
			{projectionPluginProperty, filterable(projectionPluginProperty)},
			{sourceMediaTypeProperty, filterable(sourceMediaTypeProperty)},
			{sourceNamespaceProperty, filterable(sourceNamespaceProperty)},
			{lexicalProperty, searchable(lexicalProperty)},
		}
		for _, property := range properties {
			if present[property.name] {
				continue
			}
			if _, err = s.call(ctx, "POST", "/v1/schema/"+collection+"/properties", property.config, nil); err != nil {
				return err
			}
		}
		return nil
	}
	if status != 404 {
		return err
	}
	properties := []any{}
	for _, name := range []string{"organization", "corpusId", "generationId", "segmentId", "versionId", "segmentationId", projectionPluginProperty, sourceMediaTypeProperty, sourceNamespaceProperty} {
		properties = append(properties, filterable(name))
	}
	for _, name := range []string{"title", "body", lexicalProperty} {
		properties = append(properties, searchable(name))
	}
	schema := map[string]any{"class": collection, "properties": properties, "vectorConfig": map[string]any{legacyVector: vectorConfig("cosine")}, "invertedIndexConfig": map[string]any{"stopwords": map[string]any{"preset": "none"}}, "replicationConfig": map[string]any{"factor": 1}}
	_, err = s.call(ctx, "POST", "/v1/schema", schema, nil)
	return err
}

// sourceNamespaceProperty holds the Record's Source Namespace for filtering.
const sourceNamespaceProperty = "sourceNamespace"

// projectionPluginProperty identifies the ingestion plugin that made a
// plugin segmentation. Empty for legacy anchors projected before ownership
// was recorded.
const projectionPluginProperty = "projectionPlugin"

// sourceMediaTypeProperty records the source Blob media type used to route a
// plugin segmentation. Plugin recipes with no recorded type use text/plain.
const sourceMediaTypeProperty = "sourceMediaType"

// searchable declares a text property keyword search scores (BM25).
func searchable(name string) map[string]any {
	return map[string]any{"name": name, "dataType": []string{"text"}, "tokenization": "word", "indexSearchable": true}
}

// filterable declares an exact-match text property that search never scores.
func filterable(name string) map[string]any {
	return map[string]any{"name": name, "dataType": []string{"text"}, "tokenization": "field", "indexFilterable": true, "indexSearchable": false}
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

var lexicalProperties = []string{"organization", "corpusId", "generationId", "versionId", "segmentationId", "segmentId", projectionPluginProperty, sourceMediaTypeProperty, sourceNamespaceProperty, "body", "title", lexicalProperty}

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
		if !sameMetadata(stored[key], value) {
			if strings.HasPrefix(key, "m_") {
				a, aok := stored[key].(string)
				b, bok := value.(string)
				if aok && bok {
					na, aok := corpus.FilterDate(a)
					nb, bok := corpus.FilterDate(b)
					if aok && bok && na == nb {
						continue
					}
				}
			}
			return false
		}
	}
	return true
}

// projectionBatch bounds request size and the number of unverified objects.
const projectionBatch = 100

// insertBatch checks every object result. A lost or rejected HTTP response is
// reconciled by the caller reading deterministic identities; an explicit object
// error or malformed successful response must never be treated as success.
func (s *Store) insertBatch(ctx context.Context, objects []map[string]any) error {
	var result []struct {
		ID     string `json:"id"`
		Result struct {
			Status string `json:"status"`
			Errors any    `json:"errors"`
		} `json:"result"`
	}
	// Request only identities; returning properties/vectors can exceed the
	// bounded response decoder even for a modest number of large objects.
	status, err := s.call(ctx, "POST", "/v1/batch/objects", map[string]any{"objects": objects, "fields": []string{"id"}}, &result)
	if err != nil {
		if status == 0 || status >= 400 {
			return nil
		}
		return errors.New("projection publication response invalid")
	}
	if len(result) != len(objects) {
		return errors.New("projection publication failed")
	}
	for i, r := range result {
		if r.ID != objects[i]["id"] || r.Result.Status != "SUCCESS" || r.Result.Errors != nil {
			return errors.New("projection publication failed")
		}
	}
	return nil
}

// Publish projects each segment's lexical object. The lexical object is the
// segment's permanent keyword-search anchor: once projected it is never
// rewritten or deleted. Weaviate re-indexes an updated object under a new
// document id, and a BM25 query does not read its index atomically, so an
// update or a delete of the object serving a segment can hide it.
func (s *Store) Publish(ctx context.Context, g content.Generation, org, corpusID, namespace string, v content.Version, seg content.Segmentation) error {
	if !className.MatchString(g.Collection) {
		return errors.New("invalid projection route")
	}
	if err := s.ensureMetadata(ctx, g); err != nil {
		return err
	}
	metadata := map[string]any{}
	if g.MetadataProjected {
		values := content.ProjectionMetadata(v, g.Fields)
		for _, f := range corpus.FilterFields(g.Fields) {
			if value, ok := values[f.Name]; ok {
				metadata[metadataProperty(f)] = metadataValue(value, f.Type)
			}
		}
	}
	texts, _ := content.ProjectionText(v, seg, g.Fields)
	objects := make([]map[string]any, 0, projectionBatch)
	flush := func() error {
		if len(objects) == 0 {
			return nil
		}
		if err := s.insertBatch(ctx, objects); err != nil {
			return err
		}
		for _, object := range objects {
			stored, found, err := s.object(ctx, g.Collection, object["id"].(string))
			if err != nil {
				return err
			}
			if !found || !sameProperties(stored.Properties, object["properties"].(map[string]any)) {
				return errors.New("projection verification mismatch")
			}
		}
		objects = objects[:0]
		return nil
	}

	for i, p := range seg.Segments {
		id := objectID(org, g.ID, p.ID)
		properties := map[string]any{"organization": org, "corpusId": corpusID, "generationId": g.ID, "versionId": v.ID, "segmentationId": seg.ID, "segmentId": p.ID, sourceNamespaceProperty: namespace, "body": texts[i].Body, "title": texts[i].Title}
		for key, value := range metadata {
			properties[key] = value
		}
		if pluginID := content.PluginOfRecipe(seg.Recipe); pluginID != "" {
			mediaType := v.SourceMediaType
			if mediaType == "" {
				mediaType = "text/plain"
			}
			properties[projectionPluginProperty] = pluginID
			properties[sourceMediaTypeProperty] = mediaType
		}
		if g.SpacesProjected && p.Derivation.LexicalText != "" {
			properties[lexicalProperty] = p.Derivation.LexicalText
		}
		existing, found, err := s.object(ctx, g.Collection, id)
		if err != nil {
			return err
		}
		if found && existing.Properties[sourceNamespaceProperty] == nil {
			// An anchor projected before Source Namespaces stays as written:
			// its generation is not source-filterable, and a rewrite would
			// re-index it.
			delete(properties, sourceNamespaceProperty)
		}
		if found && existing.Properties[projectionPluginProperty] == nil {
			delete(properties, projectionPluginProperty)
		}
		if found && existing.Properties[sourceMediaTypeProperty] == nil {
			delete(properties, sourceMediaTypeProperty)
		}
		if found && sameProperties(existing.Properties, properties) {
			continue
		}
		objects = append(objects, map[string]any{"class": g.Collection, "id": id, "properties": properties})
		if len(objects) == projectionBatch {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

func quote(v string) string { b, _ := json.Marshal(v); return string(b) }
func equal(field, value string) string {
	return "{path:[" + quote(field) + "],operator:Equal,valueText:" + quote(value) + "}"
}
func notEqual(field, value string) string {
	return "{path:[" + quote(field) + "],operator:NotEqual,valueText:" + quote(value) + "}"
}
func and(operands ...string) string {
	return "{operator:And,operands:[" + strings.Join(operands, ",") + "]}"
}
func or(operands ...string) string {
	return "{operator:Or,operands:[" + strings.Join(operands, ",") + "]}"
}

// projectionOwnerFilter keeps normal search on the served projection(s) of a
// generation. Legacy anchors without ownership remain searchable. An
// evaluation query selects one exact owner and intentionally excludes those
// legacy anchors.
func projectionOwnerFilter(g content.Generation, evaluationPlugin string) string {
	if evaluationPlugin != "" {
		return equal(projectionPluginProperty, evaluationPlugin)
	}
	// NotEqual uses the inverse equality bitmap in the pinned engine, so it
	// includes absent properties without requiring a collection null-state
	// index. Existing collections cannot add that index during bootstrap.
	known := map[string]bool{}
	for _, sp := range g.Spaces {
		if sp.OwnerPluginID != "" {
			known[sp.OwnerPluginID] = true
		}
	}
	if r := g.IngestionRouting; r != nil {
		if r.Default != "" {
			known[r.Default] = true
		}
		for _, owner := range r.Routes {
			known[owner] = true
		}
	}
	ids := make([]string, 0, len(known))
	for owner := range known {
		ids = append(ids, owner)
	}
	sort.Strings(ids)
	legacy := []string{equal("generationId", g.ID)}
	for _, owner := range ids {
		legacy = append(legacy, notEqual(projectionPluginProperty, owner))
	}
	operands := []string{and(legacy...)}
	if routing := g.IngestionRouting; routing != nil && routing.Default != "" {
		mediaTypes := make([]string, 0, len(routing.Routes))
		for mediaType := range routing.Routes {
			mediaTypes = append(mediaTypes, mediaType)
		}
		sort.Strings(mediaTypes)
		for _, mediaType := range mediaTypes {
			operands = append(operands, and(equal(projectionPluginProperty, routing.Routes[mediaType]), equal(sourceMediaTypeProperty, mediaType)))
		}
		defaultOperands := []string{equal(projectionPluginProperty, routing.Default)}
		for _, mediaType := range mediaTypes {
			defaultOperands = append(defaultOperands, notEqual(sourceMediaTypeProperty, mediaType))
		}
		operands = append(operands, and(defaultOperands...))
	} else {
		owners := map[string]bool{}
		for _, space := range g.Spaces {
			if space.Role == content.SpaceServed && space.OwnerPluginID != "" {
				owners[space.OwnerPluginID] = true
			}
		}
		ownerIDs := make([]string, 0, len(owners))
		for owner := range owners {
			ownerIDs = append(ownerIDs, owner)
		}
		sort.Strings(ownerIDs)
		for _, owner := range ownerIDs {
			operands = append(operands, equal(projectionPluginProperty, owner))
		}
	}
	if len(operands) == 1 {
		return operands[0]
	}
	return or(operands...)
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
		routeFilters := []string{equal("corpusId", r.CorpusID), equal("generationId", r.Generation.ID), projectionOwnerFilter(r.Generation, q.EvaluationPlugin)}
		typed, missing, err := corpus.ResolveFilters(q.Metadata, r.Generation.Fields)
		if err != nil {
			return nil, err
		}
		if len(missing) > 0 {
			continue
		}
		if len(typed) > 0 && !r.Generation.MetadataProjected {
			return nil, retrieval.ErrMetadataFilterUnavailable
		}
		// An empty generation may be routed before its first publication.
		// Reconcile declared fields before querying their schema properties.
		if len(typed) > 0 {
			if err := s.ensureMetadata(ctx, r.Generation); err != nil {
				return nil, err
			}
		}
		for _, f := range typed {
			routeFilters = append(routeFilters, metadataCondition(f))
		}
		filters = append(filters, and(routeFilters...))
	}
	if len(filters) == 0 {
		return []content.Candidate{}, nil
	}
	operands := []string{equal("organization", scope.Organization), "{operator:Or,operands:[" + strings.Join(filters, ",") + "]}"}
	// The source filter is part of the candidate query, so ranking and the
	// candidate limit apply within it in every mode.
	if len(q.SourceNamespaces) > 0 {
		sources := make([]string, 0, len(q.SourceNamespaces))
		for _, ns := range q.SourceNamespaces {
			sources = append(sources, equal(sourceNamespaceProperty, ns))
		}
		operands = append(operands, "{operator:Or,operands:["+strings.Join(sources, ",")+"]}")
	}
	where := "{operator:And,operands:[" + strings.Join(operands, ",") + "]}"
	// Every route names the space's vectors the same way, or the query
	// cannot rank them together.
	target := ""
	for _, r := range routes {
		space := q.Space
		if space == "" {
			space = r.Generation.SpaceID
		}
		name := s.VectorName(r.Generation, space)
		if target != "" && name != target {
			return nil, errors.New("routes store the space under different vectors")
		}
		target = name
		// A generation's named vectors exist from its first vector write; a
		// semantic or hybrid search before any (a new install, a Corpus whose
		// Versions still wait for their vectors) adds them, and finds nothing.
		if q.Mode != "lexical" {
			if err := s.ensureVectors(ctx, r.Generation); err != nil {
				return nil, err
			}
		}
	}
	// The source field ranks the title and body as written; the lexical field
	// ranks the lexical text an ingestion plugin produced.
	properties := `["title^2","body"]`
	if q.Field == retrieval.FieldLexical {
		properties = `["` + lexicalProperty + `"]`
	}
	alpha, fusion := 0.5, "relativeScoreFusion"
	if q.Hybrid != nil {
		alpha = q.Hybrid.Alpha
		if q.Hybrid.Fusion == retrieval.FusionRanked {
			fusion = "rankedFusion"
		}
	}
	branch := fmt.Sprintf("bm25:{query:%s,properties:%s}", quote(q.Query), properties)
	vector, _ := json.Marshal(q.Vector)
	if q.Mode == "semantic" {
		branch = fmt.Sprintf("nearVector:{vector:%s,targetVectors:[%s]}", vector, quote(target))
	}
	if q.Mode == "hybrid" {
		branch = fmt.Sprintf("hybrid:{query:%s,vector:%s,alpha:%s,fusionType:%s,properties:%s,targetVectors:[%s]}", quote(q.Query), vector, strconv.FormatFloat(alpha, 'f', -1, 64), fusion, properties, quote(target))
	}
	limit := retrieval.CandidateLimit
	if q.K > 0 {
		limit = q.K
	}
	query := fmt.Sprintf("{Get{%s(%s,where:%s,limit:%d){segmentId generationId _additional{score distance}}}}", collection, branch, where, limit)
	var response struct {
		Data struct {
			Get map[string][]struct {
				SegmentID    string `json:"segmentId"`
				GenerationID string `json:"generationId"`
				Additional   struct {
					Score    *string  `json:"score"`
					Distance *float64 `json:"distance"`
				} `json:"_additional"`
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
		// Higher is better: the BM25F or fused score, or 1 minus the distance.
		score := 0.0
		switch {
		case q.Mode == "semantic" && r.Additional.Distance != nil:
			score = 1 - *r.Additional.Distance
		case r.Additional.Score != nil:
			score, _ = strconv.ParseFloat(*r.Additional.Score, 64)
		}
		result = append(result, content.Candidate{SegmentID: r.SegmentID, GenerationID: r.GenerationID, Score: score})
	}
	return result, nil
}

// PublishEmbeddings attaches vectors without updating or deleting the segment's
// lexical anchor. For each segment it creates one enriched object beside the
// anchor carrying the segment's vector in every space of data, each under its
// named vector, verifies it, then removes any other enriched object of the
// segment. Search sees the anchor throughout; retrieval deduplicates by segment.
func (s *Store) PublishEmbeddings(ctx context.Context, g content.Generation, org string, data []content.EmbeddingData) error {
	if !className.MatchString(g.Collection) {
		return errors.New("invalid embedding projection")
	}
	if err := s.ensureVectors(ctx, g); err != nil {
		return err
	}
	// One enriched object per segment, in the order segments first appear.
	var order []string
	bySegment := map[string][]content.EmbeddingData{}
	for _, p := range data {
		e := p.Artifact
		if e.Organization != org || !g.Carries(e.SpaceID) {
			return errors.New("incompatible embedding projection")
		}
		raw, err := content.VectorBytes(p.Vector)
		if err != nil || content.Hash(raw) != e.Payload.SHA256 {
			return errors.New("embedding payload mismatch")
		}
		if _, ok := bySegment[e.SegmentID]; !ok {
			order = append(order, e.SegmentID)
		}
		bySegment[e.SegmentID] = append(bySegment[e.SegmentID], p)
	}

	type attachment struct {
		id, segment string
		vectors     []content.EmbeddingData
		stored      storedObject
		found       bool
	}
	pending := make([]attachment, 0, projectionBatch)
	objects := make([]map[string]any, 0, projectionBatch)
	flush := func() error {
		if len(objects) > 0 {
			if err := s.insertBatch(ctx, objects); err != nil {
				return err
			}
		}
		for _, a := range pending {
			stored, found := a.stored, a.found
			if !found {
				var err error
				stored, found, err = s.object(ctx, g.Collection, a.id)
				if err != nil {
					return err
				}
			}
			if !found || stored.Properties["segmentId"] != a.segment {
				return errors.New("embedding projection verification failed")
			}
			for _, p := range a.vectors {
				recovered, err := content.VectorBytes(stored.Vectors[s.VectorName(g, p.Artifact.SpaceID)])
				if err != nil || content.Hash(recovered) != p.Artifact.Payload.SHA256 {
					return errors.New("embedding projection verification failed")
				}
			}
			// Cleanup still runs on retries of objects already verified and written.
			if err := s.removeStaleEnriched(ctx, g, org, a.segment, a.id); err != nil {
				return err
			}
		}
		pending = pending[:0]
		objects = objects[:0]
		return nil
	}
	for _, segment := range order {
		vectors := bySegment[segment]
		payload := vectors[0].Artifact.Payload.SHA256
		if len(vectors) > 1 {
			// Several spaces: the identity covers every payload, by space.
			keys := make([]string, len(vectors))
			for i, p := range vectors {
				keys[i] = p.Artifact.SpaceID + "=" + p.Artifact.Payload.SHA256
			}
			sort.Strings(keys)
			payload = content.Hash([]byte(strings.Join(keys, "\n")))
		}
		named := map[string]any{}
		for _, p := range vectors {
			named[s.VectorName(g, p.Artifact.SpaceID)] = p.Vector
		}
		id := enrichedID(org, g.ID, segment, payload)
		stored, found, err := s.object(ctx, g.Collection, id)
		if err != nil {
			return err
		}
		if !found {
			// Never create an enriched object for a segment without its lexical anchor.
			anchor, projected, err := s.object(ctx, g.Collection, objectID(org, g.ID, segment))
			if err != nil {
				return err
			}
			if !projected {
				return retrieval.ErrProjectionMissing
			}
			lexical := map[string]any{}
			for _, key := range lexicalProperties {
				// An anchor written before a property existed has no value to copy.
				if value, ok := anchor.Properties[key]; ok {
					lexical[key] = value
				}
			}
			for key, value := range anchor.Properties {
				if strings.HasPrefix(key, "m_") {
					lexical[key] = value
				}
			}
			// Matching identities were omitted above, so a completed retry does
			// not rewrite the enriched object or its permanent lexical anchor.
			objects = append(objects, map[string]any{"class": g.Collection, "id": id, "properties": lexical, "vectors": named})
		}
		pending = append(pending, attachment{id: id, segment: segment, vectors: vectors, stored: stored, found: found})
		if len(pending) == projectionBatch {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

// staleListLimit bounds one listing of a segment's objects; a segment holds
// its anchor and, between two attachments, a few enriched objects.
const staleListLimit = 100

// removeStaleEnriched deletes the segment's other enriched objects in the
// generation. It lists the segment's objects and deletes the others by
// identity, never by a filter excluding the kept one: Weaviate indexes an
// object's id after its other properties and evaluates NotEqual as the
// complement of Equal, so while a concurrent attachment creates the kept
// object such a filter matches it. The lexical anchor is never deleted, so
// keyword search keeps the segment visible even while an enriched object is
// replaced. Segments belong to one Version, so no other Version's objects match.
func (s *Store) removeStaleEnriched(ctx context.Context, g content.Generation, org, segment, keep string) error {
	anchor := objectID(org, g.ID, segment)
	where := "{operator:And,operands:[" + equal("organization", org) + "," + equal("generationId", g.ID) + "," + equal("segmentId", segment) + "]}"
	query := fmt.Sprintf("{Get{%s(where:%s,limit:%d){_additional{id}}}}", g.Collection, where, staleListLimit)
	for {
		var response struct {
			Data struct {
				Get map[string][]struct {
					Additional struct {
						ID string `json:"id"`
					} `json:"_additional"`
				} `json:"Get"`
			} `json:"data"`
			Errors []any `json:"errors"`
		}
		if _, err := s.call(ctx, "POST", "/v1/graphql", map[string]any{"query": query}, &response); err != nil {
			return err
		}
		rows, ok := response.Data.Get[g.Collection]
		if len(response.Errors) > 0 || !ok {
			return errors.New("projection query failed")
		}
		for _, r := range rows {
			if r.Additional.ID == keep || r.Additional.ID == anchor {
				continue
			}
			// A concurrent attachment may have deleted it first.
			if status, err := s.call(ctx, "DELETE", "/v1/objects/"+g.Collection+"/"+r.Additional.ID, nil, nil); err != nil && status != http.StatusNotFound {
				return err
			}
		}
		// A full listing may hide more stale objects behind the ones just deleted.
		if len(rows) < staleListLimit {
			return nil
		}
	}
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
	// The purge only needs the HTTP configuration; do not copy the schema
	// cache's mutex when using its longer request timeout.
	scoped := Store{Endpoint: s.Endpoint, Client: client}
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
