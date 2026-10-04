package weaviate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/weaviate"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
)

// attachFixture is one collection with lexical segments projected the way
// promotion projects them, before any embedding is attached.
type attachFixture struct {
	t        *testing.T
	ctx      context.Context
	url      string
	store    *weaviate.Store
	gen      content.Generation
	org      string
	corpusID string
}

func newAttachFixture(t *testing.T) *attachFixture {
	t.Helper()
	path := os.Getenv("QUIVR_ADAPTER_CONFIG")
	if path == "" {
		t.Skip("make verify real adapters")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		WeaviateURL string `json:"weaviate_url"`
	}
	if err = json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	f := &attachFixture{t: t, ctx: ctx, url: strings.TrimRight(cfg.WeaviateURL, "/"), store: weaviate.New(cfg.WeaviateURL), org: "adapter-attach", corpusID: "corpus-attach"}
	collection := "QuivrAttach" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err = f.store.Bootstrap(ctx, collection); err != nil {
		t.Fatal(err)
	}
	f.gen = content.Generation{ID: "generation-attach", Collection: collection, ProfileVersion: retrieval.ProfileVersion, SpaceID: "space-attach"}
	return f
}

func (f *attachFixture) segmentation(id, text string) content.Segmentation {
	return content.Segmentation{ID: "seg-" + id, VersionID: "version-" + id, Segments: []content.Segment{{ID: id, PartKey: "body", Text: text}}}
}

func (f *attachFixture) publish(g content.Generation, seg content.Segmentation) {
	f.t.Helper()
	if err := f.store.Publish(f.ctx, g, f.org, f.corpusID, "example-feed", content.Version{ID: seg.VersionID}, seg); err != nil {
		f.t.Fatal(err)
	}
}

// unitVector returns a distinct normalized vector per seed.
func unitVector(seed int) []float32 {
	v := make([]float32, 384)
	n := 0.0
	for i := range v {
		v[i] = float32(math.Sin(float64(seed*7+i) + 0.5))
		n += float64(v[i]) * float64(v[i])
	}
	for i := range v {
		v[i] = float32(float64(v[i]) / math.Sqrt(n))
	}
	return v
}

func (f *attachFixture) embedding(g content.Generation, segmentID string, vector []float32) content.EmbeddingData {
	raw, err := content.VectorBytes(vector)
	if err != nil {
		f.t.Fatal(err)
	}
	return content.EmbeddingData{Artifact: content.Embedding{Organization: f.org, SpaceID: g.SpaceID, SegmentID: segmentID, Payload: content.Blob{SHA256: content.Hash(raw)}}, Vector: vector}
}

type storedObject struct {
	ID      string
	Updated string
	Vector  bool
}

