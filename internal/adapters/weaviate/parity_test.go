package weaviate_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
)

// Steady-state results over lexical anchors plus enriched objects, deduplicated
// by segment as retrieval does, rank exactly like one object per segment that
// carries both text and vector.
func TestAnchoredProjectionRanksLikeSingleObjectProjection(t *testing.T) {
	f := newAttachFixture(t)
	single := newAttachFixture(t)
	texts := []string{
		"la lanterne rouge du port de peche",
		"une lanterne dans la nuit noire",
		"le phare guide les bateaux vers le port",
		"les bateaux rentrent au port avant la tempete",
		"une tempete annoncee sur la cote",
		"la cote sauvage et ses falaises",
		"lanterne magique et projections anciennes",
		"le marche aux poissons du port ouvre tot",
	}
	for i, text := range texts {
		id := "segment-rank-" + strconv.Itoa(i)
		seg := f.segmentation(id, text)
		f.publish(f.gen, seg)
		single.publish(single.gen, seg)
		vector := unitVector(100 + i)
		if err := f.store.PublishEmbeddings(f.ctx, f.gen, f.org, []content.EmbeddingData{f.embedding(f.gen, id, vector)}); err != nil {
			t.Fatal(err)
		}
		// The single-object baseline carries the vector on the lexical object itself.
		single.patchVector(id, vector)
	}
	ranked := func(x *attachFixture, mode, query string, vector []float32) []string {
		t.Helper()
		q := retrieval.Request{Query: query, Mode: mode, Profile: "default", Limit: 10, CorpusIDs: []string{x.corpusID}, Vector: vector}
		candidates, err := x.store.Search(x.ctx, []retrieval.Route{{CorpusID: x.corpusID, Generation: x.gen}}, corpus.Scope{Organization: x.org, Corpora: []string{"*"}}, q)
		if err != nil {
			t.Fatal(err)
		}
		out, seen := []string{}, map[string]bool{}
		for _, c := range candidates {
			if !seen[c.SegmentID] {
				seen[c.SegmentID] = true
				out = append(out, c.SegmentID)
			}
		}
		return out
	}
	for _, query := range []string{"lanterne", "port", "tempete cote", "bateaux port"} {
		for _, mode := range []string{"lexical", "semantic", "hybrid"} {
			vector := unitVector(103)
			got, want := ranked(f, mode, query, vector), ranked(single, mode, query, vector)
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("%s %q ranks %v, single-object baseline ranks %v", mode, query, got, want)
			}
		}
	}
}

// patchVector attaches a vector in place, as the single-object projection did.
func (f *attachFixture) patchVector(segmentID string, vector []float32) {
	f.t.Helper()
	objects := f.objects(f.gen, segmentID)
	if len(objects) != 1 {
		f.t.Fatalf("baseline segment %s has %d objects", segmentID, len(objects))
	}
	body, _ := json.Marshal(map[string]any{"class": f.gen.Collection, "vectors": map[string]any{"semantic_text_v1": vector}})
	req, _ := http.NewRequestWithContext(f.ctx, http.MethodPatch, f.url+"/v1/objects/"+f.gen.Collection+"/"+objects[0].ID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode/100 != 2 {
		f.t.Fatalf("baseline vector attach status %d", res.StatusCode)
	}
}

// A rebuild covers enriched Versions into the target generation with their
// lexical anchor plus one enriched object, and not-yet-enriched Versions with the
// anchor only. Re-covering converges on the same shape; the prior generation is
// untouched.
func TestRebuildCoverShapePerSegment(t *testing.T) {
	f := newAttachFixture(t)
	target := f.gen
	target.ID = "generation-attach-target"
	enriched, lexicalOnly := f.segmentation("segment-cover-enriched", "une lanterne couverte"), f.segmentation("segment-cover-lexical", "une lanterne sans vecteur")
	f.publish(f.gen, enriched)
	before := f.objects(f.gen, enriched.Segments[0].ID)
	data := []content.EmbeddingData{f.embedding(target, enriched.Segments[0].ID, unitVector(21))}
	for attempt := 0; attempt < 2; attempt++ {
		f.publish(target, enriched)
		if err := f.store.PublishEmbeddings(f.ctx, target, f.org, data); err != nil {
			t.Fatal(err)
		}
		f.publish(target, lexicalOnly)
	}
	if got := fmt.Sprint(shape(f.objects(target, enriched.Segments[0].ID))); got != "1 1" {
		t.Fatalf("enriched Version covered as %s lexical/enriched objects, want 1 1", got)
	}
	if got := fmt.Sprint(shape(f.objects(target, lexicalOnly.Segments[0].ID))); got != "1 0" {
		t.Fatalf("lexical-only Version covered as %s lexical/enriched objects, want 1 0", got)
	}
	if after := f.objects(f.gen, enriched.Segments[0].ID); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("covering the target changed the prior generation: %+v then %+v", before, after)
	}
}
