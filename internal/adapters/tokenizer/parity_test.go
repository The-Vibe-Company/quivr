package tokenizer_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/tokenizer"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/processing"
)

// fixtureRows are the workload-v1 records: [id, title, body, language, query].
func fixtureRows(t *testing.T) [][]string {
	t.Helper()
	data, err := os.ReadFile("../weaviate/testdata/relevance-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]string
	if err = json.Unmarshal(data, &rows); err != nil || len(rows) != 24 {
		t.Fatal("fixture", err, len(rows))
	}
	return rows
}

func words(word string, n int) string { return strings.TrimSuffix(strings.Repeat(word+" ", n), " ") }

// TestServerMatchesPinnedReference proves the persistent tokenizer returns exactly
// what the pinned one-shot helper (scripts/token_offsets.py) returns (THE-675).
func TestServerMatchesPinnedReference(t *testing.T) {
	cfg := pinnedConfig(t)
	reference := tokenizer.Encoder{Config: cfg}
	server := &tokenizer.Server{Config: cfg}
	defer server.Close()
	ctx := context.Background()
	rows := fixtureRows(t)

	// Items are encoded independently, so large batches keep the one-shot reference
	// affordable (~0.35-0.9 s per call) without changing any item's result.
	fixture := []processing.TokenInput{}
	for _, r := range rows {
		fixture = append(fixture,
			processing.TokenInput{Text: strings.TrimSpace(r[1])}, processing.TokenInput{Text: r[2]},
			processing.TokenInput{Text: r[4]}, processing.TokenInput{Text: "query: " + r[4], Special: true},
			processing.TokenInput{Text: "passage: " + strings.TrimSpace(r[1]) + "\n\n" + strings.TrimSpace(r[2]), Special: true},
		)
	}
	edge := []string{
		"", " ", "\n\n", "a\r\nb\rc", "🌞 é ñ ﬁ", "𝔘𝔫𝔦𝔠𝔬𝔡𝔢 😀👩‍👩‍👧", "مرحبا بالعالم", "עברית", "日本語のテキスト", "  \u200b",
		words("Paris", 255), words("Paris", 256), words("Paris", 257), words("Paris", 511), words("Paris", 512),
		strings.Repeat("é", 8192), strings.Repeat("x", 8192), words("l'économie", 300) + "\n\n" + words("growth", 300),
	}
	edges := []processing.TokenInput{}
	for _, text := range edge {
		edges = append(edges, processing.TokenInput{Text: text}, processing.TokenInput{Text: "query: " + text, Special: true})
	}
	many := make([]processing.TokenInput, 512)
	for i := range many {
		many[i] = processing.TokenInput{Text: rows[i%len(rows)][1], Special: i%2 == 0}
	}
	batches := [][]processing.TokenInput{fixture, edges, many, {{Text: words("Élection présidentielle, inflation; énergie!", 40000)}}, {}}
	for i, batch := range batches {
		want, wantErr := reference.Encode(ctx, batch)
		got, gotErr := server.Encode(ctx, batch)
		if wantErr != nil || gotErr != nil || !reflect.DeepEqual(want, got) {
			t.Fatalf("batch %d diverged: reference err=%v server err=%v", i, wantErr, gotErr)
		}
	}
	t.Logf("THE-675 parity: %d items identical", len(fixture)+len(edges)+len(many)+1)

	oneShot := processing.TokenWindows{Tokenizer: reference}
	persistent := processing.TokenWindows{Tokenizer: server}
	// End to end on a sample; every other fixture item is covered by the encodings above.
	for _, r := range rows[:3] {
		v := content.Version{ID: r[0], RecordID: r[0], Manifest: content.Manifest{Kind: "text", Parts: []content.Part{{Key: "title", Role: "title", Content: content.Text{Kind: "text", Text: r[1]}}, {Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: r[2]}}}}}
		in := processing.Input{Organization: "parity", Version: v}
		want, wantErr := oneShot.Process(ctx, in)
		got, gotErr := persistent.Process(ctx, in)
		if wantErr != nil || gotErr != nil || !reflect.DeepEqual(want, got) {
			t.Fatalf("segmentation of %s diverged: %v %v", r[0], wantErr, gotErr)
		}
	}
	long := content.Version{ID: "long", RecordID: "long", Manifest: content.Manifest{Kind: "text", Parts: []content.Part{{Key: "title", Role: "title", Content: content.Text{Kind: "text", Text: words("Titre", 80)}}, {Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: words("Paris", 300) + "\n\n" + words("énergie", 900) + " 🌞 é\r\n"}}}}}
	want, wantErr := oneShot.Process(ctx, processing.Input{Organization: "parity", Version: long})
	got, gotErr := persistent.Process(ctx, processing.Input{Organization: "parity", Version: long})
	if wantErr != nil || gotErr != nil || !reflect.DeepEqual(want, got) || len(want.Segments) < 2 {
		t.Fatal("multi-window segmentation diverged", wantErr, gotErr)
	}
	for _, q := range []string{rows[0][4], " \r\n", words("Paris", 256), words("Paris", 257), strings.Repeat("é", 8193)} {
		want, wantErr := oneShot.NormalizeQuery(ctx, q)
		got, gotErr := persistent.NormalizeQuery(ctx, q)
		if want != got || (wantErr == nil) != (gotErr == nil) {
			t.Fatalf("query normalization diverged for %.40q: %v %v", q, wantErr, gotErr)
		}
	}
}

// BenchmarkServerQueryEncode is the post-fix counterpart of BenchmarkTokenizerQueryEncode.
func BenchmarkServerQueryEncode(b *testing.B) {
	server := &tokenizer.Server{Config: pinnedConfig(b)}
	defer server.Close()
	if _, err := server.Encode(context.Background(), queryBatch); err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		if _, err := server.Encode(context.Background(), queryBatch); err != nil {
			b.Fatal(err)
		}
	}
}