// objects lists every physical object projected for a segment in a generation.
func (f *attachFixture) objects(g content.Generation, segmentID string) []storedObject {
	f.t.Helper()
	where := fmt.Sprintf(`{operator:And,operands:[{path:["organization"],operator:Equal,valueText:%q},{path:["generationId"],operator:Equal,valueText:%q},{path:["segmentId"],operator:Equal,valueText:%q}]}`, f.org, g.ID, segmentID)
	query := fmt.Sprintf("{Get{%s(where:%s,limit:20){segmentId _additional{id lastUpdateTimeUnix vectors{semantic_text_v1}}}}}", g.Collection, where)
	body, _ := json.Marshal(map[string]any{"query": query})
	res, err := http.Post(f.url+"/v1/graphql", "application/json", bytes.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Data struct {
			Get map[string][]struct {
				Additional struct {
					ID      string               `json:"id"`
					Updated string               `json:"lastUpdateTimeUnix"`
					Vectors map[string][]float32 `json:"vectors"`
				} `json:"_additional"`
			} `json:"Get"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	if err = json.NewDecoder(res.Body).Decode(&out); err != nil || len(out.Errors) > 0 {
		f.t.Fatalf("list objects: %v %v", err, out.Errors)
	}
	list := []storedObject{}
	for _, o := range out.Data.Get[g.Collection] {
		list = append(list, storedObject{ID: o.Additional.ID, Updated: o.Additional.Updated, Vector: len(o.Additional.Vectors["semantic_text_v1"]) > 0})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}

// shape counts a segment's lexical and enriched objects.
func shape(objects []storedObject) (lexical, enriched int) {
	for _, o := range objects {
		if o.Vector {
			enriched++
		} else {
			lexical++
		}
	}
	return lexical, enriched
}

func lexicalObject(objects []storedObject) storedObject {
	for _, o := range objects {
		if !o.Vector {
			return o
		}
	}
	return storedObject{}
}

func (f *attachFixture) search(mode string, vector []float32) map[string]int {
	f.t.Helper()
	q := retrieval.Request{Query: "lanterne", Mode: mode, Profile: "default", Limit: 10, CorpusIDs: []string{f.corpusID}, Vector: vector}
	candidates, err := f.store.Search(f.ctx, []retrieval.Route{{CorpusID: f.corpusID, Generation: f.gen}}, corpus.Scope{Organization: f.org, Corpora: []string{"*"}}, q)
	if err != nil {
		f.t.Fatal(err)
	}
	seen := map[string]int{}
	for _, c := range candidates {
		seen[c.SegmentID]++
	}
	return seen
}

// Attaching a vector must never hide an already searchable segment from
// lexical or hybrid search. Weaviate re-indexes an updated object under a new
// document id, so an in-place update leaves a window where BM25 misses it.
func TestEmbeddingAttachmentKeepsSegmentsLexicallyVisible(t *testing.T) {
	f := newAttachFixture(t)
	segments := []string{"segment-lantern-a", "segment-lantern-b"}
	f.publish(f.gen, f.segmentation(segments[0], "la lanterne rouge du port"))
	f.publish(f.gen, f.segmentation(segments[1], "une lanterne dans la nuit"))
	for i := 0; i < 20; i++ {
		f.publish(f.gen, f.segmentation("segment-filler-"+strconv.Itoa(i), "texte de remplissage numero "+strconv.Itoa(i)))
	}
	anchors := map[string]storedObject{}
	for _, id := range segments {
		anchors[id] = lexicalObject(f.objects(f.gen, id))
	}
	// Re-attachment is bounded by count, not by time, and exercises the same
	// write as the first attachment each time.
	const attachments = 60
	var wg sync.WaitGroup
	done := make(chan struct{})
	errs := make(chan error, len(segments))
	for i, id := range segments {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			for n := 1; n <= attachments; n++ {
				if err := f.store.PublishEmbeddings(f.ctx, f.gen, f.org, []content.EmbeddingData{f.embedding(f.gen, id, unitVector(i*1000+n))}); err != nil {
					errs <- err
					return
				}
			}
		}(i, id)
	}
	go func() { wg.Wait(); close(done) }()
	queries, missing := 0, map[string]int{}
	for running := true; running; {
		select {
		case <-done:
			running = false
		default:
		}
		for _, mode := range []string{"lexical", "hybrid"} {
			seen := f.search(mode, unitVector(3))
			queries++
			for _, id := range segments {
				if seen[id] == 0 {
					missing[mode]++
				}
			}
		}
	}
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	t.Logf("queries=%d missing=%v", queries, missing)
	if len(missing) > 0 {
		t.Fatalf("searchable segments went missing during embedding attachment: %v out of %d queries", missing, queries)
	}
	for i, id := range segments {
		objects := f.objects(f.gen, id)
		if lexical, enriched := shape(objects); lexical != 1 || enriched != 1 {
			t.Fatalf("segment %s projected as %+v, want its lexical object plus one enriched object", id, objects)
		}
		if anchor := lexicalObject(objects); anchor != anchors[id] {
			t.Fatalf("segment %s lexical object changed from %+v to %+v", id, anchors[id], anchor)
		}
		// The surviving object carries the last attached vector.
		if seen := f.search("semantic", unitVector(i*1000+attachments)); seen[id] == 0 {
			t.Fatalf("semantic search with the last vector did not find %s", id)
		}
	}
}

// failingDeletes drops the cleanup request, as a crash between inserting the
// enriched object and removing the stale one would.
type failingDeletes struct{ next http.RoundTripper }

func (f failingDeletes) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodDelete {
		return nil, errors.New("injected crash before cleanup")
	}
	return f.next.RoundTrip(r)
}

func TestEmbeddingAttachmentConvergesAfterInterruptedSwap(t *testing.T) {
	f := newAttachFixture(t)
	id := "segment-interrupted"
	f.publish(f.gen, f.segmentation(id, "une lanterne interrompue"))
	if err := f.store.PublishEmbeddings(f.ctx, f.gen, f.org, []content.EmbeddingData{f.embedding(f.gen, id, unitVector(41))}); err != nil {
		t.Fatal(err)
	}
	data := []content.EmbeddingData{f.embedding(f.gen, id, unitVector(42))}
	crashing := weaviate.New(f.url)
	crashing.Client = &http.Client{Timeout: 4 * time.Second, Transport: failingDeletes{next: http.DefaultTransport}}
	if err := crashing.PublishEmbeddings(f.ctx, f.gen, f.org, data); err == nil {
		t.Fatal("attachment must report the interrupted cleanup")
	}
	if seen := f.search("lexical", nil); seen[id] == 0 {
		t.Fatalf("interrupted attachment hid the segment: %v", seen)
	}
	// The retry finds the enriched object already written and still cleans up.
	if err := f.store.PublishEmbeddings(f.ctx, f.gen, f.org, data); err != nil {
		t.Fatal(err)
	}
	objects := f.objects(f.gen, id)
	if lexical, enriched := shape(objects); lexical != 1 || enriched != 1 {
		t.Fatalf("retry left %+v, want the lexical object plus one enriched object", objects)
	}
	// Repeating a completed attachment writes nothing.
	if err := f.store.PublishEmbeddings(f.ctx, f.gen, f.org, data); err != nil {
		t.Fatal(err)
	}
	if again := f.objects(f.gen, id); fmt.Sprint(again) != fmt.Sprint(objects) {
		t.Fatalf("idempotent attachment rewrote the object: %+v then %+v", objects, again)
	}
	// Re-publishing the lexical projection after enrichment writes nothing either.
	f.publish(f.gen, f.segmentation(id, "une lanterne interrompue"))
	if again := f.objects(f.gen, id); fmt.Sprint(again) != fmt.Sprint(objects) {
		t.Fatalf("lexical republication touched the enriched object: %+v then %+v", objects, again)
	}
}

func TestEmbeddingAttachmentRequiresProjectedSegment(t *testing.T) {
	f := newAttachFixture(t)
	id := "segment-never-projected"
	err := f.store.PublishEmbeddings(f.ctx, f.gen, f.org, []content.EmbeddingData{f.embedding(f.gen, id, unitVector(7))})
	if !errors.Is(err, retrieval.ErrProjectionMissing) {
		t.Fatalf("attachment to an unprojected segment = %v, want ErrProjectionMissing", err)
	}
	if objects := f.objects(f.gen, id); len(objects) != 0 {
		t.Fatalf("attachment created %+v for an unprojected segment", objects)
	}
}

// Enriched object identities are scoped to their generation and segment, so a
// swap never removes another generation's or another Version's objects.
func TestEmbeddingAttachmentIsScopedToGenerationAndSegment(t *testing.T) {
	f := newAttachFixture(t)
	other := f.gen
	other.ID = "generation-attach-next"
	current, correction := f.segmentation("segment-v1", "une lanterne ancienne"), f.segmentation("segment-v2", "une lanterne corrigee")
	f.publish(f.gen, current)
	f.publish(f.gen, correction)
	f.publish(other, current)
	before := f.objects(f.gen, correction.Segments[0].ID)
	vector := unitVector(9)
	if err := f.store.PublishEmbeddings(f.ctx, other, f.org, []content.EmbeddingData{f.embedding(other, current.Segments[0].ID, vector)}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.PublishEmbeddings(f.ctx, f.gen, f.org, []content.EmbeddingData{f.embedding(f.gen, current.Segments[0].ID, vector)}); err != nil {
		t.Fatal(err)
	}
	a, b := f.objects(f.gen, current.Segments[0].ID), f.objects(other, current.Segments[0].ID)
	la, ea := shape(a)
	lb, eb := shape(b)
	if la != 1 || ea != 1 || lb != 1 || eb != 1 {
		t.Fatalf("each generation must hold the lexical object plus one enriched object: %+v %+v", a, b)
	}
	ids := map[string]bool{}
	for _, o := range append(a, b...) {
		ids[o.ID] = true
	}
	if len(ids) != 4 {
		t.Fatalf("generations share an object identity: %+v %+v", a, b)
	}
	if objects := f.objects(f.gen, correction.Segments[0].ID); fmt.Sprint(objects) != fmt.Sprint(before) {
		t.Fatalf("another Version's lexical object changed: %+v", objects)
	}
}

// Overlapping attachments of the same embedding never delete each other's
// enriched object, and both converge on it. Weaviate indexes an object's id
// after its other properties, so while the first attachment creates the
// enriched object, a filter on the segment matches it but cannot exclude it
// by id yet. The second attachment runs inside that window; a long body keeps
// the window open, and a segment whose properties were indexed in another
// order is retried with the next one.
func TestConcurrentIdenticalAttachmentsConverge(t *testing.T) {
	f := newAttachFixture(t)
	words := make([]string, 20000)
	for i := range words {
		words[i] = "lanterne" + strconv.Itoa(i)
	}
	text := strings.Join(words, " ")
	for n := 0; n < 40; n++ {
		id := "segment-concurrent-" + strconv.Itoa(n)
		f.publish(f.gen, f.segmentation(id, text))
		data := []content.EmbeddingData{f.embedding(f.gen, id, unitVector(11+n))}
		first := make(chan error, 1)
		go func() { first <- f.store.PublishEmbeddings(f.ctx, f.gen, f.org, data) }()
		overlapped, err := f.awaitUnindexedIdentity(id, first)
		if overlapped {
			second := f.store.PublishEmbeddings(f.ctx, f.gen, f.org, data)
			if err = <-first; err == nil {
				err = second
			}
		}
		if err != nil {
			t.Fatalf("attachment %s: %v", id, err)
		}
		if objects := f.objects(f.gen, id); fmt.Sprint(shape(objects)) != "1 1" {
			t.Fatalf("attachment %s left %+v, want the lexical object plus one enriched object", id, objects)
		}
		if overlapped {
			return
		}
	}
	t.Fatal("no attachment overlapped the indexing of its enriched object")
}

// awaitUnindexedIdentity polls until the segment's enriched object matches a
// filter that excludes its own id, or the attachment creating it returns.
func (f *attachFixture) awaitUnindexedIdentity(segmentID string, done chan error) (bool, error) {
	f.t.Helper()
	for {
		select {
		case err := <-done:
			return false, err
		default:
		}
		for _, o := range f.objects(f.gen, segmentID) {
			if o.Vector && f.matchesExcluding(segmentID, o.ID) {
				return true, nil
			}
		}
	}
}

// matchesExcluding reports whether a filter on the segment that excludes id
// still returns id.
func (f *attachFixture) matchesExcluding(segmentID, id string) bool {
	f.t.Helper()
	query := fmt.Sprintf(`{Get{%s(where:{operator:And,operands:[{path:["segmentId"],operator:Equal,valueText:%q},{path:["id"],operator:NotEqual,valueText:%q}]},limit:20){_additional{id}}}}`, f.gen.Collection, segmentID, id)
	body, _ := json.Marshal(map[string]any{"query": query})
	res, err := http.Post(f.url+"/v1/graphql", "application/json", bytes.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Data struct {
			Get map[string][]struct {
				Additional struct {
					ID string `json:"id"`
				} `json:"_additional"`
			} `json:"Get"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	if err = json.NewDecoder(res.Body).Decode(&out); err != nil || len(out.Errors) > 0 {
		f.t.Fatalf("filter objects: %v %v", err, out.Errors)
	}
	for _, o := range out.Data.Get[f.gen.Collection] {
		if o.Additional.ID == id {
			return true
		}
	}
	return false
}
