package pluginhttp_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
)

// The HTTP adapter owns bounded source windows and Unicode offset translation,
// across both page and Part boundaries. Provider negotiation is tested by hosted.embed.
func TestIngestionPagesCoverLargeMultipartText(t *testing.T) {
	for _, chunkRunes := range []int{64, 4096} {
		t.Run(fmt.Sprint(chunkRunes), func(t *testing.T) {
			ring, err := plugins.NewSigningKeys()
			if err != nil {
				t.Fatal(err)
			}
			signing, _ := json.Marshal(map[string]plugins.SigningKeys{"example.paged": ring})
			t.Setenv(plugins.EnvSigningKeys, string(signing))
			var pin *plugins.Pin
			var captureMu sync.Mutex
			var requestKeys []string
			calls := 0
			cached := map[string][]byte{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveDiscovery(w, r, pin) {
					return
				}
				var req plugins.SegmentAndEmbedRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if req.Page == nil || len(req.Parts) != 1 || req.Parts[0].Role != "body" || len([]rune(req.Parts[0].Text)) > 4096 {
					t.Errorf("unbounded page: %+v", req.Page)
					return
				}
				// Negative control: removing the host guard reaches the old oversize-slice panic.
				if req.Page.Start >= len([]rune(req.Parts[0].Text)) {
					w.WriteHeader(422)
					_, _ = w.Write([]byte(`{"code":"response_too_large","message":"smaller page required","retryable":false}`))
					return
				}
				captureMu.Lock()
				defer captureMu.Unlock()
				requestKeys = append(requestKeys, req.IdempotencyKey)
				calls++
				if body, ok := cached[req.IdempotencyKey]; ok {
					_, _ = w.Write(body)
					return
				}
				length := len([]rune(req.Parts[0].Text))
				segments := []map[string]any{}
				end := req.Page.Start
				for end < length && len(segments) < req.Page.MaxSegments {
					start := end
					end = min(start+chunkRunes, length)
					segments = append(segments, map[string]any{"part_key": req.Parts[0].Key, "start": start, "end": end, "lexical_text": string([]rune(req.Parts[0].Text)[start:end]), "source_ranges": []map[string]any{{"part_key": req.Parts[0].Key, "start": start, "end": end}}, "vectors": map[string]any{"example.paged.text": []float32{1, 0, 0, 0}}})
				}
				answer := map[string]any{"segments": segments}
				if end < length {
					answer["next_start"] = end
				}
				w.Header().Set("Content-Type", "application/json")
				body, _ := json.Marshal(answer)
				if len(body) > 2048 {
					w.WriteHeader(422)
					_, _ = w.Write([]byte(`{"code":"response_too_large","message":"use a smaller page","retryable":false}`))
					return
				}
				cached[req.IdempotencyKey] = body
				_, _ = w.Write(body)
			}))
			defer server.Close()
			raw, err := os.ReadFile("../../../contracts/plugins/v0/fixtures/manifests/valid/ingestion-paged.yaml")
			if err != nil {
				t.Fatal(err)
			}
			pin, err = plugins.LoadPinManifest([]byte(strings.Replace(strings.Replace(string(raw), "max_segments: 2", "max_segments: 16", 1), "max_response_bytes: 65536", "max_response_bytes: 2048", 1)), "paged", plugins.PinConfig{Endpoint: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			ingestor := pluginhttp.Ingestor{Pin: pin}
			v := content.Version{ID: "large", RecordID: "record", Manifest: content.Manifest{Parts: []content.Part{
				{Key: "headline", Role: "title", Content: content.Text{Kind: "text", Text: strings.Repeat("Ω", 5000)}},
				{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: strings.Repeat("α", 140000)}},
			}}}
			for n := range 70 {
				v.Manifest.Parts = append(v.Manifest.Parts, content.Part{Key: strings.Repeat("x", n+1), Role: "context", Content: content.Text{Kind: "text", Text: "tail"}})
			}
			v.Manifest.Parts = append(v.Manifest.Parts, content.Part{Key: "space", Role: "body", Content: content.Text{Kind: "text", Text: " \n "}})
			_, err = ingestor.SegmentAndEmbedPage(t.Context(), "org", "corpus", v, nil, json.RawMessage(`{"page_start":5000}`))
			if !errors.Is(err, content.ErrIngestionRefused) {
				t.Fatalf("corrupt source cursor err=%v", err)
			}
			texts := map[string][]rune{}
			for _, part := range v.Manifest.Parts {
				texts[part.Key] = []rune(part.Content.Text)
			}
			reconstructed := map[string]string{}
			ends := map[string]int{}
			var cursor json.RawMessage
			count := 0
			for {
				page, err := ingestor.SegmentAndEmbedPage(t.Context(), "org", "corpus", v, []string{plugins.SpaceKey("example.paged.text", "1")}, cursor)
				if err != nil {
					t.Fatal(err)
				}
				if count == 0 {
					captureMu.Lock()
					firstKeys := slices.Clone(requestKeys)
					captureMu.Unlock()
					// jsonb reorders keys and adds spaces on durable round trips.
					_, err = ingestor.SegmentAndEmbedPage(t.Context(), "org", "corpus", v, []string{plugins.SpaceKey("example.paged.text", "1")}, json.RawMessage(`{"part": 0, "page_start": 0, "rune_start": 0, "byte_start": 0}`))
					if err != nil {
						t.Fatal(err)
					}
					captureMu.Lock()
					retryKeys := slices.Clone(requestKeys[len(firstKeys):])
					captureMu.Unlock()
					if !slices.Equal(firstKeys, retryKeys) {
						t.Fatalf("equivalent durable cursor changed page identity: %v vs %v", firstKeys, retryKeys)
					}
				}
				for _, seg := range page.Segments {
					if seg.Start != ends[seg.PartKey] || len(seg.SourceRanges) != 1 || seg.SourceRanges[0].Start != seg.Start || seg.SourceRanges[0].End != seg.End {
						t.Fatalf("lost/repeated source: %+v", seg)
					}
					reconstructed[seg.PartKey] += string(texts[seg.PartKey][seg.Start:seg.End])
					ends[seg.PartKey] = seg.End
					count++
				}
				if len(page.Next) == 0 {
					break
				}
				cursor = page.Next
			}
			for _, part := range v.Manifest.Parts {
				if reconstructed[part.Key] != part.Content.Text {
					t.Fatalf("incomplete Part %s: covered %d of %d codepoints", part.Key, ends[part.Key], len([]rune(part.Content.Text)))
				}
			}
			if (chunkRunes == 64 && count <= 1024) || calls < 2 {
				t.Fatalf("did not cross legacy limits: segments=%d calls=%d", count, calls)
			}
		})
	}
}

