package tei_test

import (
	"context"
	"encoding/json"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/tei"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"os"
	"strings"
	"testing"
)

func TestRealE5StableFloat32AndNoTruncation(t *testing.T) {
	path := os.Getenv("QUIVR_ADAPTER_CONFIG")
	if path == "" {
		t.Skip("make verify real TEI")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		URL string `json:"tei_url"`
	}
	if err = json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	encoder := tei.Encoder{Endpoint: cfg.URL}
	ctx := context.Background()
	first, err := encoder.Embed(ctx, "passage: Les bateaux naviguent vers la Corse.")
	if err != nil {
		t.Fatal(err)
	}
	again, err := encoder.Embed(ctx, "passage: Les bateaux naviguent vers la Corse.")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := content.VectorBytes(first)
	b, _ = content.VectorBytes(again)
	if string(a) != string(b) {
		t.Fatal("same pinned execution changed float32 payload")
	}
	if _, err = encoder.Embed(ctx, "passage: "+strings.Repeat("Paris ", 513)); err == nil {
		t.Fatal("provider silently truncated")
	}
	if _, err = encoder.Embed(ctx, "missing E5 prefix"); err == nil {
		t.Fatal("missing template accepted")
	}
}
