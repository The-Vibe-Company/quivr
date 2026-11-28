package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/visibility"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// statementTrace counts the statements a pool sends, one per query round trip.
type statementTrace struct {
	mu  sync.Mutex
	sql []string
}

func (s *statementTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	s.mu.Lock()
	s.sql = append(s.sql, d.SQL)
	s.mu.Unlock()
	return ctx
}

func (*statementTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (s *statementTrace) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.sql
	s.sql = nil
	return out
}

func tracedPool(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tracer pgx.QueryTracer) *pgxpool.Pool {
	t.Helper()
	cfg := pool.Config()
	cfg.ConnConfig.Tracer = tracer
	traced, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(traced.Close)
	return traced
}

// Owns the round-trip budget of the search control plane (Authorize, then the
// routed Generation and live space description of every Corpus) and the exact
// values it returns. Golden values are literal: they cover a served, a
// rebuilding, an empty and an evaluation-present Corpus, a Corpus routed to an
// unprojected generation with ingestion routing and pinned fields, a
// registered engine space, an unregistered space, and delegated visibility.
func TestSearchControlPlaneStatementsAndGoldenDescriptions(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	pool := queueWorkPool(t, ctx)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	const corpora = 16
	ids := make([]string, corpora)
	for i := range ids {
		ids[i] = fmt.Sprintf("corpus-%02d", i)
	}
	exec(`INSERT INTO corpora(organization,id,request_key,canonical_request,name,retrieval)
SELECT 'example',id,id,'',id,'{}' FROM unnest($1::text[]) id`, ids)
	exec(`INSERT INTO content_blobs(organization,blob_id,object_key,sha256,byte_length)
VALUES('example','blob','fixture','fixture',1);
INSERT INTO vector_spaces(id,manifest,name,version,owner_plugin_id,owner_plugin_version,model,dimensions,metric,indexes,query_modalities,role)
VALUES('served','{"kind":"served"}','served','1','example.owner','1.0.0','example-model',4,'cosine','{text}','{text}','served'),
 ('evaluation','{"kind":"evaluation"}','evaluation','2','example.evaluation','2.0.0','example-evaluation',8,'dot','{text,image}','{text}','evaluation'),
 ('engine','{}','engine','','','','engine-model',16,'cosine','{text}','{text,image}','evaluation');
INSERT INTO projection_generations(id,collection,profile_version,active,space_id,spaces_projected,spaces,source_namespace_projected,metadata_projected,item_keywords_projected)
VALUES('generation','collection','fixture',true,'served',true,
 '[{"id":"served","metric":"cosine","role":"served","owner_plugin_id":"example.owner"},
   {"id":"evaluation","metric":"dot","role":"evaluation","owner_plugin_id":"example.evaluation"},
   {"id":"engine","metric":"cosine","role":"evaluation"},
   {"id":"unregistered","metric":"cosine","role":"evaluation","owner_plugin_id":"example.absent"}]',true,true,true);
INSERT INTO projection_generations(id,collection,profile_version,active,space_id,spaces_projected,spaces,retrieval,ingestion_routing)
VALUES('legacy','legacy-collection','fixture',false,'served',false,'[]',
 '{"fields":[{"name":"title","type":"text","roles":["search"]}]}',
 '{"default":"example.owner","routes":{"application/pdf":"example.evaluation"}}');
INSERT INTO corpus_projection_routes(organization,corpus_id,generation_id) VALUES('example','corpus-15','legacy');
-- Delegated visibility makes only corpus-01's source public.
INSERT INTO visibility_grants(organization,corpus_id,source_namespace,visibility,allowed_principals)
VALUES('example','corpus-01','fixture','public','{}')`)
	// Corpus i%4: 0 empty, 1 served owner present, 2 rebuilding (only an
	// owner this generation does not select has a served cut), 3 served owner
	// plus an evaluation-role cut.
	for i, id := range ids {
		if i%4 == 0 {
			continue
		}
		exec(`INSERT INTO records(organization,id,corpus_id,namespace,record_key,current_version_id)
SELECT 'example',$1||'-'||n,$1,'fixture',n::text,$1||'-version-'||n FROM generate_series(1,3) n`, id)
		exec(`INSERT INTO record_versions(organization,id,record_id,slot,digest,acceptance_order,source_position,text_blob_id,manifest_blob_id,provenance,baseline_ready)
SELECT 'example',$1||'-version-'||n,$1||'-'||n,'current',n::text,1,'','blob','blob','{}',true FROM generate_series(1,3) n`, id)
		owners := map[int][]string{1: {"example.owner"}, 2: {"example.evaluation"}, 3: {"example.owner", "example.evaluation"}}[i%4]
		for _, owner := range owners {
			// Only a served cut makes an owner present; class 3's evaluation
			// cut sits beside the served owner's projection.
			role := "served"
			if i%4 == 3 && owner == "example.evaluation" {
				role = "evaluation"
			}
			exec(`INSERT INTO segmentations(organization,id,version_id,recipe,digest)
SELECT 'example',$1||'-'||$2||'-'||n,$1||'-version-'||n,'plugin:'||$2||'@1.0.0',n::text FROM generate_series(1,3) n`, id, owner)
			exec(`INSERT INTO projection_coverage(organization,version_id,generation_id,segmentation_id,role)
SELECT 'example',$1||'-version-'||n,'generation',$1||'-'||$2||'-'||n,$3 FROM generate_series(1,3) n`, id, owner, role)
		}
	}

	trace := &statementTrace{}
	routing := ProjectionStore{Pool: tracedPool(t, ctx, pool, trace)}
	spaces := SpaceStore{Pool: routing.Pool}
	for _, delegated := range []bool{false, true} {
		t.Run(fmt.Sprintf("delegated=%t", delegated), func(t *testing.T) {
			ctx := ctx
			if delegated {
				ctx = visibility.WithPrincipals(ctx, []string{"user:reader"})
			}
			trace.take()
			if err := routing.Authorize(ctx, corpus.Scope{Organization: "example", Corpora: ids}, ids); err != nil {
				t.Fatal(err)
			}
			for i, id := range ids {
				g, err := routing.Generation(ctx, "example", id)
				if err != nil {
					t.Fatal(err)
				}
				described, coverage, err := spaces.DescribeVectorSpaces(ctx, "example", id)
				if err != nil {
					t.Fatal(err)
				}
				want := goldenGeneration(i)
				if !reflect.DeepEqual(g, want) || !reflect.DeepEqual(described, want) {
					t.Fatalf("%s: Generation %#v, described %#v, want %#v", id, g, described, want)
				}
				if wantCoverage := goldenCoverage(i, delegated); !reflect.DeepEqual(coverage, wantCoverage) {
					got, _ := json.Marshal(coverage)
					wantJSON, _ := json.Marshal(wantCoverage)
					t.Fatalf("%s: coverage\n got %s\nwant %s", id, got, wantJSON)
				}
			}
			statements := trace.take()
			budget := 2*corpora + 1
			t.Logf("search control plane: %d statements for %d corpora (budget %d)", len(statements), corpora, budget)
			if len(statements) > budget {
				t.Fatalf("search control plane sent %d statements for %d corpora, want at most %d:\n%s",
					len(statements), corpora, budget, summarizeStatements(statements))
			}
		})
	}
}

