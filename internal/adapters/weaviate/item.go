package weaviate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/keywords"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

const itemKind = "itemKind"
const passageText = "passageText"
const maxItemPassages = 2400

func itemProperty(f corpus.Field) string {
	b, _ := json.Marshal(f)
	return "k_" + content.Hash(b)[:32]
}

// analyzedProperty stores the field's keyword copy written by its analyzer.
func analyzedProperty(f corpus.Field, a keywords.Analyzer) string {
	return itemProperty(f) + "_" + a.Property
}

// fieldAnalyzers lists the distinct analyzers fields name, in field order.
func fieldAnalyzers(fields []corpus.Field) []keywords.Analyzer {
	var out []keywords.Analyzer
	seen := map[string]bool{}
	for _, f := range fields {
		if a, ok := keywords.Lookup(f.Analyzer); ok && !seen[a.Name] {
			seen[a.Name] = true
			out = append(out, a)
		}
	}
	return out
}

func (s *Store) ensureItemSchema(ctx context.Context, g content.Generation) error {
	if !g.ItemKeywordsProjected {
		return nil
	}
	props := []map[string]any{filterable(itemKind), filterable("recordId"), {"name": passageText, "dataType": []string{"text"}, "indexSearchable": false, "indexFilterable": false}}
	for _, f := range content.ItemFields(g.Fields) {
		props = append(props, searchable(itemProperty(f)))
		if a, ok := keywords.Lookup(f.Analyzer); ok {
			props = append(props, searchable(analyzedProperty(f, a)))
		}
	}
	return s.ensureProperties(ctx, g.Collection, props)
}

// Reconcile additive schemas once per process, including concurrent creation.
func (s *Store) ensureProperties(ctx context.Context, collection string, props []map[string]any) error {
	s.metadataMu.Lock()
	if s.metadataProperties == nil {
		s.metadataProperties = map[string]bool{}
	}
	var missing []map[string]any
	for _, p := range props {
		if !s.metadataProperties[collection+"/"+p["name"].(string)] {
			missing = append(missing, p)
		}
	}
	s.metadataMu.Unlock()
	if len(missing) == 0 {
		return nil
	}
	var schema struct {
		Properties []struct {
			Name string `json:"name"`
		} `json:"properties"`
	}
	if _, err := s.call(ctx, "GET", "/v1/schema/"+collection, nil, &schema); err != nil {
		return err
	}
	present := map[string]bool{}
	for _, p := range schema.Properties {
		present[p.Name] = true
	}
	for _, p := range missing {
		name := p["name"].(string)
		if !present[name] {
			if _, err := s.call(ctx, "POST", "/v1/schema/"+collection+"/properties", p, nil); err != nil {
				schema.Properties = nil
				if _, readErr := s.call(ctx, "GET", "/v1/schema/"+collection, nil, &schema); readErr != nil {
					return readErr
				}
				found := false
				for _, p := range schema.Properties {
					if p.Name == name {
						found = true
					}
				}
				if !found {
					return err
				}
			}
		}
		s.metadataMu.Lock()
		s.metadataProperties[collection+"/"+name] = true
		s.metadataMu.Unlock()
	}
	return nil
}

type itemRow struct {
	SegmentID    string `json:"segmentId"`
	GenerationID string `json:"generationId"`
	VersionID    string `json:"versionId"`
	Text         string `json:"passageText"`
	Additional   struct {
		Score    *string  `json:"score"`
		Distance *float64 `json:"distance"`
	} `json:"_additional"`
}

