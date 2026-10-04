package normalization_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
)

// Fixture tools and engine ports must serialize the same contribution input.
// Invocation IDs belong to attempts; fixture keys use a local identity rather
// than an installed generation. Those identities have their own owner tests.
// Normalizer file and signed URL references are the two transport capabilities
// for the same bytes, so compare the immutable input metadata independently.
func TestEngineAndContractRunnerRequestsAgree(t *testing.T) {
	t.Run("normalizer", func(t *testing.T) {
		f := setup(t, func(int) (int, any) {
			return 422, map[string]any{"code": "bad_input", "message": "refused", "retryable": false}
		})
		dir := t.TempDir()
		writeJSON(t, filepath.Join(dir, "note.json"), map[string]any{"input": map[string]any{"path": "note.md", "media_type": "text/markdown"}, "source": f.repo.work.Command.Source, "configuration": f.pin.Configuration, "provenance": f.repo.work.Command.Provenance})
		if err := os.WriteFile(filepath.Join(dir, "note.md"), input, 0600); err != nil {
			t.Fatal(err)
		}
		want, issues, err := devhost.BuildFixtureRequest(filepath.Join(dir, "note.json"), &f.pin.Manifest)
		requireBuilt(t, issues, err)
		var request plugins.NormalizerRequest
		decode(t, want, &request)
		f.repo.work.Organization, f.repo.work.RecordID, f.repo.work.VersionID = request.OrganizationID, request.RecordID, request.RecordVersionID
		blob := content.VerifiedBlob{ID: request.Input.BlobID, MediaType: request.Input.MediaType, Blob: content.Blob{Key: "org/blob", SHA256: request.Input.SHA256, Size: request.Input.SizeBytes}}
		f.service.Content.BlobSource = blobSource{blob}
		f.repo.work.Command.Content.BlobID = blob.ID
		if err := f.service.Normalize(context.Background(), request.OrganizationID, "receipt_1"); err != nil {
			t.Fatal(err)
		}
		if len(f.plugin.requests) != 1 {
			t.Fatalf("normalizer captured %d requests", len(f.plugin.requests))
		}
		got, _ := json.Marshal(f.plugin.requests[0])
		assertRequestParity(t, got, want, true)
	})
	t.Run("ingestion", func(t *testing.T) {
		pin, captured := capturePin(t, "ingestion-valid", map[string]string{"certified.ingestion-valid.small": plugins.SpaceServed})
		raw := readFixture(t, "ingestion-valid", "memo.json")
		run, issues := devhost.BuildIngestionRun(raw, &pin.Manifest)
		requireBuilt(t, issues, nil)
		var request plugins.SegmentAndEmbedRequest
		decode(t, run.Request, &request)
		v := content.Version{ID: request.Version.RecordVersionID, RecordID: request.Version.RecordID, Manifest: content.Manifest{Kind: "manifest"}}
		for _, p := range request.Parts {
			v.Manifest.Parts = append(v.Manifest.Parts, content.Part{Key: p.Key, Role: p.Role, Content: content.Text{Kind: "text", Text: p.Text}})
		}
		keys := []string{}
		for _, id := range request.Spaces {
			keys = append(keys, plugins.SpaceKey(id, pin.Manifest.Contributions.Ingestion.Spaces[id].Version))
		}
		_, _ = (pluginhttp.Ingestor{Pin: pin}).SegmentAndEmbed(context.Background(), request.OrganizationID, request.Version.CorpusID, v, keys)
		assertRequestParity(t, *captured, run.Request, false)
		want := run.QueryRequest(request.Spaces[0], run.Queries[0], "same")
		_, _ = (pluginhttp.Ingestor{Pin: pin}).EncodeQuery(context.Background(), request.OrganizationID, keys[0], run.Queries[0])
		assertRequestParity(t, *captured, want, false)
	})
	t.Run("subscription", func(t *testing.T) {
		pin, captured := capturePin(t, "subscription-valid", nil)
		path := filepath.Join("../../tests/plugin-contract/subscription-valid/fixtures/news.json")
		batches, issues, err := devhost.BuildSubscriptionRequests(path, &pin.Manifest)
		requireBuilt(t, issues, err)
		var request plugins.SubscriptionRequest
		decode(t, batches[0].Body, &request)
		b := monitoring.Batch{Organization: request.OrganizationID, CorpusID: request.Record.CorpusID, RecordID: request.Record.RecordID, VersionID: request.Record.RecordVersionID, Enriched: request.Record.Enriched}
		for _, p := range request.Record.Parts {
			b.Article.Parts = append(b.Article.Parts, monitoring.Part{Key: p.Key, Role: p.Role, Text: p.Text})
		}
		decode(t, request.Record.Source, &b.Article.Metadata.Source)
		decode(t, request.Record.Provenance, &b.Article.Metadata.Provenance)
		b.Article.Metadata.AcceptedAt, err = time.Parse(time.RFC3339Nano, request.Record.AcceptedAt)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range request.Evaluations {
			item := monitoring.BatchItem{ID: e.ID}
			decode(t, e.Expression, &item.Expression)
			decode(t, e.Configuration, &item.Configuration)
			for _, ref := range e.Subscriptions {
				item.Subscriptions = append(item.Subscriptions, monitoring.SubscriptionRef(ref))
			}
			b.Items = append(b.Items, item)
		}
		_, _ = (pluginhttp.Evaluator{Pin: pin}).Evaluate(context.Background(), b)
		assertRequestParity(t, *captured, batches[0].Body, false)
	})
	t.Run("connector", func(t *testing.T) {
		pin, captured := capturePin(t, "connector-valid", nil)
		run, issues, err := devhost.BuildConnectorRun("../../tests/plugin-contract/connector-valid/fixtures/pages.json", &pin.Manifest)
		requireBuilt(t, issues, err)
		want := run.FetchRequest(run.Checkpoint, 0, 0, "same")
		var q plugins.ConnectorFetchRequest
		decode(t, want, &q)
		now, err := time.Parse(time.RFC3339, q.Now)
		if err != nil {
			t.Fatal(err)
		}
		c := pluginhttp.Connector{Pin: pin, Name: run.Kind}
		_, _ = c.Fetch(context.Background(), connectors.FetchRequest{Organization: q.OrganizationID, InstanceID: q.Connector.InstanceID, CorpusID: q.Connector.CorpusID, Namespace: q.Connector.SourceNamespace, WebhookURL: q.Connector.WebhookURL, Config: q.Connector.Config, Credential: q.Credential, Checkpoint: q.Checkpoint, Now: now})
		assertRequestParity(t, *captured, want, false)
		credential := connectors.CredentialRequest{Organization: q.OrganizationID, InstanceID: q.Connector.InstanceID, Config: q.Connector.Config, Credential: q.Credential, Now: now}
		_ = c.CheckCredential(context.Background(), credential)
		assertRequestParity(t, *captured, run.CheckCredentialRequest("same"), false)
		relayed := run.Receives[0].Relayed()
		bytes, err := base64.StdEncoding.DecodeString(relayed.BodyBase64)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = c.Receive(context.Background(), connectors.ReceiveRequest{Organization: q.OrganizationID, InstanceID: q.Connector.InstanceID, CorpusID: devhost.DevCorpusID, Namespace: devhost.DevSourceNamespace, Config: q.Connector.Config, Credential: q.Credential, Checkpoint: q.Checkpoint, Now: now, Request: connectors.Relayed{Method: relayed.Method, Path: relayed.Path, Query: relayed.Query, Headers: relayed.Headers, Body: bytes}})
		assertRequestParity(t, *captured, run.ReceiveRequest(relayed, "same"), false)
		size := int64(4)
		at := plugins.ConnectorAttachment{Key: "photo", Role: "attachment", MediaType: "image/png", SizeBytes: &size, SHA256: content.Hash([]byte("data")), Ref: "photo"}
		item := devhost.AttachmentItem{RecordKey: "record", Revision: "revision"}
		a := connectors.AttachmentRequest{Organization: q.OrganizationID, InstanceID: q.Connector.InstanceID, Config: q.Connector.Config, Credential: q.Credential, Now: now, RecordKey: item.RecordKey, Revision: item.Revision, Attachment: connectors.Attachment{Key: at.Key, Role: at.Role, MediaType: at.MediaType, SizeBytes: at.SizeBytes, SHA256: at.SHA256, Ref: at.Ref}}
		_, _ = c.DescribeAttachment(context.Background(), a)
		assertRequestParity(t, *captured, run.AttachmentRequest(item, at, nil, "same"), false)
		g := connectors.UploadGrant{URL: "https://objects.example/put", Headers: map[string]string{"Content-Type": "image/png"}, SizeBytes: size, SHA256: at.SHA256, MediaType: at.MediaType, ExpiresAt: now.Add(time.Minute)}
		_ = c.UploadAttachment(context.Background(), a, g)
		grant := devhost.AttachmentGrant{URL: g.URL, Method: "PUT", Headers: g.Headers, SizeBytes: g.SizeBytes, SHA256: g.SHA256, MediaType: g.MediaType, ExpiresAt: g.ExpiresAt.Format(time.RFC3339)}
		assertRequestParity(t, *captured, run.AttachmentRequest(item, at, &grant, "same"), false)
	})
	t.Run("retrieval", func(t *testing.T) {
		pin, captured := capturePin(t, "retrieval-valid", nil)
		run, issues := devhost.BuildRetrievalRun(readFixture(t, "retrieval-valid", "wire.json"), &pin.Manifest)
		requireBuilt(t, issues, nil)
		q := run.Request(run.Profiles[0], "same")
		// The engine and devhost both drive this initial input through a session.
		engine := plugins.NewRetrievalSession(&pin.Manifest, q).Request()
		_, _ = (pluginhttp.Retriever{Pin: pin}).Round(context.Background(), engine)
		got := append([]byte(nil), (*captured)...)
		_, _ = devhost.DriveSearch(context.Background(), pin.Endpoint, &pin.Manifest, q, run.Serve)
		assertRequestParity(t, got, *captured, false)
	})
}

