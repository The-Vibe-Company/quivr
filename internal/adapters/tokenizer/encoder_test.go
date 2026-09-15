package tokenizer_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/tokenizer"
	"github.com/The-Vibe-Company/quivr-v2/internal/processing"
)

func TestExecutableTokenizerHelperIsPinned(t *testing.T) {
	path := os.Getenv("QUIVR_ADAPTER_CONFIG")
	if path == "" {
		t.Skip("make verify pinned tokenizer")
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
	encoder := tokenizer.Encoder{Config: cfg.Tokenizer}
	if _, err = encoder.Encode(context.Background(), []processing.TokenInput{}); err != nil {
		t.Fatal("pinned helper failed", err)
	}
	// This replacement would successfully return the expected empty batch if executed.
	replacement := filepath.Join(t.TempDir(), "helper.py")
	if err = os.WriteFile(replacement, []byte("print('[]')\n"), 0600); err != nil {
		t.Fatal(err)
	}
	encoder.Config.Script = replacement
	if _, err = encoder.Encode(context.Background(), []processing.TokenInput{}); err == nil {
		t.Fatal("unidentified producer executed")
	}
}
