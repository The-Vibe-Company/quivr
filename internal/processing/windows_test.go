package processing_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/tokenizer"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/processing"
)

func processor(t *testing.T) processing.TokenWindows {
	t.Helper()
	path := os.Getenv("QUIVR_ADAPTER_CONFIG")
	if path == "" {
		t.Skip("pinned tokenizer tests run in make verify")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Tokenizer tokenizer.Config `json:"tokenizer"`
	}
	if err = json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	// The persistent tokenizer production runs; TestServerMatchesPinnedReference holds it
	// to the one-shot reference helper, which costs a process start per call.
	server := &tokenizer.Server{Config: cfg.Tokenizer}
	t.Cleanup(server.Close)
	return processing.TokenWindows{Tokenizer: server}
}
func input(body, title string) processing.Input {
	parts := []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: body}}}
	if title != "" {
		parts = append(parts, content.Part{Key: "title", Role: "title", Content: content.Text{Kind: "text", Text: title}})
	}
	return processing.Input{Organization: "org-test", Version: content.Version{ID: "version-test", Manifest: content.Manifest{Kind: "manifest", Parts: parts}}}
}
func TestTokenWindowsExactLimits(t *testing.T) {
	p := processor(t)
	ctx := context.Background()
	for _, count := range []int{384, 385} {
		text := strings.TrimSuffix(strings.Repeat("Paris ", count), " ")
		r, err := p.Process(ctx, input(text, ""))
		if err != nil {
			t.Fatal(err)
		}
		if count == 384 {
			if len(r.Segments) != 1 || r.Segments[0].Start != 0 || r.Segments[0].End != 2303 || r.Segments[0].Derivation.TokenEnd != 384 {
				t.Fatal("384-token boundary", r.Segments)
			}
		} else {
			if len(r.Segments) != 2 || r.Segments[0].End != 2304 || r.Segments[1].Start != 2016 || r.Segments[1].End != 2309 || r.Segments[1].Derivation.Overlap != 48 {
				t.Fatal("385-token overlap", r.Segments)
			}
		}
		again, err := p.Process(ctx, input(text, ""))
		if err != nil || !reflect.DeepEqual(r, again) {
			t.Fatal("same derivation inputs diverged", err)
		}
	}
}
func TestTokenWindowsPreferParagraphAndPreserveUnicode(t *testing.T) {
	p := processor(t)
	text := strings.TrimSuffix(strings.Repeat("Paris ", 300), " ") + "\n\n" + strings.TrimSuffix(strings.Repeat("Paris ", 200), " ") + " 🌞 e\u0301\r\n"
	r, err := p.Process(context.Background(), input(text, ""))
	if err != nil {
		t.Fatal(err)
	}
	if r.Segments[0].End != 1801 || r.Segments[0].Derivation.TokenEnd != 300 || r.Segments[0].Derivation.HardEnd {
		t.Fatal("paragraph preference lost", r.Segments[0])
	}
	if r.Segments[len(r.Segments)-1].End != len([]rune(text)) {
		t.Fatal("source tail truncated")
	}
	for _, s := range r.Segments {
		d := s.Derivation
		if string([]rune(text)[s.Start:s.End]) != s.Text || text[d.UTF8Start:d.UTF8End] != s.Text {
			t.Fatal("Unicode/UTF8 coordinates diverged")
		}
		if d.TokenEnd-d.TokenStart > 384 || d.ModelTokens > 512 {
			t.Fatal("token budget exceeded")
		}
	}
}
func TestTokenWindowsTitleViewAndHardCuts(t *testing.T) {
	p := processor(t)
	ctx := context.Background()
	title := " \n" + strings.TrimSuffix(strings.Repeat("Paris ", 65), " ") + "\n "
	body := strings.Repeat("x", 5000)
	r, err := p.Process(ctx, input(body, title))
	if err != nil {
		t.Fatal(err)
	}
	expected := strings.TrimSuffix(strings.Repeat("Paris ", 64), " ")
	if len(r.Segments) < 3 {
		t.Fatal("hard cut fixture not split")
	}
	for i, s := range r.Segments {
		d := s.Derivation
		if s.Title != title || !d.TitleTruncated || d.TitleTokens != 64 || !strings.HasPrefix(d.ModelInput, "passage: "+expected+"\n\n") {
			t.Fatal("title cap changed canonical title or template", s)
		}
		if i > 0 && (!d.HardStart || d.Overlap != 56) {
			t.Fatal("hard start/backtrack not recorded", d)
		}
		if i < len(r.Segments)-1 && !d.HardEnd {
			t.Fatal("hard end not recorded")
		}
		if s.Text != body[s.Start:s.End] {
			t.Fatal("title shifted body offsets")
		}
	}
	empty, err := p.Process(ctx, input("", "Titre 🌞"))
	if err != nil || len(empty.Segments) != 1 || empty.Segments[0].Start != 0 || empty.Segments[0].End != 0 || empty.Segments[0].Derivation.ModelInput != "passage: Titre 🌞" {
		t.Fatal("title-only body coordinates", empty, err)
	}
}
func TestTokenWindowsRejectWithoutTruncation(t *testing.T) {
	p := processor(t)
	ctx := context.Background()
	for _, text := range []string{strings.Repeat("x", 262145), strings.Repeat(" ", 4097), "invalid\x00source"} {
		if _, err := p.Process(ctx, input(text, "")); !errors.Is(err, processing.ErrUnsupported) {
			t.Fatal("technical limit not explicit", err)
		}
	}
	query := strings.TrimSuffix(strings.Repeat("Paris ", 256), " ")
	normalized, err := p.NormalizeQuery(ctx, " \r\n"+query+"\r ")
	if err != nil || normalized != query {
		t.Fatal("query boundary or normalization", err)
	}
	if _, err = p.NormalizeQuery(ctx, query+" Paris"); !errors.Is(err, content.ErrInvalid) {
		t.Fatal("257-token query truncated", err)
	}
}

