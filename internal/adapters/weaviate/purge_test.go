package weaviate_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
)

// count reports how many objects in the collection match an optional filter.
func (f *attachFixture) count(collection, where string) int {
	f.t.Helper()
	args := ""
	if where != "" {
		args = "(where:" + where + ")"
	}
	body, _ := json.Marshal(map[string]any{"query": fmt.Sprintf("{Aggregate{%s%s{meta{count}}}}", collection, args)})
	res, err := http.Post(f.url+"/v1/graphql", "application/json", bytes.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Data struct {
			Aggregate map[string][]struct {
				Meta struct {
					Count int `json:"count"`
				} `json:"meta"`
			} `json:"Aggregate"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	if err = json.NewDecoder(res.Body).Decode(&out); err != nil || len(out.Errors) > 0 || len(out.Data.Aggregate[collection]) != 1 {
		f.t.Fatalf("aggregate: %v %v", err, out.Errors)
	}
	return out.Data.Aggregate[collection][0].Meta.Count
}

func textFilter(pairs ...string) string {
	operands := ""
	for i := 0; i < len(pairs); i += 2 {
		if i > 0 {
			operands += ","
		}
		operands += fmt.Sprintf(`{path:[%q],operator:Equal,valueText:%q}`, pairs[i], pairs[i+1])
	}
	return "{operator:And,operands:[" + operands + "]}"
}

// Purges delete by filter: a generation purge removes one Corpus's lexical
// and enriched objects in one generation, a Version purge removes one
// Version's objects in every generation, and nothing else is touched. Repeating
// either deletes nothing.
func TestPurgeDeletesOnlyFilteredObjects(t *testing.T) {
	f := newAttachFixture(t)
	old, next := f.gen, f.gen
	next.ID = "generation-attach-next"
	a, b := f.segmentation("segment-a", "phare du port"), f.segmentation("segment-b", "phare du quai")
	f.publish(old, a)
	f.publish(next, a)
	f.publish(old, b)
	if err := f.store.PublishEmbeddings(f.ctx, old, f.org, []content.EmbeddingData{f.embedding(old, "segment-a", unitVector(1))}); err != nil {
		t.Fatal(err)
	}
	// A neighbour Corpus and a neighbour Organization in the same generation.
	if err := f.store.Publish(f.ctx, old, f.org, "corpus-neighbour", "example-feed", content.Version{ID: "version-n"}, f.segmentation("segment-n", "phare voisin")); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Publish(f.ctx, old, "adapter-attach-other", f.corpusID, "example-feed", content.Version{ID: "version-segment-a"}, a); err != nil {
		t.Fatal(err)
	}
	collection := f.gen.Collection
	if n := f.count(collection, ""); n != 6 {
		t.Fatalf("fixture objects %d, want 6", n)
	}
	r, err := f.store.PurgeGeneration(f.ctx, collection, f.org, f.corpusID, old.ID)
	if err != nil || !r.Complete || r.Deleted != 3 {
		t.Fatalf("generation purge %+v %v, want 3 deleted (a lexical+enriched, b lexical)", r, err)
	}
	if n := f.count(collection, textFilter("organization", f.org, "corpusId", f.corpusID, "generationId", old.ID)); n != 0 {
		t.Fatalf("%d objects left in the purged generation", n)
	}
	for _, kept := range []string{
		textFilter("organization", f.org, "generationId", next.ID),
		textFilter("organization", f.org, "corpusId", "corpus-neighbour"),
		textFilter("organization", "adapter-attach-other"),
	} {
		if n := f.count(collection, kept); n != 1 {
			t.Fatalf("purge touched %s: %d objects", kept, n)
		}
	}
	if r, err = f.store.PurgeGeneration(f.ctx, collection, f.org, f.corpusID, old.ID); err != nil || !r.Complete || r.Deleted != 0 {
		t.Fatalf("repeated generation purge %+v %v", r, err)
	}
	r, err = f.store.PurgeVersion(f.ctx, collection, f.org, a.VersionID)
	if err != nil || !r.Complete || r.Deleted != 1 {
		t.Fatalf("version purge %+v %v, want the next generation's object", r, err)
	}
	if n := f.count(collection, ""); n != 2 {
		t.Fatalf("objects after purges %d, want the neighbour Corpus and Organization", n)
	}
	if r, err = f.store.PurgeVersion(f.ctx, collection, f.org, a.VersionID); err != nil || !r.Complete || r.Deleted != 0 {
		t.Fatalf("repeated version purge %+v %v", r, err)
	}
	if _, err = f.store.PurgeVersion(f.ctx, collection, "", a.VersionID); err == nil {
		t.Fatal("an unscoped version purge must be refused")
	}
	if _, err = f.store.PurgeGeneration(f.ctx, collection, f.org, "", old.ID); err == nil {
		t.Fatal("an unscoped generation purge must be refused")
	}
}

// Measurement (THE-698): dead Versions crowd the candidate window until they
// are purged. 60 Records each keep one live Version and three dead ones whose
// text matches the query more strongly; every Version is enriched, as after
// THE-690, so each has a lexical and an enriched object.
func TestDeadVersionsCrowdCandidatesUntilPurged(t *testing.T) {
	f := newAttachFixture(t)
	const records, dead = 60, 3
	live := map[string]bool{}
	deadVersions := []string{}
	seed := 0
	for r := 0; r < records; r++ {
		for v := 0; v <= dead; v++ {
			id := fmt.Sprintf("r%02d-v%d", r, v)
			text := fmt.Sprintf("phare balise cap %s", id)
			if v < dead {
				text = fmt.Sprintf("phare phare cap %s", id) // an older wording that matched more strongly
				deadVersions = append(deadVersions, "version-"+id)
			} else {
				live[id] = true
			}
			f.publish(f.gen, f.segmentation(id, text))
			seed++
			if err := f.store.PublishEmbeddings(f.ctx, f.gen, f.org, []content.EmbeddingData{f.embedding(f.gen, id, unitVector(seed))}); err != nil {
				t.Fatal(err)
			}
		}
	}
	route := []retrieval.Route{{CorpusID: f.corpusID, Generation: f.gen}}
	scope := corpus.Scope{Organization: f.org, Corpora: []string{"*"}}
	liveCandidates := func() (int, int) {
		t.Helper()
		candidates, err := f.store.Search(f.ctx, route, scope, retrieval.Request{Query: "phare", Mode: "lexical", Profile: "default", Limit: retrieval.MaxLimit, CorpusIDs: []string{f.corpusID}})
		if err != nil {
			t.Fatal(err)
		}
		distinct := map[string]bool{}
		for _, c := range candidates {
			if live[c.SegmentID] {
				distinct[c.SegmentID] = true
			}
		}
		return len(distinct), len(candidates)
	}
	objectsBefore := f.count(f.gen.Collection, "")
	liveBefore, slotsBefore := liveCandidates()
	deleted := 0
	for _, v := range deadVersions {
		r, err := f.store.PurgeVersion(f.ctx, f.gen.Collection, f.org, v)
		if err != nil || !r.Complete {
			t.Fatalf("purge %s: %+v %v", v, r, err)
		}
		deleted += r.Deleted
	}
	objectsAfter := f.count(f.gen.Collection, "")
	liveAfter, slotsAfter := liveCandidates()
	t.Logf("THE-698 measurement: objects %d -> %d (%d deleted); distinct live Records in %d candidate slots: %d/%d before, %d/%d after (candidates returned %d -> %d)",
		objectsBefore, objectsAfter, deleted, retrieval.CandidateLimit, liveBefore, records, liveAfter, records, slotsBefore, slotsAfter)
	if objectsBefore != records*(dead+1)*2 || objectsAfter != records*2 || deleted != records*dead*2 {
		t.Fatalf("object counts %d -> %d, deleted %d", objectsBefore, objectsAfter, deleted)
	}
	if liveBefore >= records || liveAfter != records {
		t.Fatalf("live Records within the candidate window: %d before, %d after, want crowding then all %d", liveBefore, liveAfter, records)
	}
}
