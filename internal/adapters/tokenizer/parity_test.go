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
	// End to end on a sample: every Encode call the persistent TokenWindows makes is
	// recorded, then held to the reference below. TokenWindows is deterministic given
	// its encodings, so equal encodings mean the one-shot helper would produce the same
	// Segmentation and query decisions. Recording keeps the reference to a few process
	// starts instead of one per call.
	recorder := &recordingTokenizer{next: server}
	persistent := processing.TokenWindows{Tokenizer: recorder}
	for _, r := range rows[:3] {
		v := content.Version{ID: r[0], RecordID: r[0], Manifest: content.Manifest{Kind: "text", Parts: []content.Part{{Key: "title", Role: "title", Content: content.Text{Kind: "text", Text: r[1]}}, {Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: r[2]}}}}}
		if _, err := persistent.Process(ctx, processing.Input{Organization: "parity", Version: v}); err != nil {
			t.Fatalf("segmentation of %s: %v", r[0], err)
		}
	}
	long := content.Version{ID: "long", RecordID: "long", Manifest: content.Manifest{Kind: "text", Parts: []content.Part{{Key: "title", Role: "title", Content: content.Text{Kind: "text", Text: words("Titre", 80)}}, {Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: words("Paris", 300) + "\n\n" + words("énergie", 900) + " 🌞 é\r\n"}}}}}
	if seg, err := persistent.Process(ctx, processing.Input{Organization: "parity", Version: long}); err != nil || len(seg.Segments) < 2 {
		t.Fatal("multi-window segmentation", err)
	}
	for _, q := range []string{rows[0][4], " \r\n", words("Paris", 256), words("Paris", 257), strings.Repeat("é", 8193)} {
		_, _ = persistent.NormalizeQuery(ctx, q)
	}
	if recorder.err != nil || len(recorder.inputs) == 0 {
		t.Fatal("persistent tokenizer during segmentation and query normalization", recorder.err)
	}

	// Items are encoded independently, so batches that fit the limits share one reference
	// call without changing any item's result; the server still sees each batch as sent.
	huge := []processing.TokenInput{{Text: words("Élection présidentielle, inflation; énergie!", 40000)}}
	batches := [][]processing.TokenInput{fixture, edges, many, huge, {}}
	got := make([][]processing.Encoding, len(batches))
	for i, batch := range batches {
		var err error
		if got[i], err = server.Encode(ctx, batch); err != nil {
			t.Fatalf("server batch %d: %v", i, err)
		}
	}
	joined := append(append(append([]processing.TokenInput{}, fixture...), edges...), recorder.inputs...)
	joinedWant, err := reference.Encode(ctx, joined)
	if err != nil {
		t.Fatal("reference", err)
	}
	joinedGot := append(append(append([]processing.Encoding{}, got[0]...), got[1]...), recorder.outputs...)
	if !reflect.DeepEqual(joinedWant, joinedGot) {
		for i := range joinedWant {
			if i < len(joinedGot) && !reflect.DeepEqual(joinedWant[i], joinedGot[i]) {
				t.Fatalf("item %d diverged (fixture %d, edges %d, then recorded): %.60q", i, len(fixture), len(edges), joined[i].Text)
			}
		}
		t.Fatalf("encodings diverged: reference %d items, server %d", len(joinedWant), len(joinedGot))
	}
	for _, i := range []int{2, 3, 4} {
		want, err := reference.Encode(ctx, batches[i])
		if err != nil || !reflect.DeepEqual(want, got[i]) {
			t.Fatalf("batch %d diverged: reference err=%v", i, err)
		}
	}
	t.Logf("THE-675 parity: %d items identical", len(fixture)+len(edges)+len(many)+len(huge)+len(recorder.inputs))
}

// recordingTokenizer passes every call to next and keeps its inputs and outputs.
type recordingTokenizer struct {
	next    processing.Tokenizer
	inputs  []processing.TokenInput
	outputs []processing.Encoding
	err     error
}

func (r *recordingTokenizer) Encode(ctx context.Context, input []processing.TokenInput) ([]processing.Encoding, error) {
	out, err := r.next.Encode(ctx, input)
	if err != nil {
		r.err = err
		return nil, err
	}
	r.inputs, r.outputs = append(r.inputs, input...), append(r.outputs, out...)
	return out, nil
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