func TestTitleCapDoesNotSplitSharedSourceOffsets(t *testing.T) {
	p := processor(t)
	ctx := context.Background()
	title := strings.Repeat("Paris ", 63) + "星 fin"
	r, err := p.Process(ctx, input("Body", title))
	if err != nil {
		t.Fatal(err)
	}
	d := r.Segments[0].Derivation
	used := strings.SplitN(strings.TrimPrefix(d.ModelInput, "passage: "), "\n\n", 2)[0]
	actual, err := p.Tokenizer.Encode(ctx, []processing.TokenInput{{Text: used}})
	if err != nil {
		t.Fatal(err)
	}
	if actual[0].Tokens > 64 || actual[0].Tokens != d.TitleTokens {
		t.Fatal("title cap counted a partial source character", actual[0].Tokens, d.TitleTokens)
	}
}

func TestOversizedAssembledTitleBatchIsExplicit(t *testing.T) {
	p := processor(t)
	title := "Paris" + strings.Repeat(" ", 100000) + "Paris"
	body := strings.TrimSuffix(strings.Repeat("Paris ", 20000), " ")
	_, err := p.Process(context.Background(), input(body, title))
	if !errors.Is(err, processing.ErrUnsupported) {
		t.Fatal("deterministic tokenizer limit became a retryable outage", err)
	}
}

func TestTokenWindowsSkipPreservedParts(t *testing.T) {
	p := processor(t)
	parts := []content.Part{
		{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: "Corps du manifeste."}},
		{Key: "source", Role: "source", Content: content.Text{Kind: "blob", BlobID: "blob_1", MediaType: "application/xml"}},
		{Key: "caption", Role: "caption", Content: content.Text{Kind: "text", Text: "légende non indexée"}},
	}
	r, err := p.Process(context.Background(), processing.Input{Organization: "org-test", Version: content.Version{ID: "version-manifest", Manifest: content.Manifest{Kind: "manifest", Parts: parts}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Segments) != 1 || r.Segments[0].PartKey != "body" || r.Segments[0].Start != 0 || r.Segments[0].End != len([]rune("Corps du manifeste.")) {
		t.Fatalf("preserved parts contributed segments: %v", r.Segments)
	}
	only := []content.Part{{Key: "source", Role: "source", Content: content.Text{Kind: "blob", BlobID: "blob_1", MediaType: "application/xml"}}}
	if _, err = p.Process(context.Background(), processing.Input{Organization: "org-test", Version: content.Version{ID: "version-blob", Manifest: content.Manifest{Kind: "manifest", Parts: only}}}); !errors.Is(err, processing.ErrUnsupported) {
		t.Fatal("manifest without retrieval text accepted", err)
	}
}
