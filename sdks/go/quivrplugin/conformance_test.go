package quivrplugin

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const fixtures = "../../../contracts/plugins/v0/fixtures"

// TestModelsCarryEveryContractField decodes every valid normative
// Contribution request and response into the SDK's types, rejecting unknown
// fields, and re-encodes it: a field the contract adds or renames without the
// SDK following fails here.
func TestModelsCarryEveryContractField(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixtures, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index struct {
		Cases []struct {
			File        string `json:"file"`
			Schema      string `json:"schema"`
			Valid       bool   `json:"valid"`
			SchemaValid bool   `json:"schema_valid"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	models := map[string]func() any{
		"normalizer-request.schema.json":                  func() any { return &NormalizerRequest{} },
		"normalizer-response.schema.json":                 func() any { return &NormalizerResponse{} },
		"subscription-request.schema.json":                func() any { return &SubscriptionRequest{} },
		"subscription-response.schema.json":               func() any { return &SubscriptionResponse{} },
		"ingestion-segment-and-embed-request.schema.json": func() any { return &IngestRequest{} },
		"ingestion-segment-and-embed-response.schema.json": func() any {
			return &struct {
				Segments []Segment `json:"segments"`
			}{}
		},
		"ingestion-embed-query-request.schema.json": func() any { return &QueryRequest{} },
		"ingestion-embed-query-response.schema.json": func() any {
			return &struct {
				Vector []float32 `json:"vector"`
			}{}
		},
		"retrieval-search-request.schema.json":               func() any { return &SearchRequest{} },
		"connector-fetch-request.schema.json":                func() any { return &FetchRequest{} },
		"connector-check-credential-request.schema.json":     func() any { return &CredentialRequest{} },
		"connector-fetch-response.schema.json":               func() any { return &pageJSON{} },
		"connector-check-credential-response.schema.json":    func() any { return &credentialJSON{} },
		"connector-describe-attachment-request.schema.json":  func() any { return &AttachmentRequest{} },
		"connector-upload-attachment-request.schema.json":    func() any { return &AttachmentRequest{} },
		"connector-describe-attachment-response.schema.json": func() any { return &describeJSON{} },
		"connector-upload-attachment-response.schema.json":   func() any { return &uploadJSON{} },
		"connector-receive-request.schema.json":              func() any { return &ReceiveRequest{} },
		"connector-receive-response.schema.json":             func() any { return &deliveryJSON{} },
	}
	checked := map[string]int{}
	for _, c := range index.Cases {
		model, ok := models[c.Schema]
		if !ok || !c.Valid {
			continue
		}
		t.Run(c.File, func(t *testing.T) {
			doc, err := os.ReadFile(filepath.Join(fixtures, c.File))
			if err != nil {
				t.Fatal(err)
			}
			v := model()
			dec := json.NewDecoder(bytes.NewReader(doc))
			dec.DisallowUnknownFields()
			if err := dec.Decode(v); err != nil {
				t.Fatalf("decode: %v", err)
			}
			again, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			want, got := generic(t, doc), generic(t, again)
			// A Credential re-encodes as [redacted] by design.
			for _, m := range []map[string]any{want, got} {
				delete(m, "credential")
			}
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("round trip lost data:\nwant %v\ngot  %v", want, got)
			}
			checked[c.Schema]++
		})
	}
	for schema := range models {
		if checked[schema] == 0 {
			t.Errorf("no normative fixture exercised %s", schema)
		}
	}
}

func generic(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}
