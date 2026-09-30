package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr-v2/sdks/go/quivrplugin"
)

// testdata/golden.json was captured from the engine's built-in segmentation
// and TEI embedding in the verify stack, before they moved here (THE-777),
// over testdata/parity-input.json. The plugin must reproduce it exactly: the
// same segments, offsets and derivations, the same refusals, and, against the
// same TEI, the same float32 vectors (by SHA-256 of their little-endian bytes).
// TEI's CPU kernels round differently from one processor family to another,
// so on another processor than the capture's the vectors are held instead to
// what the engine's former TEI request returns from the same TEI in this run.
// It runs where the pinned tokenizer is prepared: QUIVR_CORE_INGEST_CONFIG names
// a pin configuration (the verify stack's, scripts/core_ingest_plugin.py);
// vectors are compared when it names a TEI.

const space = "core.ingest.e5-small"

type text struct{ value string }

func (t *text) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		t.value = s
		return nil
	}
	var r struct {
		Text   *string              `json:"text"`
		Repeat [][2]json.RawMessage `json:"repeat"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	if r.Text != nil {
		t.value = *r.Text
		return nil
	}
	var out strings.Builder
	for _, pair := range r.Repeat {
		var piece string
		var count int
		if err := json.Unmarshal(pair[0], &piece); err != nil {
			return err
		}
		if err := json.Unmarshal(pair[1], &count); err != nil {
			return err
		}
		out.WriteString(strings.Repeat(piece, count))
	}
	t.value = out.String()
	return nil
}

type inputPart struct {
	Key, Role string
	Text      text
}

func (p *inputPart) UnmarshalJSON(b []byte) error {
	var head struct{ Key, Role string }
	if err := json.Unmarshal(b, &head); err != nil {
		return err
	}
	p.Key, p.Role = head.Key, head.Role
	return p.Text.UnmarshalJSON(b)
}

type goldenSegment struct {
	PartKey      string               `json:"part_key"`
	Start        int                  `json:"start"`
	End          int                  `json:"end"`
	Derivation   map[string]any       `json:"derivation"`
	VectorSHA256 string               `json:"vector_sha256"`
	Vectors      map[string][]float64 `json:"vectors"`
	Provenance   map[string]any       `json:"provenance"`
}

type golden struct {
	// CPU is the processor the goldens were captured on: TEI's CPU kernels
	// may round differently on another processor family.
	CPU       string `json:"cpu"`
	Documents []struct {
		ID       string          `json:"id"`
		Refused  bool            `json:"refused"`
		Segments []goldenSegment `json:"segments"`
	} `json:"documents"`
	Queries []struct {
		Normalized   string `json:"normalized"`
		Refused      bool   `json:"refused"`
		VectorSHA256 string `json:"vector_sha256"`
	} `json:"queries"`
}

// cpu names this machine's processor, for a vector mismatch.
func cpu() string {
	b, _ := os.ReadFile("/proc/cpuinfo")
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "model name") {
			return strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
		}
	}
	return "unknown"
}

// engineVector is the SHA-256 of what the engine's TEI client, before
// THE-777, got for one input: one input per request, normalized, never
// truncated, decoded as float32.
func engineVector(t *testing.T, tei, input string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"inputs": []string{input}, "normalize": true, "truncate": false})
	res, err := http.Post(strings.TrimRight(tei, "/")+"/embed", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var vectors [][]float32
	if err = json.NewDecoder(res.Body).Decode(&vectors); err != nil || res.StatusCode != 200 || len(vectors) != 1 {
		t.Fatalf("TEI answered %d: %v", res.StatusCode, err)
	}
	v := make([]float64, len(vectors[0]))
	for i, x := range vectors[0] {
		v[i] = float64(x)
	}
	return vectorSHA256(v)
}

func vectorSHA256(v []float64) string {
	raw := make([]byte, 4*len(v))
	for i, x := range v {
		// The engine stores what it reads as float64, converted to float32.
		binary.LittleEndian.PutUint32(raw[4*i:], math.Float32bits(float32(x)))
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// engineQuery is what the engine does to a query before it asks the plugin.
func engineQuery(q string) (string, bool) {
	if !validText(q) || utf8.RuneCountInString(q) > Parameters.MaxQueryCodepoints {
		return "", false
	}
	q = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(q, "\r\n", "\n"), "\r", "\n"))
	return q, q != ""
}

func TestReproducesTheEngineGoldens(t *testing.T) {
	path := os.Getenv("QUIVR_CORE_INGEST_CONFIG")
	if path == "" {
		t.Skip("the pinned tokenizer and TEI are prepared by the verify stack (scripts/core_ingest_plugin.py)")
	}
	config, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pinned configuration
	if err = json.Unmarshal(config, &pinned); err != nil {
		t.Fatal(err)
	}
	// Without a TEI (a tokenizer-only check), segments are compared and vectors skipped.
	vectors := pinned.TEIURL != ""
	if !vectors {
		pinned.TEIURL = "http://127.0.0.1:9"
		config, _ = json.Marshal(pinned)
	}
	// Each pass has its own plugin process state, so the batched pass asks
	// TEI itself instead of reusing the vectors the first pass kept.
	newPost := func() func(route string, body map[string]any) (int, []byte) {
		plugin, err := quivrplugin.New("quivr-plugin.yaml")
		if err != nil {
			t.Fatal(err)
		}
		if err = plugin.Ingestion(&ingester{backends: map[string]*backend{}, budget: CallBudget}); err != nil {
			t.Fatal(err)
		}
		handler, err := plugin.Handler()
		if err != nil {
			t.Fatal(err)
		}
		return func(route string, body map[string]any) (int, []byte) {
			b, _ := json.Marshal(body)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, route, bytes.NewReader(b)))
			return rec.Code, rec.Body.Bytes()
		}
	}
	post := newPost()
	// Batched TEI requests must answer the same vectors as one-input requests.
	type pass struct {
		spaces []string
		config json.RawMessage
		post   func(string, map[string]any) (int, []byte)
	}
	passes := []pass{{[]string{}, config, post}}
	if vectors {
		batched := pinned
		batched.BatchSize = 8
		batchedConfig, _ := json.Marshal(batched)
		passes = append(passes, pass{[]string{space}, config, post}, pass{[]string{space}, batchedConfig, newPost()})
	}
	var input struct {
		Documents []struct {
			ID    string      `json:"id"`
			Parts []inputPart `json:"parts"`
		} `json:"documents"`
		Queries []text `json:"queries"`
	}
	var want golden
	for file, into := range map[string]any{"testdata/parity-input.json": &input, "testdata/golden.json": &want} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(b, into); err != nil {
			t.Fatal(file, err)
		}
	}
	sameCPU := want.CPU == cpu()
	if len(want.Documents) != len(input.Documents) || len(want.Queries) != len(input.Queries) {
		t.Fatalf("golden has %d documents and %d queries for %d and %d inputs", len(want.Documents), len(want.Queries), len(input.Documents), len(input.Queries))
	}
	segments, compared := 0, 0
	for n, d := range input.Documents {
		g := want.Documents[n]
		parts := []map[string]any{}
		for _, p := range d.Parts {
			parts = append(parts, map[string]any{"key": p.Key, "role": p.Role, "text": p.Text.value})
		}
		for _, run := range passes {
			requested := run.spaces
			status, body := run.post("/v0/contributions/ingestion/segment_and_embed", map[string]any{
				"invocation_id": "parity-" + d.ID, "idempotency_key": "parity:" + d.ID, "contribution": "ingestion", "organization_id": "org-parity",
				"configuration": run.config, "version": map[string]string{"corpus_id": "c", "record_id": "r", "record_version_id": "v-" + d.ID},
				"parts": parts, "spaces": requested})
			if g.Refused {
				var e struct{ Code string }
				_ = json.Unmarshal(body, &e)
				if status != 422 || e.Code != "segmentation_limit" {
					t.Errorf("%s: the engine refused it; the plugin answered %d %s", d.ID, status, body)
				}
				continue
			}
			if status != 200 {
				t.Fatalf("%s (spaces %v): %d %s", d.ID, requested, status, body)
			}
			var got struct {
				Segments []goldenSegment `json:"segments"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Segments) != len(g.Segments) {
				t.Errorf("%s: %d segments, the engine made %d", d.ID, len(got.Segments), len(g.Segments))
				continue
			}
			for i, s := range got.Segments {
				e := g.Segments[i]
				derivation := map[string]any{}
				for k, v := range e.Derivation {
					if k != "model_input" {
						derivation[k] = v
					}
				}
				if s.PartKey != e.PartKey || s.Start != e.Start || s.End != e.End || !reflect.DeepEqual(s.Provenance, derivation) {
					t.Errorf("%s segment %d: %s [%d,%d) %v; the engine made %s [%d,%d) %v", d.ID, i, s.PartKey, s.Start, s.End, s.Provenance, e.PartKey, e.Start, e.End, derivation)
				}
				if len(requested) == 0 {
					segments++
					if len(s.Vectors) != 0 {
						t.Errorf("%s segment %d: vectors without a requested space", d.ID, i)
					}
					continue
				}
				expected := e.VectorSHA256
				if !sameCPU {
					expected = engineVector(t, pinned.TEIURL, e.Derivation["model_input"].(string))
				}
				if got := vectorSHA256(s.Vectors[space]); got != expected {
					t.Errorf("%s segment %d: vector %s; the engine's is %s (captured on %s, running on %s)", d.ID, i, got, expected, want.CPU, cpu())
				}
				compared++
			}
		}
	}
	queries := 0
	for n, q := range input.Queries {
		g := want.Queries[n]
		normalized, ok := engineQuery(q.value)
		if !ok {
			if !g.Refused {
				t.Errorf("query %d: the engine accepted it", n)
			}
			continue
		}
		status, body := post("/v0/contributions/ingestion/embed_query", map[string]any{
			"invocation_id": "parity-query", "contribution": "ingestion", "organization_id": "org-parity", "configuration": json.RawMessage(config),
			"space": space, "query": map[string]string{"modality": "text", "text": normalized}})
		if g.Refused {
			if status != 422 {
				t.Errorf("query %d: the engine refused it; the plugin answered %d %s", n, status, body)
			}
			continue
		}
		if normalized != g.Normalized {
			t.Errorf("query %d: normalized %q, the engine %q", n, normalized, g.Normalized)
		}
		if !vectors {
			continue
		}
		var answer struct {
			Vector []float64 `json:"vector"`
		}
		if status != 200 || json.Unmarshal(body, &answer) != nil {
			t.Fatalf("query %d: %d %s", n, status, body)
		}
		expected := g.VectorSHA256
		if !sameCPU {
			expected = engineVector(t, pinned.TEIURL, "query: "+g.Normalized)
		}
		if got := vectorSHA256(answer.Vector); got != expected {
			t.Errorf("query %d: vector %s; the engine's is %s (captured on %s, running on %s)", n, got, expected, want.CPU, cpu())
		}
		queries++
	}
	against := "the goldens"
	if !sameCPU {
		against = "the engine's TEI request in this run (processor " + cpu() + ", goldens from " + want.CPU + ")"
	}
	t.Logf("%d documents, %d segments, %d segment vectors and %d query vectors identical to %s", len(input.Documents), segments, compared, queries, against)
}
