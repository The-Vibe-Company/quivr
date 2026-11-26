package weaviate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/sync/errgroup"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/keywords"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

const itemKind = "itemKind"
const segmentKind = "segment"
const passageText = "passageText"
const maxItemPassages = 2400

const (
	// Bound text-based passage selection to the top items that can reach the
	// caller, plus modest headroom for candidates replacing entries rejected
	// during hydration. Tail items only need one valid segment for fusion and fallback.
	maxPassageSelectionItems    = retrieval.MaxLimit
	maxPassageSelectionHeadroom = 10
	minTopKPassageScan          = 256
	maxTopKPassageScan          = 512
	passagesPerTopItem          = 16
	maxTailPassageScan          = 512
	passagesPerTailItem         = 4
	missingPassageConcurrency   = 8
	minDensePassageScan         = 64
	maxDensePassageScan         = 600

	// Minimum lexical information density for a substantive passage. Shorter
	// boilerplate fragments (headers, footers, page markers) rank below
	// substantive passages when both match (or neither matches) the query.
	minSubstantiveTerms = 8
	minSubstantiveRunes = 40

	// Within-item passage BM25 parameters. Flooring substantive passage length
	// keeps short notices from exploiting BM25 length normalization against
	// richer paragraphs.
	passageBM25K1       = 1.2
	passageBM25B        = 0.4
	passageBM25MinTerms = 24.0
)

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
	props := []map[string]any{filterable(itemKind), filterable("recordId"), embeddingRevisionProperty(), {"name": passageText, "dataType": []string{"text"}, "indexSearchable": false, "indexFilterable": false}}
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
func (s *Store) searchRows(ctx context.Context, collection string, branch searchBranch, where filter, order searchOrder, limit int, item, text bool) ([]itemRow, error) {
	fields := []string{"segmentId", "generationId"}
	if item {
		fields = append(fields, "versionId")
	}
	if text {
		fields = append(fields, passageText)
	}
	responseLimit := 2 << 20
	if text {
		// The bounded window can carry more text than a 128-row page.
		responseLimit = 16 << 20
	}
	if s.grpc != nil {
		return s.grpcSearchRows(ctx, collection, branch, where, order, limit, fields, responseLimit, item)
	}
	query := fmt.Sprintf("{Get{%s(%s%s%s%slimit:%d){%s _additional{score distance}}}}",
		collection, tenantArgument(ctx), branch.graphql(), order.graphql(), where.graphql(), limit, strings.Join(fields, " "))
	var out struct {
		Data struct {
			Get map[string][]itemRow `json:"Get"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	reader := s
	if _, bounded := ctx.Deadline(); item && bounded && s.Client.Timeout != 0 {
		// Item search is already bounded by its caller. Keep the shared
		// transport and leave the client used for projection writes alone.
		client := *s.Client
		client.Timeout = 0
		reader = &Store{Endpoint: s.Endpoint, Client: &client, apiKey: s.apiKey}
	}
	if _, err := reader.callLimit(ctx, "POST", "/v1/graphql", map[string]any{"query": query}, &out, int64(responseLimit)); err != nil {
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

func identityConditions(q retrieval.Request) []filter {
	var out []filter
	for _, field := range []struct {
		name   string
		values []string
	}{{"recordId", q.RecordIDs}, {"versionId", q.VersionIDs}} {
		if len(field.values) > 0 {
			var choices []filter
			for _, v := range field.values {
				choices = append(choices, equal(field.name, v))
			}
			out = append(out, or(choices...))
		}
	}
	return out
}

func (s *Store) itemWhere(ctx context.Context, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request, kind string) (filter, error) {
	var clauses []filter
	for _, r := range routes {
		if !className.MatchString(r.Generation.Collection) || r.Generation.ID == "" {
			return filter{}, errors.New("invalid projection route")
		}
		typed, missing, err := corpus.ResolveFilters(q.Metadata, r.Generation.Fields)
		if err != nil {
			return filter{}, err
		}
		if len(missing) > 0 {
			continue
		}
		if len(typed) > 0 && !r.Generation.MetadataProjected {
			return filter{}, retrieval.ErrMetadataFilterUnavailable
		}
		if err = s.ensureMetadata(ctx, r.Generation); err != nil {
			return filter{}, err
		}
		var values []filter
		if tenantFrom(ctx) == "" {
			values = append(values, equal("corpusId", r.CorpusID), equal("generationId", r.Generation.ID))
		}
		if kind != "item" {
			values = append(values, projectionOwnerFilter(r.Generation, q.EvaluationPlugin))
		}
		for _, f := range typed {
			values = append(values, metadataConditionFor(r.Generation, f))
		}
		clauses = append(clauses, and(values...))
	}
	if len(clauses) == 0 {
		return filter{}, nil
	}
	kinds := equal(itemKind, kind)
	if kind == "vector" {
		// Include earlier enriched objects with the passage kind; nearVector
		// only scores objects carrying this target vector.
		kinds = or(kinds, equal(itemKind, "passage"), equal(itemKind, segmentKind))
	} else if kind == "passage" {
		kinds = or(kinds, equal(itemKind, segmentKind))
	}
	all := []filter{kinds, or(clauses...)}
	if tenantFrom(ctx) == "" {
		all = append([]filter{equal("organization", scope.Organization)}, all...)
	}
	if len(q.SourceNamespaces) > 0 {
		var namespaces []filter
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
// selection scores each passage with minimum information density and within-item
// BM25 tie-breaking, taking the best score under any single analyzer.
type passageTerms struct {
	analyzer     keywords.Analyzer
	terms        map[string]struct{}
	orderedTerms []string
}

func itemPassageTerms(query string, analyzers []keywords.Analyzer) []passageTerms {
	compile := func(list []keywords.Analyzer) []passageTerms {
		var out []passageTerms
		for _, a := range list {
			terms := map[string]struct{}{}
			for word := range strings.FieldsSeq(a.Analyze(query)) {
				terms[word] = struct{}{}
			}
			if len(terms) > 0 {
				ordered := make([]string, 0, len(terms))
				for word := range terms {
					ordered = append(ordered, word)
				}
				sort.Strings(ordered)
				out = append(out, passageTerms{analyzer: a, terms: terms, orderedTerms: ordered})
			}
		}
		return out
	}
	if len(analyzers) == 0 {
		return compile([]keywords.Analyzer{keywords.Folded})
	}
	if out := compile(analyzers); len(out) > 0 {
		return out
	}
	// If configured analyzers drop every query token (for example, a query of
	// stopwords under a stemming analyzer), fall back to language-neutral folding
	// so passage selection still scores matches and information density.
	return compile([]keywords.Analyzer{keywords.Folded})
}

type passageScore struct {
	hasMatch    bool
	substantive bool
	coverage    int
	bm25        float64
	uniqueTerms int
	uniqueRunes int
}

func passageDensity(text string) (uniqueTerms, uniqueRunes int, substantive bool) {
	seen := map[string]struct{}{}
	for word := range strings.FieldsSeq(keywords.Folded.Analyze(text)) {
		if _, ok := seen[word]; !ok {
			seen[word] = struct{}{}
			uniqueRunes += utf8.RuneCountInString(word)
		}
	}
	uniqueTerms = len(seen)
	substantive = uniqueTerms >= minSubstantiveTerms && uniqueRunes >= minSubstantiveRunes
	return uniqueTerms, uniqueRunes, substantive
}

func betterPassage(a, b passageScore, segA, segB string) bool {
	if a.hasMatch != b.hasMatch {
		return a.hasMatch
	}
	if a.substantive != b.substantive {
		return a.substantive
	}
	if a.hasMatch {
		if a.coverage != b.coverage {
			return a.coverage > b.coverage
		}
		if a.bm25 != b.bm25 {
			return a.bm25 > b.bm25
		}
	} else {
		if a.uniqueTerms != b.uniqueTerms {
			return a.uniqueTerms > b.uniqueTerms
		}
		if a.uniqueRunes != b.uniqueRunes {
			return a.uniqueRunes > b.uniqueRunes
		}
	}
	return segA < segB
}

func pickBestPassage(group []itemRow, query []passageTerms) itemRow {
	n := len(group)
	scores := make([]passageScore, n)
	substantiveCount := 0
	for i, row := range group {
		uniqueTerms, uniqueRunes, substantive := passageDensity(row.Text)
		scores[i] = passageScore{
			substantive: substantive,
			uniqueTerms: uniqueTerms,
			uniqueRunes: uniqueRunes,
		}
		if substantive {
			substantiveCount++
		}
	}
	for _, q := range query {
		tfs := make([]map[string]int, n)
		dls := make([]float64, n)
		df := make(map[string]int, len(q.terms))
		totalDL := 0.0
		substantiveDL := 0.0
		for i, row := range group {
			count := 0
			var tf map[string]int
			for word := range strings.FieldsSeq(q.analyzer.Analyze(row.Text)) {
				count++
				if _, ok := q.terms[word]; ok {
					if tf == nil {
						tf = map[string]int{}
					}
					tf[word]++
				}
			}
			for word := range tf {
				df[word]++
			}
			dl := float64(count)
			if scores[i].substantive {
				dl = max(dl, passageBM25MinTerms)
				substantiveDL += dl
			}
			tfs[i] = tf
			dls[i] = dl
			totalDL += dl
		}
		avgdl := max(totalDL/float64(n), 1.0)
		if substantiveCount > 0 {
			avgdl = max(substantiveDL/float64(substantiveCount), passageBM25MinTerms)
		}
		for i := range group {
			cov := len(tfs[i])
			if cov == 0 || cov < scores[i].coverage {
				continue
			}
			norm := 1 - passageBM25B + passageBM25B*(dls[i]/avgdl)
			bm25 := 0.0
			for _, term := range q.orderedTerms {
				freq := tfs[i][term]
				if freq == 0 {
					continue
				}
				idf := math.Log(1 + (float64(n)-float64(df[term])+0.5)/(float64(df[term])+0.5))
				tfNorm := (float64(freq) * (passageBM25K1 + 1)) / (float64(freq) + passageBM25K1*norm)
				bm25 += idf * tfNorm
			}
			if cov > scores[i].coverage || (cov == scores[i].coverage && bm25 > scores[i].bm25) {
				scores[i].hasMatch = true
				scores[i].coverage = cov
				scores[i].bm25 = bm25
			}
		}
	}
	bestIdx := 0
	for i := 1; i < n; i++ {
		if betterPassage(scores[i], scores[bestIdx], group[i].SegmentID, group[bestIdx].SegmentID) {
			bestIdx = i
		}
	}
	return group[bestIdx]
}

var passageOrder = searchOrder{field: "segmentId"}

func passageSelectionLimit(q retrieval.Request, limit int) int {
	if q.Limit > 0 {
		// Keep modest headroom above q.Limit (including at retrieval.MaxLimit)
		// for candidates replacing entries filtered during hydration.
		headroom := min(q.Limit, maxPassageSelectionHeadroom)
		return min(limit, min(q.Limit, maxPassageSelectionItems)+headroom)
	}
	return min(limit, maxPassageSelectionItems)
}

func topPassageScanLimit(itemCount int) int {
	if itemCount <= 0 {
		return 0
	}
	return min(maxTopKPassageScan, max(minTopKPassageScan, itemCount*passagesPerTopItem))
}

func tailPassageScanLimit(itemCount int) int {
	if itemCount <= 0 {
		return 0
	}
	return min(maxTailPassageScan, max(itemCount, itemCount*passagesPerTailItem))
}

func versionFilter(items []content.Candidate) filter {
	seen := make(map[string]bool, len(items))
	ids := make([]filter, 0, len(items))
	for _, c := range items {
		if seen[c.VersionID] {
			continue
		}
		seen[c.VersionID] = true
		ids = append(ids, equal("versionId", c.VersionID))
	}
	return or(ids...)
}

func groupPassageRows(rows []itemRow) (map[string]itemRow, map[string][]itemRow) {
	best := map[string]itemRow{}
	ambiguous := map[string][]itemRow{}
	addPassageRows(best, ambiguous, rows)
	return best, ambiguous
}

func addPassageRows(best map[string]itemRow, ambiguous map[string][]itemRow, rows []itemRow) {
	seenSegments := make(map[string]bool, len(rows))
	for _, row := range best {
		seenSegments[candidateKey(row.candidate(false), false)] = true
	}
	for _, group := range ambiguous {
		for _, row := range group {
			seenSegments[candidateKey(row.candidate(false), false)] = true
		}
	}
	for _, row := range rows {
		segment := candidateKey(row.candidate(false), false)
		if row.SegmentID == "" || seenSegments[segment] {
			continue
		}
		seenSegments[segment] = true
		key := candidateKey(row.candidate(false), true)
		first, seen := best[key]
		if !seen {
			best[key] = row
		} else {
			if len(ambiguous[key]) == 0 {
				ambiguous[key] = append(ambiguous[key], first)
			}
			ambiguous[key] = append(ambiguous[key], row)
		}
	}
}

func missingCandidates(items []content.Candidate, present map[string]itemRow) []content.Candidate {
	var missing []content.Candidate
	for _, c := range items {
		if _, ok := present[candidateKey(c, true)]; !ok {
			missing = append(missing, c)
		}
	}
	return missing
}

// recoverMissingPassages guarantees that a high-passage item filling a shared
// segmentId-ordered scan window cannot starve other ranked versions.
func (s *Store) recoverMissingPassages(ctx context.Context, collection string, passageWhere filter, missing []content.Candidate, perVersionLimit int) ([]itemRow, error) {
	seen := make(map[string]bool, len(missing))
	versions := make([]string, 0, len(missing))
	for _, c := range missing {
		if c.VersionID == "" || seen[c.VersionID] {
			continue
		}
		seen[c.VersionID] = true
		versions = append(versions, c.VersionID)
	}
	if len(versions) == 0 {
		return nil, nil
	}
	results := make([][]itemRow, len(versions))
	group, gctx := errgroup.WithContext(ctx)
	group.SetLimit(missingPassageConcurrency)
	for i, ver := range versions {
		group.Go(func() error {
			rows, err := s.searchRows(gctx, collection, searchBranch{}, and(passageWhere, equal("versionId", ver)), passageOrder, perVersionLimit, true, false)
			results[i] = rows
			return err
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	var out []itemRow
	for _, rows := range results {
		out = append(out, rows...)
	}
	return out, nil
}

// Choose a representative only where text affects the answer. Ungrouped
// candidates all keep their segment IDs. With relative-score fusion a dense
// match supplies the representative and the sparse score is independent of it.
func (s *Store) resolveAmbiguousPassages(ctx context.Context, collection string, where filter, best map[string]itemRow, ambiguous map[string][]itemRow, query []passageTerms) (bool, error) {
	if len(ambiguous) == 0 {
		return false, nil
	}
	// Pin the text read to the canonical rows in the metadata window.
	// The same segment ID can appear in more than one generation.
	var rows []itemRow
	for _, group := range ambiguous {
		rows = append(rows, group...)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].GenerationID == rows[j].GenerationID {
			return rows[i].SegmentID < rows[j].SegmentID
		}
		return rows[i].GenerationID < rows[j].GenerationID
	})
	ids := make([]filter, 0, len(rows))
	for _, row := range rows {
		id := equal("segmentId", row.SegmentID)
		if tenantFrom(ctx) == "" {
			id = and(equal("generationId", row.GenerationID), id)
		}
		ids = append(ids, id)
	}
	// Reserve room for the seed and replacement of a segment during a swap.
	textRows, err := s.searchRows(ctx, collection, searchBranch{}, and(where, or(ids...)), passageOrder, 2*len(ids), true, true)
	if errors.Is(err, errResponseLimit) {
		// Retain the canonical first passage already captured per item in the
		// metadata window for byte-limit recovery.
		return true, nil
	}
	if err != nil {
		return false, err
	}
	byItem := map[string][]itemRow{}
	seenSegments := make(map[string]bool, len(textRows))
	for _, row := range textRows {
		segment := candidateKey(row.candidate(false), false)
		if row.SegmentID == "" || seenSegments[segment] {
			continue
		}
		seenSegments[segment] = true
		key := candidateKey(row.candidate(false), true)
		if len(ambiguous[key]) == 0 {
			continue
		}
		byItem[key] = append(byItem[key], row)
	}
	for key, group := range byItem {
		best[key] = pickBestPassage(group, query)
	}
	return false, nil
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
	if err != nil || where.operator == 0 {
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
				rows, err := s.searchRows(copyCtx, collection, branch, where, searchOrder{}, depth, true, false)
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
		ranked := ordered(items)
		if len(ranked) > depth {
			ranked = ranked[:depth]
		}
		var rows []itemRow
		var exhausted bool
		if !grouped {
			scanLimit := min(maxItemPassages, max(limit, topPassageScanLimit(len(ranked))))
			rows, err = s.searchRows(ctx, collection, searchBranch{}, and(passageWhere, versionFilter(ranked)), passageOrder, scanLimit, true, false)
			if err != nil {
				return err
			}
			exhausted = len(rows) == scanLimit
			if exhausted {
				present, _ := groupPassageRows(rows)
				bounded := ranked[:min(len(ranked), passageSelectionLimit(q, limit))]
				if missing := missingCandidates(bounded, present); len(missing) > 0 {
					recovered, err := s.recoverMissingPassages(ctx, collection, passageWhere, missing, passagesPerTopItem)
					if err != nil {
						return err
					}
					rows = append(rows, recovered...)
				}
			}
		} else {
			rows, exhausted, err = s.selectSparseGroupedPassages(ctx, collection, passageWhere, ranked, query, passageSelectionLimit(q, limit), densePassages)
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

func (s *Store) selectSparseGroupedPassages(ctx context.Context, collection string, passageWhere filter, ranked []content.Candidate, query []passageTerms, selectK int, densePassages func() map[string]content.Candidate) ([]itemRow, bool, error) {
	topCount := min(len(ranked), selectK)
	top := ranked[:topCount]
	tail := ranked[topCount:]

	topLimit := topPassageScanLimit(len(top))
	topRows, err := s.searchRows(ctx, collection, searchBranch{}, and(passageWhere, versionFilter(top)), passageOrder, topLimit, true, false)
	if err != nil {
		return nil, false, err
	}
	best, ambiguous := groupPassageRows(topRows)

	var denseMap map[string]content.Candidate
	if densePassages != nil && (len(ambiguous) > 0 || len(tail) > 0 || len(best) < len(top)) {
		denseMap = densePassages()
		for key := range denseMap {
			delete(ambiguous, key)
		}
		for _, c := range top {
			key := candidateKey(c, true)
			if _, ok := best[key]; !ok {
				if d, ok := denseMap[key]; ok {
					best[key] = itemRow{SegmentID: d.SegmentID, GenerationID: d.GenerationID, VersionID: d.VersionID}
				}
			}
		}
	}

	// If a high-passage version filled the shared segmentId-ordered scan cap,
	// recover omitted top-ranked versions individually so none are starved.
	if len(topRows) == topLimit {
		if missingTop := missingCandidates(top, best); len(missingTop) > 0 {
			recovered, err := s.recoverMissingPassages(ctx, collection, passageWhere, missingTop, passagesPerTopItem)
			if err != nil {
				return nil, false, err
			}
			addPassageRows(best, ambiguous, recovered)
			for key := range denseMap {
				delete(ambiguous, key)
			}
		}
	}

	topCovered := 0
	for _, c := range top {
		if _, ok := best[candidateKey(c, true)]; ok {
			topCovered++
		}
	}

	var tailNeed []content.Candidate
	for _, c := range tail {
		key := candidateKey(c, true)
		if denseMap != nil {
			if d, ok := denseMap[key]; ok {
				best[key] = itemRow{SegmentID: d.SegmentID, GenerationID: d.GenerationID, VersionID: d.VersionID}
				continue
			}
		}
		tailNeed = append(tailNeed, c)
	}

	var exhausted bool
	if len(tailNeed) > 0 {
		tailLimit := tailPassageScanLimit(len(tailNeed))
		tailWhere := and(passageWhere, versionFilter(tailNeed))
		fetchTail := func(tailCtx context.Context, perMissing int) (map[string]itemRow, map[string][]itemRow, error) {
			tailRows, err := s.searchRows(tailCtx, collection, searchBranch{}, tailWhere, passageOrder, tailLimit, true, false)
			if err != nil {
				return nil, nil, err
			}
			tailBest, tailAmbiguous := groupPassageRows(tailRows)
			if len(tailRows) == tailLimit {
				if missingTail := missingCandidates(tailNeed, tailBest); len(missingTail) > 0 {
					recovered, err := s.recoverMissingPassages(tailCtx, collection, passageWhere, missingTail, perMissing)
					if err != nil {
						return nil, nil, err
					}
					addPassageRows(tailBest, tailAmbiguous, recovered)
				}
			}
			return tailBest, tailAmbiguous, nil
		}
		if topCovered >= selectK {
			var tailBest map[string]itemRow
			var textExhausted bool
			group, gctx := errgroup.WithContext(ctx)
			group.Go(func() error {
				var err error
				textExhausted, err = s.resolveAmbiguousPassages(gctx, collection, passageWhere, best, ambiguous, query)
				return err
			})
			group.Go(func() error {
				var err error
				tailBest, _, err = fetchTail(gctx, 1)
				return err
			})
			if err := group.Wait(); err != nil {
				return nil, false, err
			}
			exhausted = textExhausted
			for key, row := range tailBest {
				if _, ok := best[key]; !ok {
					best[key] = row
				}
			}
		} else {
			tailBest, tailAmbiguous, err := fetchTail(ctx, passagesPerTopItem)
			if err != nil {
				return nil, false, err
			}
			for _, c := range tail {
				key := candidateKey(c, true)
				if _, already := best[key]; already {
					if topCovered < selectK {
						topCovered++
					}
					continue
				}
				row, ok := tailBest[key]
				if !ok {
					continue
				}
				best[key] = row
				if topCovered < selectK {
					if g := tailAmbiguous[key]; len(g) > 0 {
						ambiguous[key] = g
					}
					topCovered++
				}
			}
			textExhausted, err := s.resolveAmbiguousPassages(ctx, collection, passageWhere, best, ambiguous, query)
			if err != nil {
				return nil, false, err
			}
			exhausted = textExhausted
		}
	} else {
		textExhausted, err := s.resolveAmbiguousPassages(ctx, collection, passageWhere, best, ambiguous, query)
		if err != nil {
			return nil, false, err
		}
		exhausted = textExhausted
	}

	selected := make([]itemRow, 0, len(best))
	for _, row := range best {
		selected = append(selected, row)
	}
	return selected, exhausted, nil
}

func itemBM25(p *itemPartition, query string) []searchBranch {
	var branches []searchBranch
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
		branches = append(branches, searchBranch{mode: "lexical", query: text, properties: props})
	}
	return branches
}

func (s *Store) itemDense(ctx context.Context, collection string, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request, limit int, grouped bool) (map[string]content.Candidate, error) {
	where, err := s.itemWhere(ctx, routes, scope, q, "vector")
	if err != nil || where.operator == 0 {
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
	branch := searchBranch{mode: "semantic", vector: q.Vector, target: target}
	dense := map[string]content.Candidate{}
	maxDepth := limit
	if grouped {
		maxDepth = min(maxItemPassages, max(limit, min(maxDensePassageScan, max(minDensePassageScan, limit*4))))
	}
	for depth := limit; ; depth = min(maxDepth, depth*2) {
		rows, err := s.searchRows(ctx, collection, branch, where, searchOrder{}, depth, true, false)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if r.SegmentID != "" {
				keepBest(dense, r.candidate(true), grouped)
			}
		}
		if !grouped || len(dense) >= limit || len(rows) < depth || depth >= maxDepth {
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
func (s *Store) searchCollection(ctx context.Context, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
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