func summarizeStatements(statements []string) string {
	counts := map[string]int{}
	var order []string
	for _, sql := range statements {
		head := strings.Join(strings.Fields(sql), " ")
		head = head[:min(len(head), 90)]
		if counts[head] == 0 {
			order = append(order, head)
		}
		counts[head]++
	}
	var b strings.Builder
	for _, head := range order {
		fmt.Fprintf(&b, "%4d × %s\n", counts[head], head)
	}
	return b.String()
}

func goldenGeneration(i int) content.Generation {
	if i == 15 {
		return content.Generation{ID: "legacy", Collection: "legacy-collection", ProfileVersion: "fixture", SpaceID: "served",
			Fields:           []corpus.Field{{Name: "title", Type: "text", Roles: []string{"search"}}},
			Spaces:           []content.GenerationSpace{},
			IngestionRouting: &content.IngestionRouting{Default: "example.owner", Routes: map[string]string{"application/pdf": "example.evaluation"}}}
	}
	return content.Generation{ID: "generation", Collection: "collection", ProfileVersion: "fixture", SpaceID: "served",
		SourceNamespaceProjected: true, MetadataProjected: true, ItemKeywordsProjected: true, SpacesProjected: true,
		Spaces: []content.GenerationSpace{
			{ID: "served", Metric: "cosine", Role: "served", OwnerPluginID: "example.owner"},
			{ID: "evaluation", Metric: "dot", Role: "evaluation", OwnerPluginID: "example.evaluation"},
			{ID: "engine", Metric: "cosine", Role: "evaluation"},
			{ID: "unregistered", Metric: "cosine", Role: "evaluation", OwnerPluginID: "example.absent"},
		}}
}

