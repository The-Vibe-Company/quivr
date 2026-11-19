// Package weaviate adapts the pinned local engine; its physical schema stays private.
package weaviate

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	wv "github.com/weaviate/weaviate-go-client/v6"
	"github.com/weaviate/weaviate-go-client/v6/data"
	"github.com/weaviate/weaviate-go-client/v6/query"
	"github.com/weaviate/weaviate-go-client/v6/query/filter"
	"github.com/weaviate/weaviate-go-client/v6/types"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

// InitialCollection is shared by every logical Projection Generation; objects carry
// their generation so PostgreSQL routing, not aliases, selects what a Corpus serves.
const InitialCollection = "QuivrTextV4"

var className = regexp.MustCompile(`^[A-Z][A-Za-z0-9_]*$`)

type Store struct {
	apiKey string
	// options configure the client library; clientMu guards the lazy client.
	options  []wv.Option
	clientMu sync.Mutex
	client   *wv.Client
	// PurgeTimeout bounds each purge selection/delete request independently of search.
	PurgeTimeout time.Duration
	purgeBatch   atomic.Int64
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
// under: legacyVector for a generation built before named spaces; for a
// space the generation records an index setting for, a name derived from
// the space key and that setting, so a changed setting builds a new index;
// otherwise legacyVector for the built-in space and a name derived from the
// space key alone.
func (s *Store) VectorName(g content.Generation, space string) string {
	if !g.SpacesProjected {
		return legacyVector
	}
	if index := spaceIndex(g, space); index != nil {
		return "s_" + content.Hash([]byte("quivr/named-vector/v2\x00" + space + "\x00" + index.Quantization + "\x00" + strconv.Itoa(index.RescoreLimit)))[:24]
	}
	if space == s.LegacySpace {
		return legacyVector
	}
	return "s_" + content.Hash([]byte("quivr/named-vector/v1\x00" + space))[:24]
}

// spaceIndex is the index setting g records for space, nil when none.
func spaceIndex(g content.Generation, space string) *content.VectorIndex {
	for _, sp := range g.Spaces {
		if sp.ID == space {
			return sp.Index
		}
	}
	return nil
}

// failed hides the provider's explanation from errors callers may surface.
func failed(err error) error {
	if err == nil {
		return nil
	}
	var request requestError
	if !errors.As(err, &request) {
		return err
	}
	if unreachable(err) {
		return unavailable{request}
	}
	return rejected{request}
}

// unavailable and rejected keep the fixed messages of the adapter while
// preserving timeout and cancellation classification.
type unavailable struct{ requestError }

func (unavailable) Error() string { return "projection connection unavailable" }

type rejected struct{ requestError }

func (rejected) Error() string { return "projection request failed" }

func (s *Store) Ready(ctx context.Context) error {
	return failed(s.call(ctx, func(ctx context.Context, c *wv.Client) error {
		ready, err := c.IsReady(ctx)
		if err == nil && !ready {
			err = errors.New("index not ready")
		}
		return err
	}))
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

var lexicalProperties = []string{"organization", "corpusId", "generationId", "versionId", "segmentationId", "segmentId", projectionPluginProperty, sourceMediaTypeProperty, sourceNamespaceProperty, "body", "title", lexicalProperty, "recordId", itemKind, passageText}

type storedObject struct {
	ID         string
	Properties map[string]any
	Vectors    map[string][]float32
}

// projected is one object to write: a deterministic identity, its properties
// and, for an enriched object, its named vectors.
type projected struct {
	id         string
	properties map[string]any
	vectors    map[string][]float32
}

// stored converts a query result to the adapter's own shape. Dates become the
// strings they were written as, so a copied anchor keeps its bytes.
func stored(o query.Object[map[string]any]) storedObject {
	out := storedObject{ID: o.UUID.String(), Properties: map[string]any{}, Vectors: map[string][]float32{}}
	for key, value := range o.Properties {
		switch v := value.(type) {
		case nil:
		case time.Time:
			out.Properties[key] = v.UTC().Format(time.RFC3339Nano)
		case int64:
			out.Properties[key] = float64(v)
		default:
			out.Properties[key] = value
		}
	}
	for name, vector := range o.Vectors {
		out.Vectors[name] = vector.Single
	}
	return out
}

// object reads one object by identity, with the named vectors asked for.
func (s *Store) object(ctx context.Context, collection, id string, vectors ...string) (storedObject, bool, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return storedObject{}, false, errors.New("invalid projection identity")
	}
	rows, err := s.objects(ctx, collection, query.OverAll{Filter: identity(parsed), Limit: 1, ReturnVectors: vectors})
	if err != nil || len(rows) == 0 {
		return storedObject{}, false, err
	}
	return rows[0], true, nil
}

// writeVisibility bounds how long a just-written object may stay invisible to
// a search by id.
const writeVisibility = 2 * time.Second

// written reads an object the caller just inserted. The index answers a read
// by id with a filtered search, which can miss an object for a moment while
// another writer replaces the same identity. Absence is therefore reported
// only after it has held for writeVisibility.
func (s *Store) written(ctx context.Context, collection, id string, vectors ...string) (storedObject, bool, error) {
	deadline := time.Now().Add(writeVisibility)
	for wait := 10 * time.Millisecond; ; wait = min(2*wait, 200*time.Millisecond) {
		object, found, err := s.object(ctx, collection, id, vectors...)
		if err != nil || found || !time.Now().Before(deadline) {
			return object, found, err
		}
		select {
		case <-ctx.Done():
			return storedObject{}, false, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// objects lists objects without ranking them.
func (s *Store) objects(ctx context.Context, collection string, request query.OverAll) ([]storedObject, error) {
	var result *query.Result
	err := s.call(ctx, func(ctx context.Context, c *wv.Client) (err error) {
		result, err = c.Collections.Use(collection).Query.OverAll(ctx, request)
		return err
	})
	if err != nil {
		return nil, failed(err)
	}
	rows := make([]storedObject, len(result.Objects))
	for i, o := range result.Objects {
		rows[i] = stored(o)
	}
	return rows, nil
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

// insertBatch checks every object result. A lost reply is reconciled by the
// caller reading deterministic identities; an explicit request or object
// error must never be treated as success.
func (s *Store) insertBatch(ctx context.Context, collection string, objects []projected) error {
	batch := make([]*data.Object, len(objects))
	for i, o := range objects {
		id, err := uuid.Parse(o.id)
		if err != nil {
			return errors.New("invalid projection identity")
		}
		object := &data.Object{UUID: &id, Properties: maps.Clone(o.properties)}
		for name, vector := range o.vectors {
			object.Vectors = append(object.Vectors, types.Vector{Name: name, Single: vector})
		}
		batch[i] = object
	}
	refused := false
	err := s.call(ctx, func(ctx context.Context, c *wv.Client) error {
		_, err := c.Collections.Use(collection).Data.Insert(ctx, batch...)
		var objectErrors data.InsertError
		refused = errors.As(err, &objectErrors)
		return err
	})
	switch {
	case err == nil:
		return nil
	case refused:
		return errors.New("projection publication failed")
	case lost(err):
		return nil
	}
	var request requestError
	errors.As(err, &request)
	if !request.grpc {
		return fmt.Errorf("projection batch request failed: %w", failed(err))
	}
	return fmt.Errorf("projection batch request failed (%s): %w", request.code, failed(err))
}

// insert uses the same checked batch path for a standalone item anchor.
func (s *Store) insert(ctx context.Context, collection string, object projected) error {
	return s.insertBatch(ctx, collection, []projected{object})
}

// Publish projects each segment's immutable anchor and, for item-capable
// generations, a separate keyword anchor for the Version. Weaviate re-indexes an updated object under a new
// document id, and a BM25 query does not read its index atomically, so an
// update or a delete of the object serving a segment can hide it.
func (s *Store) Publish(ctx context.Context, g content.Generation, org, corpusID, namespace string, v content.Version, seg content.Segmentation) error {
	if !className.MatchString(g.Collection) {
		return errors.New("invalid projection route")
	}
	if err := s.ensureItemSchema(ctx, g); err != nil {
		return err
	}
	if err := s.ensureMetadata(ctx, g); err != nil {
		return err
	}
	metadata := map[string]any{}
	if g.MetadataProjected {
		values := content.ProjectionMetadata(v, g.Fields)
		for _, f := range corpus.FilterFields(g.Fields) {
			if value, ok := values[f.Name]; ok {
				metadata[metadataPropertyFor(g, f)] = metadataValue(value, f.Type)
			}
		}
	}
	texts, _ := content.ProjectionText(v, seg, g.Fields)
	objects := make([]projected, 0, projectionBatch)
	flush := func() error {
		if len(objects) == 0 {
			return nil
		}
		if err := s.insertBatch(ctx, g.Collection, objects); err != nil {
			return err
		}
		for _, object := range objects {
			stored, found, err := s.written(ctx, g.Collection, object.id)
			if err != nil {
				return err
			}
			if !found || !sameProperties(stored.Properties, object.properties) {
				return errors.New("projection verification mismatch")
			}
		}
		objects = objects[:0]
		return nil
	}

	for i, p := range seg.Segments {
		id := objectID(org, g.ID, p.ID)
		properties := map[string]any{"organization": org, "corpusId": corpusID, "generationId": g.ID, "versionId": v.ID, "segmentationId": seg.ID, "segmentId": p.ID, sourceNamespaceProperty: namespace, "body": texts[i].Body, "title": texts[i].Title}
		if g.ItemKeywordsProjected {
			delete(properties, "body")
			delete(properties, "title")
			properties[itemKind] = "passage"
			properties[passageText] = p.Text
			properties["recordId"] = v.RecordID
		}
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
		objects = append(objects, projected{id: id, properties: properties})
		if len(objects) == projectionBatch {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if g.ItemKeywordsProjected {
		return s.publishItem(ctx, g, org, corpusID, namespace, v, metadata)
	}
	return nil
}

// projectionOwnerFilter keeps normal search on the served projection(s) of a
// generation. Legacy anchors without ownership remain searchable. An
// evaluation query selects one exact owner and intentionally excludes those
// legacy anchors.
func projectionOwnerFilter(g content.Generation, evaluationPlugin string) filter.Expr {
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
	legacy := []filter.Expr{equal("generationId", g.ID)}
	for _, owner := range ids {
		legacy = append(legacy, notEqual(projectionPluginProperty, owner))
	}
	operands := []filter.Expr{and(legacy...)}
	if routing := g.IngestionRouting; routing != nil && routing.Default != "" {
		mediaTypes := make([]string, 0, len(routing.Routes))
		for mediaType := range routing.Routes {
			mediaTypes = append(mediaTypes, mediaType)
		}
		sort.Strings(mediaTypes)
		for _, mediaType := range mediaTypes {
			operands = append(operands, and(equal(projectionPluginProperty, routing.Routes[mediaType]), equal(sourceMediaTypeProperty, mediaType)))
		}
		defaultOperands := []filter.Expr{equal(projectionPluginProperty, routing.Default)}
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
func (s *Store) searchLegacy(ctx context.Context, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
	if len(routes) == 0 {
		return nil, errors.New("projection route missing")
	}
	collection := routes[0].Generation.Collection
	filters := []filter.Expr{}
	for _, r := range routes {
		if r.Generation.Collection != collection || !className.MatchString(collection) || r.Generation.ID == "" {
			return nil, errors.New("invalid projection route")
		}
		routeFilters := []filter.Expr{equal("corpusId", r.CorpusID), equal("generationId", r.Generation.ID), projectionOwnerFilter(r.Generation, q.EvaluationPlugin)}
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
			routeFilters = append(routeFilters, metadataConditionFor(r.Generation, f))
		}
		filters = append(filters, and(routeFilters...))
	}
	if len(filters) == 0 {
		return []content.Candidate{}, nil
	}
	operands := []filter.Expr{equal("organization", scope.Organization), or(filters...)}
	// The source filter is part of the candidate query, so ranking and the
	// candidate limit apply within it in every mode.
	if len(q.SourceNamespaces) > 0 {
		sources := make([]filter.Expr, 0, len(q.SourceNamespaces))
		for _, ns := range q.SourceNamespaces {
			sources = append(sources, equal(sourceNamespaceProperty, ns))
		}
		operands = append(operands, or(sources...))
	}
	operands = append(operands, identityConditions(q)...)
	where := and(operands...)
	// For a vector search, every route names the space's vectors the same
	// way, or the query cannot rank them together; a keyword search reads
	// no vector.
	target := ""
	for _, r := range routes {
		if q.Mode == "lexical" {
			break
		}
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
		if err := s.ensureVectors(ctx, r.Generation); err != nil {
			return nil, err
		}
	}
	// The source field ranks the title and body as written; the lexical field
	// ranks the lexical text an ingestion plugin produced.
	properties := []string{"title^2", "body"}
	if q.Field == retrieval.FieldLexical {
		properties = []string{lexicalProperty}
	}
	alpha, rankedFusion := float32(0.5), false
	if q.Hybrid != nil {
		alpha, rankedFusion = float32(q.Hybrid.Alpha), q.Hybrid.Fusion == retrieval.FusionRanked
	}
	limit := retrieval.CandidateLimit
	if q.K > 0 {
		limit = q.K
	}
	search := ranked{filter: where, limit: limit, properties: []string{"segmentId", "generationId"}}
	switch q.Mode {
	case "semantic":
		search.vector, search.target = q.Vector, target
	case "hybrid":
		search.keywords, search.keywordProperties = q.Query, properties
		search.vector, search.target = q.Vector, target
		search.hybrid, search.alpha, search.rankedFusion = true, alpha, rankedFusion
	default:
		search.keywords, search.keywordProperties = q.Query, properties
	}
	rows, err := s.rank(ctx, collection, search)
	if err != nil {
		if errors.Is(err, errResponseLimit) {
			return nil, err
		}
		return nil, queryFailed(err, "projection query failed")
	}
	result := make([]content.Candidate, 0, len(rows))
	for _, r := range rows {
		segment, _ := r.Properties["segmentId"].(string)
		generation, _ := r.Properties["generationId"].(string)
		if segment == "" || generation == "" {
			return nil, errors.New("projection candidate invalid")
		}
		// Higher is better: the BM25F or fused score, or 1 minus the distance.
		score := 0.0
		switch {
		case q.Mode == "semantic" && r.distance != nil:
			score = 1 - *r.distance
		case r.score != nil:
			score = *r.score
		}
		result = append(result, content.Candidate{SegmentID: segment, GenerationID: generation, Score: score})
	}
	return result, nil
}

// ranked is one scored index query: keyword (keywords), vector (vector), or
// both fused by the index (hybrid).
type ranked struct {
	filter            filter.Expr
	limit             int
	properties        []string
	keywords          string
	keywordProperties []string
	vector            []float32
	target            string
	hybrid            bool
	alpha             float32
	rankedFusion      bool
}

type rankedObject struct {
	storedObject
	score, distance *float64
}

// widen keeps the decimal value the index reported for a 32-bit number.
func widen(v *float32) *float64 {
	if v == nil {
		return nil
	}
	out, _ := strconv.ParseFloat(strconv.FormatFloat(float64(*v), 'g', -1, 32), 64)
	return &out
}

var errResponseLimit = errors.New("projection response exceeds byte limit")

// rank runs one scored query.
func (s *Store) rank(ctx context.Context, collection string, r ranked) ([]rankedObject, error) {
	var result *query.Result
	metadata := query.ReturnMetadata{Score: true, Distance: true}
	err := s.call(ctx, func(ctx context.Context, c *wv.Client) (err error) {
		search := c.Collections.Use(collection).Query
		nearVector := query.NearVector{Target: types.Vector{Name: r.target, Single: r.vector}, Filter: r.filter, Limit: r.limit, ReturnProperties: r.properties, ReturnMetadata: metadata}
		switch {
		case r.hybrid:
			alpha, fusion := r.alpha, query.HybridFusionRelativeScore
			if r.rankedFusion {
				fusion = query.HybridFusionRanked
			}
			result, err = search.Hybrid(ctx, query.Hybrid{Query: r.keywords, QueryProperties: r.keywordProperties, NearVector: &query.NearVector{Target: nearVector.Target}, Alpha: &alpha, Fusion: fusion, Filter: r.filter, Limit: r.limit, ReturnProperties: r.properties, ReturnMetadata: metadata})
		case r.target != "":
			result, err = search.NearVector(ctx, nearVector)
		default:
			result, err = search.BM25(ctx, query.BM25{Query: r.keywords, QueryProperties: r.keywordProperties, Filter: r.filter, Limit: r.limit, ReturnProperties: r.properties, ReturnMetadata: query.ReturnMetadata{Score: true}})
		}
		return err
	})
	if tooLarge(err) {
		return nil, errResponseLimit
	}
	if err != nil {
		return nil, err
	}
	rows := make([]rankedObject, len(result.Objects))
	for i, o := range result.Objects {
		rows[i] = rankedObject{storedObject: stored(o), score: widen(o.Metadata.Score), distance: widen(o.Metadata.Distance)}
	}
	return rows, nil
}

// queryFailed keeps connection failures distinct from a refused query.
func queryFailed(err error, message string) error {
	if unreachable(err) {
		return failed(err)
	}
	return errors.New(message)
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
	}
	pending := make([]attachment, 0, projectionBatch)
	objects := make([]projected, 0, projectionBatch)
	flush := func() error {
		if len(objects) > 0 {
			if err := s.insertBatch(ctx, g.Collection, objects); err != nil {
				return err
			}
		}
		for _, a := range pending {
			// Existing objects may disappear while the rest of the batch is built.
			// Verify the current identity before cleanup or durable coverage.
			names := make([]string, len(a.vectors))
			for i, p := range a.vectors {
				names[i] = s.VectorName(g, p.Artifact.SpaceID)
			}
			stored, found, err := s.written(ctx, g.Collection, a.id, names...)
			if err != nil {
				return err
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
		named := map[string][]float32{}
		for _, p := range vectors {
			named[s.VectorName(g, p.Artifact.SpaceID)] = p.Vector
		}
		id := enrichedID(org, g.ID, segment, payload)
		_, found, err := s.object(ctx, g.Collection, id)
		if err != nil {
			return err
		}
		if !found {
			// Never create an enriched object for a segment without its lexical anchor.
			anchor, anchored, err := s.object(ctx, g.Collection, objectID(org, g.ID, segment))
			if err != nil {
				return err
			}
			if !anchored {
				return retrieval.ErrProjectionMissing
			}
			lexical := map[string]any{}
			for _, key := range lexicalProperties {
				// An anchor written before a property existed has no value to copy.
				if value, ok := anchor.Properties[key]; ok {
					lexical[key] = value
				}
			}
			if g.ItemKeywordsProjected {
				lexical[itemKind] = "vector"
			}
			for key, value := range anchor.Properties {
				if strings.HasPrefix(key, "m_") {
					lexical[key] = value
				}
			}
			// Matching identities were omitted above, so a completed retry does
			// not rewrite the enriched object or its permanent lexical anchor.
			objects = append(objects, projected{id: id, properties: lexical, vectors: named})
		}
		pending = append(pending, attachment{id: id, segment: segment, vectors: vectors})
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
	where := and(equal("organization", org), equal("generationId", g.ID), equal("segmentId", segment))
	for {
		rows, err := s.objects(ctx, g.Collection, query.OverAll{Filter: where, Limit: staleListLimit, ReturnProperties: []string{}})
		if err != nil {
			return queryFailed(err, "projection query failed")
		}
		for _, r := range rows {
			if r.ID == keep || r.ID == anchor {
				continue
			}
			id, err := uuid.Parse(r.ID)
			if err != nil {
				return errors.New("invalid projection identity")
			}
			// A concurrent attachment may have deleted it first.
			err = s.call(ctx, func(ctx context.Context, c *wv.Client) error {
				return c.Collections.Use(g.Collection).Data.Delete(ctx, id)
			})
			if err != nil && !notFound(err) {
				return failed(err)
			}
		}
		// A full listing may hide more stale objects behind the ones just deleted.
		if len(rows) < staleListLimit {
			return nil
		}
	}
}

var _ retrieval.PurgeProjection = (*Store)(nil)
