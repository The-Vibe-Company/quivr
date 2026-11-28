package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

func TestNewsroomPackingDefaultsBitExact(t *testing.T) {
	base := testConfig("openai", "http://127.0.0.1:9")
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"overlap_tokens", "lead_max_tokens", "include_title_in_lead", "contextual_anchor", "normalize_wire_text", "dual_lead_segment"} {
		if strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("default config serialized opt-in field %q: %s", key, raw)
		}
	}
	other := base
	other.LeadMaxTokens = 180
	if other.spaceID() == base.spaceID() {
		t.Fatal("opt-in lead_max_tokens must produce a distinct vector space ID")
	}
}

func TestNormalizeWireText(t *testing.T) {
	in := "PARIS (Agency) - ## *Major summit opens in Geneva*\n\nSee [official report](https://example.org/doc) and https://example.org/raw.\n\nab/cd"
	got := normalizeWireText(in)
	for _, bad := range []string{"PARIS (Agency)", "##", "*", "https://example.org", "ab/cd"} {
		if strings.Contains(got, bad) {
			t.Fatalf("expected %q stripped from %q", bad, got)
		}
	}
	for _, good := range []string{"Major summit opens in Geneva", "See official report"} {
		if !strings.Contains(got, good) {
			t.Fatalf("expected %q preserved in %q", good, got)
		}
	}
	unicodeIn := "ČESKÝ KRUMLOV (Wire) — Regional festival opens today."
	unicodeGot := normalizeWireText(unicodeIn)
	if strings.Contains(unicodeGot, "ČESKÝ KRUMLOV") || unicodeGot != "Regional festival opens today." {
		t.Fatalf("expected Unicode uppercase dateline stripped, got %q", unicodeGot)
	}
}

func TestLeadIsolationAndContextualAnchors(t *testing.T) {
	c := testConfig("openai", "http://127.0.0.1:9")
	c.DocumentTemplate = "title: {title} | text: {text}"
	c.MaxTokens = 1024
	c.BodyTokens = 160
	c.LeadMaxTokens = 80
	c.IncludeTitleInLead = true
	c.ContextualAnchor = "title_lead_slot"
	c.NormalizeWireText = true

	parts := []quivrplugin.IngestPart{
		{Key: "title", Role: "title", Text: "European Rail Accord Signed"},
		{Key: "body", Role: "body", Text: "BRUSSELS (Wire) - ## Ministers finalized the cross-border rail treaty on Tuesday.\n\nThe agreement harmonizes freight corridors across twelve member states with new signaling standards.\n\nIndustry groups welcomed the timetable while regional operators requested transitional funding for depot upgrades.\n\nOfficials confirmed ratification votes will begin next quarter in national parliaments."},
	}
	segs, inputs, err := c.packedSegments(context.Background(), parts, byteCounter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) < 3 {
		t.Fatalf("expected >=3 segments with lead isolation, got %d", len(segs))
	}
	// Segment 0 must carry title + body lead in SourceRanges.
	if segs[0].PartKey != "title" || len(segs[0].SourceRanges) != 2 || segs[0].SourceRanges[0].PartKey != "title" || segs[0].SourceRanges[1].PartKey != "body" || segs[0].SourceRanges[1].Start != 0 {
		t.Fatalf("unexpected lead segment source ranges: %+v", segs[0])
	}
	// Deep chunks must carry Title — LeadSentence in the title slot and clean wire text.
	if !strings.Contains(inputs[1], "title: European Rail Accord Signed — Ministers finalized the cross-border rail treaty on Tuesday.") {
		t.Fatalf("deep chunk missing title_lead_slot anchor: %q", inputs[1])
	}
	if strings.Contains(inputs[0], "BRUSSELS (Wire)") || strings.Contains(inputs[0], "##") {
		t.Fatalf("lead chunk input was not normalized: %q", inputs[0])
	}
}

func TestOverlapAndDualLeadSegment(t *testing.T) {
	c := testConfig("openai", "http://127.0.0.1:9")
	c.DocumentTemplate = "title: {title} | text: {text}"
	c.MaxTokens = 1024
	c.BodyTokens = 140
	c.OverlapTokens = 45
	c.DualLeadSegment = true

	parts := []quivrplugin.IngestPart{
		{Key: "title", Role: "title", Text: "Harbor Expansion Plan"},
		{Key: "body", Role: "body", Text: "First paragraph introduces the new northern terminal project and budget.\n\nSecond paragraph describes dredging schedules and environmental permits.\n\nThird paragraph outlines rail connections to inland logistics hubs.\n\nFourth paragraph details labor negotiations with dockworker unions."},
	}
	segs, _, err := c.packedSegments(context.Background(), parts, byteCounter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) < 3 {
		t.Fatalf("expected >=3 segments (dual lead + >=2 overlapping body), got %d", len(segs))
	}
	if segs[0].PartKey != "title" || segs[0].Provenance["packing"] != "lead_summary" {
		t.Fatalf("expected segment 0 to be dual lead summary, got %+v", segs[0])
	}
	// Body chunk 1 must start before body chunk 0 ends (overlap).
	if segs[2].Start >= segs[1].End {
		t.Fatalf("expected overlap between body chunks: seg1=[%d,%d) seg2=[%d,%d)", segs[1].Start, segs[1].End, segs[2].Start, segs[2].End)
	}
}

func TestPagedIngestionCoversContiguouslyAndDisablesLeadLimit(t *testing.T) {
	c := testConfig("openai", "http://127.0.0.1:9")
	c.DocumentTemplate = "title: {title} | text: {text}"
	c.MaxTokens = 1024
	c.BodyTokens = 100
	c.OverlapTokens = 30
	c.LeadMaxTokens = 40
	c.ContextualAnchor = "title_lead_slot"
	c.IncludeTitleInLead = true
	c.DualLeadSegment = true

	ing := newIngester(c, "", nil)
	body := "First paragraph introduces the new northern terminal project and budget.\n\nSecond paragraph describes dredging schedules and environmental permits.\n\nThird paragraph outlines rail connections to inland logistics hubs."
	page, err := ing.SegmentAndEmbedPage(context.Background(), &quivrplugin.IngestRequest{
		Parts: []quivrplugin.IngestPart{{Key: "body", Role: "body", Text: body}},
		Page:  &quivrplugin.IngestPageRequest{Start: 0, MaxSegments: 16},
	})
	if err != nil {
		t.Fatalf("paged ingestion failed with overlap/lead enabled: %v", err)
	}
	if len(page.Segments) < 2 {
		t.Fatalf("expected >= 2 paged segments, got %d", len(page.Segments))
	}
	if got := page.Segments[0].End - page.Segments[0].Start; got <= c.LeadMaxTokens {
		t.Fatalf("expected LeadMaxTokens (%d) to be disabled on paged windows, got first segment len %d", c.LeadMaxTokens, got)
	}
	for i := 1; i < len(page.Segments); i++ {
		if page.Segments[i].Start != page.Segments[i-1].End {
			t.Fatalf("paged segments must cover code points contiguously without overlap: seg[%d]=[%d,%d) seg[%d]=[%d,%d)",
				i-1, page.Segments[i-1].Start, page.Segments[i-1].End, i, page.Segments[i].Start, page.Segments[i].End)
		}
	}
}