func goldenCoverage(i int, delegated bool) []content.SpaceCoverage {
	presence := func(n int64) *int64 { return &n }
	served := content.SpaceCoverage{RegisteredSpace: content.RegisteredSpace{
		VectorSpace: content.VectorSpace{ID: "served", Manifest: json.RawMessage(`{"kind": "served"}`), Dimensions: 4},
		Name:        "served", Version: "1", OwnerPluginID: "example.owner", OwnerPluginVersion: "1.0.0", Model: "example-model",
		Metric: "cosine", Indexes: []string{"text"}, QueryModalities: []string{"text"}, Role: "served"},
		GenerationRole: content.SpaceServed, ServingSegments: presence(0), CoverageUnknown: true}
	if i == 15 {
		// The unprojected generation serves its SpaceID alone and has no
		// coverage, so eligibility decides whether the Corpus is empty.
		served.CorpusEmpty = delegated
		return []content.SpaceCoverage{served}
	}
	evaluation := content.SpaceCoverage{RegisteredSpace: content.RegisteredSpace{
		VectorSpace: content.VectorSpace{ID: "evaluation", Manifest: json.RawMessage(`{"kind": "evaluation"}`), Dimensions: 8},
		Name:        "evaluation", Version: "2", OwnerPluginID: "example.evaluation", OwnerPluginVersion: "2.0.0", Model: "example-evaluation",
		Metric: "dot", Indexes: []string{"text", "image"}, QueryModalities: []string{"text"}, Role: "evaluation"},
		GenerationRole: content.SpaceEvaluation, ServingSegments: presence(0), CoverageUnknown: true}
	engine := content.SpaceCoverage{RegisteredSpace: content.RegisteredSpace{
		VectorSpace: content.VectorSpace{ID: "engine", Manifest: json.RawMessage(`{}`), Dimensions: 16},
		Name:        "engine", Model: "engine-model", Metric: "cosine", Indexes: []string{"text"}, QueryModalities: []string{"text", "image"}, Role: "evaluation"},
		GenerationRole: content.SpaceEvaluation, CoverageUnknown: true}
	unregistered := content.SpaceCoverage{RegisteredSpace: content.RegisteredSpace{VectorSpace: content.VectorSpace{ID: "unregistered"}},
		GenerationRole: content.SpaceEvaluation, CoverageUnknown: true}
	visible := !delegated || i == 1
	if visible && (i%4 == 1 || i%4 == 3) {
		served.ServingSegments = presence(1)
	}
	out := []content.SpaceCoverage{served, evaluation, engine, unregistered}
	if *served.ServingSegments == 0 && (!visible || i%4 == 0) {
		for j := range out {
			out[j].CorpusEmpty = true
		}
	}
	return out
}

// sourceLoad is a controllable source partition loader. Each call returns
// the next sequence number as the snapshot revision, after release opens.
type sourceLoad struct {
	calls   atomic.Int64
	release chan struct{}
}

func (l *sourceLoad) load(ctx context.Context, _ visibilityCorpus) (sourceSnapshot, error) {
	n := l.calls.Add(1)
	if l.release != nil {
		select {
		case <-l.release:
		case <-ctx.Done():
			return sourceSnapshot{}, ctx.Err()
		}
	}
	return sourceSnapshot{revision: n, sources: []sourceGrant{{namespace: "fixture", grant: visibility.Grant{Visibility: "public"}}}}, nil
}

