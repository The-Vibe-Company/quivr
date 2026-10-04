package devhost_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost"
)

const normativeSubscriptions = "../../../contracts/plugins/v0/fixtures/"

func subscriptionManifest(t *testing.T, maxBatch string) *plugins.Manifest {
	t.Helper()
	raw, err := os.ReadFile(normativeSubscriptions + "manifests/valid/subscription.yaml")
	if err != nil {
		t.Fatal(err)
	}
	report := plugins.Validate(raw)
	if !report.Valid {
		t.Fatalf("%+v", report.Errors)
	}
	if maxBatch == "1" {
		report.Manifest.Contributions.Subscription.MaxBatchSize = 1
	}
	return report.Manifest
}

func TestSubscriptionFixtureVectorsRequireOptIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vectors.json")
	fixture := `{"record":{"vector_space_id":"served-text","vectors_ready":true,"parts":[{"key":"body","role":"body","text":"Port workers strike","vectors":[{"segment_id":"segment-body","vector":[1,0]}]}]},"evaluations":[{"expression":{"kind":"substring","text":"strike"},"query_vector":{"vector_space_id":"served-text","vector":[1,0]}}]}`
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		manifest := subscriptionManifest(t, "")
		if enabled {
			manifest.Contributions.Subscription.Vectors = &plugins.SubscriptionVectors{Parts: true, Query: true, QueryTextPointer: "/text"}
		}
		batches, issues, err := devhost.BuildSubscriptionRequests(path, manifest)
		if err != nil || len(issues) != 0 || len(batches) != 1 {
			t.Fatalf("enabled=%v: batches=%d issues=%v err=%v", enabled, len(batches), issues, err)
		}
		var request struct {
			Record struct {
				Space string           `json:"vector_space_id"`
				Ready *bool            `json:"vectors_ready"`
				Parts []map[string]any `json:"parts"`
			} `json:"record"`
			Evaluations []map[string]any `json:"evaluations"`
		}
		if err := json.Unmarshal(batches[0].Body, &request); err != nil {
			t.Fatal(err)
		}
		if enabled {
			if request.Record.Space != "served-text" || request.Record.Ready == nil || !*request.Record.Ready || request.Record.Parts[0]["vectors"] == nil || request.Evaluations[0]["query_vector"] == nil {
				t.Fatalf("fixture vectors lost: %s", batches[0].Body)
			}
		} else if request.Record.Space != "" || request.Record.Ready != nil || request.Record.Parts[0]["vectors"] != nil || request.Evaluations[0]["query_vector"] != nil {
			t.Fatalf("vectors leaked without opt-in: %s", batches[0].Body)
		}
	}
}

func TestBuildSubscriptionRequests(t *testing.T) {
	path := normativeSubscriptions + "subscriptions/strike.json"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !devhost.IsSubscriptionFixture(raw) {
		t.Fatal("not recognized as a subscription fixture")
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])

	batches, issues, err := devhost.BuildSubscriptionRequests(path, subscriptionManifest(t, ""))
	if err != nil || len(issues) > 0 || len(batches) != 1 {
		t.Fatalf("batches %d, issues %+v, err %v", len(batches), issues, err)
	}
	var request struct {
		InvocationID   string `json:"invocation_id"`
		IdempotencyKey string `json:"idempotency_key"`
		Record         struct {
			RecordVersionID string `json:"record_version_id"`
		} `json:"record"`
		Evaluations []struct {
			ID            string `json:"id"`
			Subscriptions []struct {
				SubscriptionID string `json:"subscription_id"`
			} `json:"subscriptions"`
		} `json:"evaluations"`
	}
	if err := json.Unmarshal(batches[0].Body, &request); err != nil {
		t.Fatal(err)
	}
	if request.IdempotencyKey != "dev:"+digest+":1" || request.InvocationID != "dev-invocation-"+digest[:16]+"-1" || request.Record.RecordVersionID != "dev-version-"+digest[:16] {
		t.Fatalf("ids %+v", request)
	}
	if len(request.Evaluations) != 2 || request.Evaluations[1].ID != "e2" || request.Evaluations[1].Subscriptions[0].SubscriptionID != "dev-subscription-2" {
		t.Fatalf("evaluations %+v", request.Evaluations)
	}
	if !reflect.DeepEqual(batches[0].Expect, map[string]string{"e1": "match", "e2": "no_match"}) || !reflect.DeepEqual(batches[0].View.PartKeys, []string{"title", "body"}) {
		t.Fatalf("expect %v view %+v", batches[0].Expect, batches[0].View)
	}

	// Batches never exceed max_batch_size.
	batches, _, _ = devhost.BuildSubscriptionRequests(path, subscriptionManifest(t, "1"))
	if len(batches) != 2 || !reflect.DeepEqual(batches[1].View.EvaluationIDs, []string{"e2"}) || !reflect.DeepEqual(batches[1].Expect, map[string]string{"e2": "no_match"}) {
		t.Fatalf("split batches %+v", batches)
	}
}

func TestBuildSubscriptionRequestsRejectsInvalidFixtures(t *testing.T) {
	m := subscriptionManifest(t, "")
	dir := t.TempDir()
	for name, c := range map[string]struct {
		body, code, path string
	}{
		"expression":    {`{"record": {"parts": []}, "evaluations": [{"expression": {"kind": "regex", "text": "a"}}]}`, plugins.CodeInvalidExpression, "/evaluations/0/expression"},
		"configuration": {`{"record": {"parts": []}, "evaluations": [{"expression": {"kind": "substring", "text": "a"}, "configuration": {"case_sensitive": 1}}]}`, plugins.CodeInvalidSubscriptionConfiguration, "/evaluations/0/configuration/case_sensitive"},
		"duplicate":     {`{"record": {"parts": [{"key": "a", "role": "body", "text": "x"}, {"key": "a", "role": "body", "text": "y"}]}, "evaluations": [{"expression": {"kind": "substring", "text": "a"}}]}`, plugins.CodeSchema, "/record/parts/1/key"},
		"schema":        {`{"record": {"parts": []}, "evaluations": []}`, plugins.CodeSchema, "/evaluations"},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".json")
			if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, issues, err := devhost.BuildSubscriptionRequests(path, m)
			if err != nil || len(issues) == 0 || issues[0].Code != c.code || !strings.HasPrefix(issues[0].Path, c.path) {
				t.Fatalf("issues %+v, err %v", issues, err)
			}
		})
	}
	// A subscription fixture for a normalizer-only plugin.
	path := filepath.Join(dir, "expression.json")
	_, report := writePlugin(t)
	if _, issues, _ := devhost.BuildSubscriptionRequests(path, report.Manifest); len(issues) != 1 || issues[0].Path != "/contributions/subscription" {
		t.Fatalf("issues %+v", issues)
	}
}
