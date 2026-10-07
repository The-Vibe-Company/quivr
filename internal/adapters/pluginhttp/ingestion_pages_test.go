package pluginhttp_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