// Even bounded source can need more passages or response bytes than one call
// permits. Both SDKs report these work limits as invalid_response envelopes.
func TestWholeItemResponseLimitsRemainPageable(t *testing.T) {
	ring, err := plugins.NewSigningKeys()
	if err != nil {
		t.Fatal(err)
	}
	signing, _ := json.Marshal(map[string]plugins.SigningKeys{"example.paged": ring})
	t.Setenv(plugins.EnvSigningKeys, string(signing))
	for _, message := range []string{"257 segments exceed max_segments 256", "the response exceeds max_segments", "the response is 65537 bytes; max_response_bytes is 65536", "the response exceeds max_response_bytes", "invalid vector dimensions", "the response exceeds max_segments; invalid vector dimensions", "engine segment limit", "mixed engine errors"} {
		t.Run(message, func(t *testing.T) {
			var pin *plugins.Pin
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveDiscovery(w, r, pin) {
					return
				}
				if message == "engine segment limit" || message == "mixed engine errors" {
					end := 3
					if message == "mixed engine errors" {
						end = 5
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"segments": []map[string]any{
						{"part_key": "body", "start": 0, "end": 1, "vectors": map[string]any{}},
						{"part_key": "body", "start": 1, "end": 2, "vectors": map[string]any{}},
						{"part_key": "body", "start": 2, "end": end, "vectors": map[string]any{}},
					}})
					return
				}
				w.WriteHeader(500)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": "invalid_response", "message": message, "retryable": false})
			}))
			defer server.Close()
			raw, err := os.ReadFile("../../../contracts/plugins/v0/fixtures/manifests/valid/ingestion-paged.yaml")
			if err != nil {
				t.Fatal(err)
			}
			pin, err = plugins.LoadPinManifest(raw, "paged", plugins.PinConfig{Endpoint: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			v := content.Version{ID: "item", Manifest: content.Manifest{Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: "body"}}}}}
			_, err = (pluginhttp.Ingestor{Pin: pin}).SegmentAndEmbed(t.Context(), "org", "corpus", v, nil)
			var refusal *plugins.PluginError
			want := "segmentation_limit"
			if strings.Contains(message, "invalid vector dimensions") {
				want = "invalid_response"
			}
			if message == "mixed engine errors" {
				if !errors.Is(err, content.ErrIngestionRefused) || errors.As(err, &refusal) {
					t.Fatalf("mixed output error was masked: %v", err)
				}
				return
			}
			if !errors.Is(err, content.ErrIngestionRefused) || !errors.As(err, &refusal) || refusal.Code != want {
				t.Fatalf("error=%v; want terminal %s", err, want)
			}
		})
	}
}