func (r itemRow) candidate(dense bool) content.Candidate {
	score := 0.0
	if dense && r.Additional.Distance != nil {
		score = 1 - *r.Additional.Distance
	} else if r.Additional.Score != nil {
		score, _ = strconv.ParseFloat(*r.Additional.Score, 64)
	}
	return content.Candidate{SegmentID: r.SegmentID, GenerationID: r.GenerationID, VersionID: r.VersionID, Score: score}
}
func (s *Store) itemQuery(ctx context.Context, collection, branch, where string, limit int, text bool) ([]itemRow, error) {
	fields := "segmentId generationId versionId"
	if text {
		fields += " passageText"
	}
	query := fmt.Sprintf("{Get{%s(%swhere:%s,limit:%d){%s _additional{score distance}}}}", collection, branch, where, limit, fields)
	var out struct {
		Data struct {
			Get map[string][]itemRow `json:"Get"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	responseLimit := int64(2 << 20)
	if text {
		// The bounded window can carry more text than a 128-row page.
		responseLimit = 16 << 20
	}
	reader := s
	if _, bounded := ctx.Deadline(); bounded && s.Client.Timeout != 0 {
		// Search is already bounded by its caller. Keep the shared transport
		// and leave the client used for projection writes alone.
		client := *s.Client
		client.Timeout = 0
		reader = &Store{Endpoint: s.Endpoint, Client: &client, apiKey: s.apiKey}
	}
	if _, err := reader.callLimit(ctx, "POST", "/v1/graphql", map[string]any{"query": query}, &out, responseLimit); err != nil {
		return nil, err
	}
	if len(out.Errors) > 0 {
		return nil, errors.New("item projection query failed")
	}
	rows, ok := out.Data.Get[collection]
	if !ok {
		return nil, errors.New("item projection response missing")
	}
	return rows, nil
}

func identityConditions(q retrieval.Request) []string {
	var out []string
	for _, field := range []struct {
		name   string
		values []string
	}{{"recordId", q.RecordIDs}, {"versionId", q.VersionIDs}} {
		if len(field.values) > 0 {
			var choices []string
			for _, v := range field.values {
				choices = append(choices, equal(field.name, v))
			}
			out = append(out, or(choices...))
		}
	}
	return out
}

func (s *Store) itemWhere(ctx context.Context, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request, kind string) (string, error) {
	var clauses []string
	for _, r := range routes {
		if !className.MatchString(r.Generation.Collection) || r.Generation.ID == "" {
			return "", errors.New("invalid projection route")
		}
		typed, missing, err := corpus.ResolveFilters(q.Metadata, r.Generation.Fields)
		if err != nil {
			return "", err
		}
		if len(missing) > 0 {
			continue
		}
		if len(typed) > 0 && !r.Generation.MetadataProjected {
			return "", retrieval.ErrMetadataFilterUnavailable
		}
		if err = s.ensureMetadata(ctx, r.Generation); err != nil {
			return "", err
		}
		values := []string{equal("corpusId", r.CorpusID), equal("generationId", r.Generation.ID)}
		if kind != "item" {
			values = append(values, projectionOwnerFilter(r.Generation, q.EvaluationPlugin))
		}
		for _, f := range typed {
			values = append(values, metadataConditionFor(r.Generation, f))
		}
		clauses = append(clauses, and(values...))
	}
	if len(clauses) == 0 {
		return "", nil
	}
	kinds := equal(itemKind, kind)
	if kind == "vector" {
		// Include earlier enriched objects with the passage kind; nearVector
		// only scores objects carrying this target vector.
		kinds = or(kinds, equal(itemKind, "passage"))
	}
	all := []string{equal("organization", scope.Organization), kinds, or(clauses...)}
	if len(q.SourceNamespaces) > 0 {
		var namespaces []string
		for _, v := range q.SourceNamespaces {
			namespaces = append(namespaces, equal(sourceNamespaceProperty, v))
		}
		all = append(all, or(namespaces...))
	}
	all = append(all, identityConditions(q)...)
	return and(all...), nil
}

func candidateKey(c content.Candidate, grouped bool) string {
	if grouped {
		return c.GenerationID + "/" + c.VersionID
	}
	return c.GenerationID + "/" + c.SegmentID
}
func keepBest(pool map[string]content.Candidate, c content.Candidate, grouped bool) {
	key := candidateKey(c, grouped)
	prev, ok := pool[key]
	if !ok || c.Score > prev.Score || (c.Score == prev.Score && c.SegmentID < prev.SegmentID) {
		pool[key] = c
	}
}
func ordered(pool map[string]content.Candidate) []content.Candidate {
	out := make([]content.Candidate, 0, len(pool))
	for _, c := range pool {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].SegmentID != out[j].SegmentID {
			return out[i].SegmentID < out[j].SegmentID
		}
		return candidateKey(out[i], true) < candidateKey(out[j], true)
	})
	return out
}

// passageTerms compiles the distinct query terms once per analyzer. Passage
// coverage is the most matches under any single analyzer.
type passageTerms struct {
	analyzer keywords.Analyzer
	terms    map[string]struct{}
}

func itemPassageTerms(query string, analyzers []keywords.Analyzer) []passageTerms {
	if len(analyzers) == 0 {
		analyzers = []keywords.Analyzer{keywords.Folded}
	}
	var out []passageTerms
	for _, a := range analyzers {
		terms := map[string]struct{}{}
		for word := range strings.FieldsSeq(a.Analyze(query)) {
			terms[word] = struct{}{}
		}
		if len(terms) > 0 {
			out = append(out, passageTerms{a, terms})
		}
	}
	return out
}

func termCoverage(text string, query []passageTerms) int {
	best := 0
	for _, q := range query {
		if len(q.terms) <= best {
			continue
		}
		matched := map[string]struct{}{}
		for word := range strings.FieldsSeq(q.analyzer.Analyze(text)) {
			if _, ok := q.terms[word]; ok {
				matched[word] = struct{}{}
				if len(matched) == len(q.terms) {
					break
				}
			}
		}
		best = max(best, len(matched))
	}
	return best
}

const passageOrder = `sort:[{path:["segmentId"],order:asc}],`

// Choose a representative only where text affects the answer. Ungrouped
// candidates all keep their segment IDs. With relative-score fusion a dense
// match supplies the representative and the sparse score is independent of it.
func (s *Store) selectItemPassages(ctx context.Context, collection, where string, rows []itemRow, query []passageTerms, densePassages func() map[string]content.Candidate) ([]itemRow, bool, error) {
	best := map[string]itemRow{}
	ambiguous := map[string][]itemRow{}
	for _, row := range rows {
		if row.SegmentID == "" {
			continue
		}
		key := candidateKey(row.candidate(false), true)
		first, seen := best[key]
		if !seen {
			best[key] = row
		} else if len(query) > 0 {
			if len(ambiguous[key]) == 0 {
				ambiguous[key] = append(ambiguous[key], first)
			}
			ambiguous[key] = append(ambiguous[key], row)
		}
	}
	if len(ambiguous) > 0 && densePassages != nil {
		for key := range densePassages() {
			delete(ambiguous, key)
		}
	}
	if len(ambiguous) > 0 {
		// Pin the text read to the canonical rows in the metadata window.
		// The same segment ID can appear in more than one generation.
		var ids []string
		for _, group := range ambiguous {
			for _, row := range group {
				ids = append(ids, and(equal("generationId", row.GenerationID), equal("segmentId", row.SegmentID)))
			}
		}
		sort.Strings(ids)
		textRows, err := s.itemQuery(ctx, collection, passageOrder, and(where, or(ids...)), len(ids), true)
		if errors.Is(err, errResponseLimit) {
			// The metadata window already holds the canonical first
			// passage used for byte-limit recovery.
			return rows[:1], true, nil
		}
		if err != nil {
			return nil, false, err
		}
		scores := map[string]int{}
		for _, row := range textRows {
			key := candidateKey(row.candidate(false), true)
			if len(ambiguous[key]) == 0 {
				continue
			}
			score := termCoverage(row.Text, query)
			if score > scores[key] || (score == scores[key] && row.SegmentID < best[key].SegmentID) {
				scores[key] = score
				best[key] = row
			}
		}
	}
	selected := make([]itemRow, 0, len(best))
	for _, row := range best {
		selected = append(selected, row)
	}
	return selected, len(rows) == maxItemPassages, nil
}

type itemPartition struct {
	routes    []retrieval.Route
	fields    []corpus.Field
	analyzers []keywords.Analyzer
}

// itemSearch consolidates sparse copies and max passage scores BEFORE fusion.
// Alpha is applied once to one sparse/dense score per item, irrespective of
// the number of passages, analyzers, or Corpus configuration partitions.
func (s *Store) itemSearch(ctx context.Context, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
	collection := routes[0].Generation.Collection
	grouped := q.GroupBy == "record"
	limit := q.K
	if limit == 0 {
		limit = retrieval.CandidateLimit
	}
	partitions := map[string]*itemPartition{}
	var partitionKeys []string
	for _, r := range routes {
		if r.Generation.Collection != collection {
			return nil, errors.New("invalid projection route")
		}
		if err := s.ensureItemSchema(ctx, r.Generation); err != nil {
			return nil, err
		}
		fields := content.ItemFields(r.Generation.Fields)
		b, _ := json.Marshal(fields)
		key := string(b)
		if partitions[key] == nil {
			partitions[key] = &itemPartition{fields: fields, analyzers: fieldAnalyzers(fields)}
			partitionKeys = append(partitionKeys, key)
		}
		partitions[key].routes = append(partitions[key].routes, r)
	}
	sort.Strings(partitionKeys)
	alpha, fusion := 0.5, retrieval.FusionRelativeScore
	if q.Hybrid != nil {
		alpha, fusion = q.Hybrid.Alpha, q.Hybrid.Fusion
	}
	var sparse, dense map[string]content.Candidate
	legs, ctx := errgroup.WithContext(ctx)
	denseDone := make(chan struct{})
	if q.Mode != "lexical" {
		legs.Go(func() error {
			var err error
			dense, err = s.itemDense(ctx, collection, routes, scope, q, limit, grouped)
			close(denseDone)
			return err
		})
	}
	if q.Mode != "semantic" {
		legs.Go(func() error {
			sparse = map[string]content.Candidate{}
			var densePassages func() map[string]content.Candidate
			if q.Mode != "lexical" && alpha > 0 && fusion != retrieval.FusionRanked {
				densePassages = func() map[string]content.Candidate {
					select {
					case <-denseDone:
						return dense
					case <-ctx.Done():
						return nil
					}
				}
			}
			for _, key := range partitionKeys {
				if err := s.itemSparse(ctx, collection, partitions[key], scope, q, limit, grouped, sparse, densePassages); err != nil {
					return err
				}
			}
			return nil
		})
	}
	if err := legs.Wait(); err != nil {
		return nil, err
	}
	if q.Mode == "lexical" {
		return ordered(sparse), nil
	}
	if q.Mode == "semantic" {
		return ordered(dense), nil
	}
	return fuseItems(sparse, dense, alpha, fusion, grouped), nil
}

func (s *Store) itemSparse(ctx context.Context, collection string, p *itemPartition, scope corpus.Scope, q retrieval.Request, limit int, grouped bool, sparse map[string]content.Candidate, densePassages func() map[string]content.Candidate) error {
	where, err := s.itemWhere(ctx, p.routes, scope, q, "item")
	if err != nil || where == "" {
		return err
	}
	requests := itemBM25(p, q.Query)
	query := itemPassageTerms(q.Query, p.analyzers)
	items := map[string]content.Candidate{}
	// An ownerless item may not yet have passages from the selected
	// ingestion owner. Allow one refill at twice the candidate depth.
	for round, depth := 0, limit; round < 2; round, depth = round+1, min(maxItemPassages, depth*2) {
		results := make([][]itemRow, len(requests))
		copies, copyCtx := errgroup.WithContext(ctx)
		for i, branch := range requests {
			copies.Go(func() error {
				rows, err := s.itemQuery(copyCtx, collection, branch, where, depth, false)
				results[i] = rows
				return err
			})
		}
		if err := copies.Wait(); err != nil {
			return err
		}
		more := false
		for _, rows := range results {
			more = more || len(rows) == depth
			for _, row := range rows {
				keepBest(items, row.candidate(false), true)
			}
		}
		if len(items) == 0 {
			break
		}
		passageWhere, err := s.itemWhere(ctx, p.routes, scope, q, "passage")
		if err != nil {
			return err
		}
		var ids []string
		for _, c := range ordered(items) {
			ids = append(ids, equal("versionId", c.VersionID))
		}
		passageWhere = and(passageWhere, or(ids...))
		rows, err := s.itemQuery(ctx, collection, passageOrder, passageWhere, maxItemPassages, false)
		if err != nil {
			return err
		}
		exhausted := len(rows) == maxItemPassages
		if grouped {
			rows, exhausted, err = s.selectItemPassages(ctx, collection, passageWhere, rows, query, densePassages)
			if err != nil {
				return err
			}
		}
		chosen := map[string]bool{}
		for _, row := range rows {
			item, ok := items[candidateKey(row.candidate(false), true)]
			if !ok || row.SegmentID == "" {
				continue
			}
			c := row.candidate(false)
			c.Score = item.Score
			id := candidateKey(c, grouped)
			chosen[id] = true
			// On equal scores keep the passage chosen before the refill.
			if prev, ok := sparse[id]; !ok || c.Score > prev.Score {
				sparse[id] = c
			}
		}
		if len(chosen) >= limit || !more || depth >= maxItemPassages || exhausted {
			break
		}
	}
	return nil
}

func itemBM25(p *itemPartition, query string) []string {
	var branches []string
	for _, a := range append([]keywords.Analyzer{{}}, p.analyzers...) {
		text := query
		if a.Name != "" {
			text = a.Analyze(query)
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		var props []string
		for _, f := range p.fields {
			property := itemProperty(f)
			if a.Name != "" {
				if f.Analyzer != a.Name {
					continue
				}
				property = analyzedProperty(f, a)
			}
			props = append(props, property+"^"+strconv.Itoa(f.EffectiveBoost()))
		}
		encoded, _ := json.Marshal(props)
		branches = append(branches, fmt.Sprintf("bm25:{query:%s,properties:%s},", quote(text), encoded))
	}
	return branches
}

func (s *Store) itemDense(ctx context.Context, collection string, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request, limit int, grouped bool) (map[string]content.Candidate, error) {
	where, err := s.itemWhere(ctx, routes, scope, q, "vector")
	if err != nil || where == "" {
		return nil, err
	}
	target := ""
	for _, r := range routes {
		space := q.Space
		if space == "" {
			space = r.Generation.SpaceID
		}
		name := s.VectorName(r.Generation, space)
		if target != "" && target != name {
			return nil, errors.New("routes store different vectors")
		}
		target = name
		if err := s.ensureVectors(ctx, r.Generation); err != nil {
			return nil, err
		}
	}
	vector, _ := json.Marshal(q.Vector)
	branch := fmt.Sprintf("nearVector:{vector:%s,targetVectors:[%s]},", vector, quote(target))
	dense := map[string]content.Candidate{}
	for depth := limit; ; depth = min(maxItemPassages, depth*2) {
		rows, err := s.itemQuery(ctx, collection, branch, where, depth, false)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if r.SegmentID != "" {
				keepBest(dense, r.candidate(true), grouped)
			}
		}
		if !grouped || len(dense) >= limit || len(rows) < depth || depth >= maxItemPassages {
			return dense, nil
		}
	}
}

func fuseItems(sparse, dense map[string]content.Candidate, alpha float64, fusion string, grouped bool) []content.Candidate {
	out := map[string]content.Candidate{}
	for _, leg := range []struct {
		pool   map[string]content.Candidate
		weight float64
		dense  bool
	}{{sparse, 1 - alpha, false}, {dense, alpha, true}} {
		rows := ordered(leg.pool)
		if len(rows) == 0 || leg.weight == 0 {
			continue
		}
		lo, hi := rows[len(rows)-1].Score, rows[0].Score
		for i, c := range rows {
			score := 1.0
			if fusion == retrieval.FusionRanked {
				score = 1 / float64(60+i+1)
			} else if hi != lo {
				score = (c.Score - lo) / (hi - lo)
			}
			key := candidateKey(c, grouped)
			prev, ok := out[key]
			if ok {
				if leg.dense {
					c.Score = prev.Score
				} else {
					c = prev
				}
			} else {
				c.Score = 0
			}
			c.Score += leg.weight * score
			out[key] = c
		}
	}
	return ordered(out)
}

// Search queries routes in partitions that one index request can rank
// together: by recipe (item keywords or passages) and, for a vector search,
// by the named vector that stores the space, which differs between
// generations built with different index settings.
func (s *Store) Search(ctx context.Context, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
	if len(routes) == 0 {
		return nil, errors.New("projection route missing")
	}
	type partition struct {
		item   bool
		routes []retrieval.Route
	}
	partitions := map[string]*partition{}
	var keys []string
	for _, r := range routes {
		// Partitions are merged, so every route must share one collection.
		if r.Generation.Collection != routes[0].Generation.Collection {
			return nil, errors.New("invalid projection route")
		}
		item := r.Generation.ItemKeywordsProjected && q.Field != retrieval.FieldLexical
		key := strconv.FormatBool(item)
		if q.Mode != "lexical" {
			space := q.Space
			if space == "" {
				space = r.Generation.SpaceID
			}
			key += "/" + s.VectorName(r.Generation, space)
		}
		if partitions[key] == nil {
			partitions[key] = &partition{item: item}
			keys = append(keys, key)
		}
		partitions[key].routes = append(partitions[key].routes, r)
	}
	sort.Strings(keys)
	var out []content.Candidate
	// Each partition's BM25 and hybrid scores are normalized over its own
	// candidates (item and passage BM25 also score different populations).
	// Reciprocal ranks put mixed partitions on a common scale without
	// reapplying hybrid alpha. Semantic scores are 1 minus the distance in
	// one space, comparable as they are.
	mixedRanks := len(keys) > 1 && q.Mode != "semantic"
	for _, key := range keys {
		p := partitions[key]
		var rows []content.Candidate
		var err error
		if p.item {
			rows, err = s.itemSearch(ctx, p.routes, scope, q)
		} else {
			if len(q.RecordIDs) > 0 {
				for _, r := range p.routes {
					if !r.Generation.ItemKeywordsProjected {
						return nil, retrieval.ErrMetadataFilterUnavailable
					}
				}
			}
			rows, err = s.searchLegacy(ctx, p.routes, scope, q)
		}
		if err != nil {
			return nil, err
		}
		if mixedRanks {
			rows = retrieval.NormalizeCandidateRanks(rows)
		}
		out = append(out, rows...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].SegmentID < out[j].SegmentID
	})
	if q.K > 0 && len(out) > q.K {
		out = out[:q.K]
	}
	return out, nil
}
