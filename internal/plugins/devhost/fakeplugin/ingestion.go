package fakeplugin

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
)

// ingestionRoutes serves the ingestion Contribution. The well-behaved plugin
// cuts every non-empty text Part into one segment, embeds it with a
// deterministic hash of its text and lower-cases it as lexical text. Broken
// modes of tests/plugin-contract: ingestion-offset, ingestion-dimensions,
// ingestion-missing-vector, ingestion-nondeterministic, ingestion-split
// (another segmentation than the fixture expects), ingestion-query-dimensions,
// ingestion-query-nondeterministic, ingestion-secret-leak (echoes the value of
// the first declared secret), ingestion-segments-only-split (other segments
// when the request asks for no space) and accept-invalid.
func ingestionRoutes(mux *http.ServeMux, mode string, m *plugins.Manifest, write func(http.ResponseWriter, int, any)) {
	declared := m.Contributions.Ingestion.Spaces
	refuse := func(w http.ResponseWriter, message string) {
		status := 400
		if mode == "accept-invalid" {
			status = 202
		}
		write(w, status, map[string]any{"code": "invalid_request", "message": message, "retryable": false})
	}
	validate := func(schema string, body []byte, spaces ...string) error {
		if issues := plugins.ValidateDocument(schema, body); len(issues) > 0 {
			return fmt.Errorf("%s %s", issues[0].Path, issues[0].Message)
		}
		for _, id := range spaces {
			if _, ok := declared[id]; !ok {
				return fmt.Errorf("space %q is not declared", id)
			}
		}
		return nil
	}
	mux.HandleFunc("POST "+plugins.SegmentAndEmbedRoute, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request struct {
			InvocationID string                  `json:"invocation_id"`
			Parts        []plugins.IngestionPart `json:"parts"`
			Spaces       []string                `json:"spaces"`
		}
		_ = json.Unmarshal(body, &request)
		if err := validate("ingestion-segment-and-embed-request.schema.json", body, request.Spaces...); err != nil {
			refuse(w, err.Error())
			return
		}
		segments := []any{}
		for _, p := range request.Parts {
			n := utf8.RuneCountInString(p.Text)
			if n == 0 {
				continue
			}
			bounds := [][2]int{{0, n}}
			if (mode == "ingestion-split" || mode == "ingestion-segments-only-split" && len(request.Spaces) == 0) && n > 1 {
				bounds = [][2]int{{0, n / 2}, {n / 2, n}}
			}
			for _, b := range bounds {
				text := string([]rune(p.Text)[b[0]:b[1]])
				vectors := map[string]any{}
				for i, id := range request.Spaces {
					if mode == "ingestion-missing-vector" && i == len(request.Spaces)-1 {
						continue
					}
					dims := declared[id].Dimensions
					if mode == "ingestion-dimensions" {
						dims--
					}
					vectors[id] = hashVector(text, dims)
				}
				end := b[1]
				if mode == "ingestion-offset" {
					end = n + 1
				}
				segment := map[string]any{"part_key": p.Key, "start": b[0], "end": end, "vectors": vectors, "lexical_text": strings.ToLower(text)}
				switch mode {
				case "ingestion-nondeterministic":
					segment["provenance"] = map[string]any{"invocation": request.InvocationID}
				case "ingestion-secret-leak":
					if len(m.Secrets) > 0 {
						segment["provenance"] = map[string]any{"key": os.Getenv(m.Secrets[0].Name)}
					}
				}
				segments = append(segments, segment)
			}
		}
		write(w, 200, map[string]any{"segments": segments})
	})
	mux.HandleFunc("POST "+plugins.EmbedQueryRoute, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request struct {
			InvocationID string `json:"invocation_id"`
			Space        string `json:"space"`
			Query        struct {
				Text string `json:"text"`
			} `json:"query"`
		}
		_ = json.Unmarshal(body, &request)
		if err := validate("ingestion-embed-query-request.schema.json", body, request.Space); err != nil {
			refuse(w, err.Error())
			return
		}
		dims, text := declared[request.Space].Dimensions, request.Query.Text
		switch mode {
		case "ingestion-query-dimensions":
			dims++
		case "ingestion-query-nondeterministic":
			text += request.InvocationID
		}
		write(w, 200, map[string]any{"vector": hashVector(text, dims)})
	})
}

// hashVector is a deterministic unit vector of the text: feature-hashed
// lower-cased words, so texts sharing words point the same way.
func hashVector(text string, dims int) []float64 {
	v := make([]float64, max(dims, 0))
	if dims <= 0 {
		return v
	}
	words := strings.Fields(strings.ToLower(text))
	sort.Strings(words)
	for _, word := range append(words, "") {
		sum := sha256.Sum256([]byte(word))
		i := binary.BigEndian.Uint32(sum[:4]) % uint32(dims)
		if sum[4]&1 == 0 {
			v[i]++
		} else {
			v[i]--
		}
	}
	norm := 0.0
	for _, x := range v {
		norm += x * x
	}
	if norm == 0 {
		v[0], norm = 1, 1
	}
	for i := range v {
		v[i] /= math.Sqrt(norm)
	}
	return v
}