// Owns the expired-key stampede bound: concurrent readers of one expired
// source partition share a single load. synctest's clock expires the entry and
// synctest.Wait proves every caller is parked before the load completes.
func TestVisibilitySnapshotLoadsAnExpiredKeyOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var s VisibilityStore
		key := visibilityCorpus{"example", "corpus"}
		prime := &sourceLoad{}
		if _, err := s.snapshotUsing(t.Context(), key, prime.load); err != nil {
			t.Fatal(err)
		}
		time.Sleep(sourceCacheTTL) // Fake time: the cached entry is now expired.
		load := &sourceLoad{release: make(chan struct{})}
		const callers = 64
		results := make([]sourceSnapshot, callers)
		errs := make([]error, callers)
		var wg sync.WaitGroup
		for i := range callers {
			wg.Go(func() { results[i], errs[i] = s.snapshotUsing(t.Context(), key, load.load) })
		}
		synctest.Wait()
		if got := load.calls.Load(); got != 1 {
			t.Errorf("%d concurrent callers on an expired key started %d loads, want 1", callers, got)
		}
		close(load.release)
		wg.Wait()
		for i := range callers {
			if errs[i] != nil || results[i].revision != 1 || len(results[i].sources) != 1 {
				t.Fatalf("caller %d: snapshot %+v, err %v; want the shared load", i, results[i], errs[i])
			}
		}
		if _, err := s.snapshotUsing(t.Context(), key, load.load); err != nil || load.calls.Load() != 1 {
			t.Fatalf("fresh entry reloaded: %d loads, err %v", load.calls.Load(), err)
		}
	})
}

// Owns the staleness guard: a grant change committed while a load is in
// flight must not let that load's result reach any caller or the cache.
func TestVisibilitySnapshotDiscardsLoadsRacingInvalidation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var s VisibilityStore
		key := visibilityCorpus{"example", "corpus"}
		load := &sourceLoad{release: make(chan struct{})}
		var before, after sourceSnapshot
		var wg sync.WaitGroup
		wg.Go(func() { before, _ = s.snapshotUsing(t.Context(), key, load.load) })
		synctest.Wait() // Load 1 started before the grant changed.
		s.invalidate(key)
		wg.Go(func() { after, _ = s.snapshotUsing(t.Context(), key, load.load) })
		synctest.Wait()
		close(load.release)
		wg.Wait()
		if before.revision < 2 || after.revision < 2 {
			t.Fatalf("callers saw loads %d and %d; a load started before invalidation must be discarded", before.revision, after.revision)
		}
		cached, err := s.snapshotUsing(t.Context(), key, load.load)
		if err != nil || cached.revision < 2 {
			t.Fatalf("cache holds load %d (err %v), want one started after invalidation", cached.revision, err)
		}
	})
}

// Owns bounded eviction: a full cache drops its least recently used entry and
// keeps hot partitions instead of flushing every Corpus at once.
func TestVisibilitySnapshotEvictsLeastRecentlyUsed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var s VisibilityStore
		load := &sourceLoad{}
		key := func(i int) visibilityCorpus { return visibilityCorpus{"example", fmt.Sprintf("corpus-%03d", i)} }
		get := func(i int) {
			t.Helper()
			if _, err := s.snapshotUsing(t.Context(), key(i), load.load); err != nil {
				t.Fatal(err)
			}
		}
		for i := range sourceCacheEntries {
			get(i)
		}
		get(0) // Hot: most recently used.
		if got := load.calls.Load(); got != sourceCacheEntries {
			t.Fatalf("filling the cache took %d loads, want %d", got, sourceCacheEntries)
		}
		get(sourceCacheEntries) // Over capacity: evicts corpus-001 only.
		for _, i := range []int{0, 2, sourceCacheEntries / 2, sourceCacheEntries - 1, sourceCacheEntries} {
			get(i)
		}
		if got := load.calls.Load(); got != sourceCacheEntries+1 {
			t.Fatalf("retained entries reloaded: %d loads, want %d", got, sourceCacheEntries+1)
		}
		get(1)
		if got := load.calls.Load(); got != sourceCacheEntries+2 {
			t.Fatalf("least recently used entry was not evicted: %d loads, want %d", got, sourceCacheEntries+2)
		}
	})
}
