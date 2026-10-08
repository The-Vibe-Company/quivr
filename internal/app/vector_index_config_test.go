package app

import (
	"encoding/json"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

// Literal external keys and omission own the vector index setting a
// deployment registers with its spaces.
func TestVectorIndexConfiguration(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  content.VectorIndex
		valid bool
	}{
		{`{}`, content.VectorIndex{Quantization: "rq-8"}, true},
		{`{"vector_index":{"rescore_limit":40}}`, content.VectorIndex{Quantization: "rq-8", RescoreLimit: 40}, true},
		{`{"vector_index":{"quantization":"rq-1"}}`, content.VectorIndex{Quantization: "rq-1"}, true},
		{`{"vector_index":{"quantization":"rq-1","rescore_limit":512,"spaces":{"quivr.e5_small":{"quantization":"rq-8"}}}}`, content.VectorIndex{Quantization: "rq-8", RescoreLimit: 512}, true},
		{`{"vector_index":{"rescore_limit":64,"spaces":{"quivr.e5_small":{"quantization":"none"}}}}`, content.VectorIndex{Quantization: "none"}, true},
		{`{"vector_index":{"spaces":{"another.space":{"quantization":"rq-1"}}}}`, content.VectorIndex{Quantization: "rq-8"}, true},
		{`{"vector_index":{"quantization":"rq-4"}}`, content.VectorIndex{}, false},
		{`{"vector_index":{"quantization":"none","rescore_limit":20}}`, content.VectorIndex{}, false},
		{`{"vector_index":{"spaces":{"another.space":{"rescore_limit":-1}}}}`, content.VectorIndex{}, false},
	} {
		var cfg Config
		if err := json.Unmarshal([]byte(tc.input), &cfg); err != nil {
			t.Fatal(err)
		}
		err := cfg.VectorIndex.Validate()
		if (err == nil) != tc.valid {
			t.Fatalf("config %s: error %v, want valid=%v", tc.input, err, tc.valid)
		}
		if !tc.valid {
			continue
		}
		spaces := cfg.DeploymentSpaces(nil)
		if len(spaces) != 1 || spaces[0].Index == nil || *spaces[0].Index != tc.want {
			t.Fatalf("config %s: registered %+v, want index %+v", tc.input, spaces, tc.want)
		}
	}
}
