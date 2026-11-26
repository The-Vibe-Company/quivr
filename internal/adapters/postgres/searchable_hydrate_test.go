package postgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

// Owns the single-round-trip SQL merge and corpus scope fencing in Hydrate:
// packed source_ranges resolve inside hydrateSQL without a second statement,
// reuse the primary part's blob when part_key matches, and preserve ErrConflict
// on missing source parts.
func TestHydrateSQLMergesPackedSourcesAndScopeFence(t *testing.T) {
	for _, fragment := range []string{
		"WHERE $6::boolean OR corpus_id=ANY($7::text[])",
		"AND ($6::boolean OR r.corpus_id=ANY($7::text[]))",
		"jsonb_typeof(c.derivation->'source_ranges')='array'",
		"ORDER BY sr.ordinality",
		"WHERE sr.part_key<>c.part_key AND (vp.organization,vp.version_id,vp.part_key)>=(c.organization,sg.version_id,sr.part_key)",
		"WHERE sr.part_key<>c.part_key AND sp.blob_id IS NOT NULL AND (cb.organization,cb.blob_id)>=(c.organization,sp.blob_id)",
		"WHERE sr.part_key=c.part_key OR sb.blob_id IS NOT NULL",
	} {
		if !strings.Contains(hydrateSQL, fragment) {
			t.Fatalf("hydrateSQL missing expected clause %q", fragment)
		}
	}

	// hydrationArgs binds scope wildcard vs explicit corpus allowlist cleanly.
	cs := []content.Candidate{{SegmentID: "s1", GenerationID: "g1"}}
	allArgs := hydrationArgs(corpus.Scope{Organization: "org", Corpora: []string{"*"}}, cs)
	if len(allArgs) != 7 || allArgs[5] != true || len(allArgs[6].([]string)) != 0 {
		t.Fatalf("wildcard args = %#v; want allCorpora=true, empty corpora slice", allArgs)
	}
	scopedArgs := hydrationArgs(corpus.Scope{Organization: "org", Corpora: []string{"c1", "c2"}}, cs)
	if len(scopedArgs) != 7 || scopedArgs[5] != false || len(scopedArgs[6].([]string)) != 2 {
		t.Fatalf("scoped args = %#v; want allCorpora=false, 2 corpora", scopedArgs)
	}
	nilArgs := hydrationArgs(corpus.Scope{Organization: "org"}, cs)
	if nilArgs[5] != false || nilArgs[6].([]string) == nil {
		t.Fatalf("nil corpora args = %#v; want non-nil empty slice", nilArgs)
	}

	// decodeHydratedSources preserves ordering and rejects missing parts.
	if got, err := decodeHydratedSources(0, nil); err != nil || got != nil {
		t.Fatalf("non-packed decode = %v, %v; want nil, nil", got, err)
	}
	raw := []byte(`[{"part_key":"p1","start":0,"end":4,"key":"k1","sha256":"h1","size":10},{"part_key":"p2","start":2,"end":8,"key":"k2","sha256":"h2","size":20}]`)
	got, err := decodeHydratedSources(2, raw)
	if err != nil || len(got) != 2 || got[0].Range.PartKey != "p1" || got[0].Blob.Key != "k1" || got[1].Range.PartKey != "p2" || got[1].Blob.Size != 20 {
		t.Fatalf("packed decode = %+v, %v", got, err)
	}
	if _, err = decodeHydratedSources(3, raw); !errors.Is(err, content.ErrConflict) {
		t.Fatalf("missing source part err = %v; want ErrConflict", err)
	}
	if _, err = decodeHydratedSources(1, nil); !errors.Is(err, content.ErrConflict) {
		t.Fatalf("null sources on packed segment err = %v; want ErrConflict", err)
	}
}