func capturePin(t *testing.T, name string, spaces map[string]string) (*plugins.Pin, *[]byte) {
	t.Helper()
	var pin *plugins.Pin
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/discovery" {
			_ = json.NewEncoder(w).Encode(map[string]any{"plugin_api": pin.PluginAPI(), "plugin": map[string]string{"id": pin.Manifest.ID, "version": pin.Manifest.Version}, "manifest_digest": pin.ManifestDigest, "contributions": pin.Manifest.Contributions.Names()})
			return
		}
		captured, _ = io.ReadAll(r.Body)
		w.WriteHeader(422)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "refused", "message": "fixture captured", "retryable": false, "error_class": "source"})
	}))
	t.Cleanup(server.Close)
	var err error
	pin, err = plugins.LoadPin(plugins.PinConfig{Manifest: filepath.Join("../../tests/plugin-contract", name, plugins.ManifestFile), Endpoint: server.URL, Spaces: spaces})
	if err != nil {
		t.Fatal(err)
	}
	return pin, &captured
}
func assertRequestParity(t *testing.T, got, want []byte, normalizer bool) {
	t.Helper()
	var a, b map[string]any
	decode(t, got, &a)
	decode(t, want, &b)
	for _, request := range []map[string]any{a, b} {
		delete(request, "invocation_id")
		delete(request, "idempotency_key")
		if normalizer {
			delete(request["input"].(map[string]any), "reference")
		}
	}
	if !reflect.DeepEqual(a, b) {
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		t.Fatalf("engine request %s\nContract Runner request %s", x, y)
	}
}
func readFixture(t *testing.T, name, file string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("../../tests/plugin-contract", name, "fixtures", file))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func decode(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
}
func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}
func requireBuilt(t *testing.T, issues []plugins.Issue, err error) {
	t.Helper()
	if err != nil || len(issues) > 0 {
		t.Fatalf("fixture: %v %v", issues, err)
	}
}
