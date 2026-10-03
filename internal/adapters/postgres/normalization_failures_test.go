package postgres_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/normalization"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost/fakeplugin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// faultyManifest declares a quick timeout and a single budgeted attempt so
// every failure class resolves in one invocation.
const faultyManifest = `id: acme.faulty
version: 1.0.0
compatibility:
  engine: ">=0.1.0 <0.3.0"
  plugin_api: ">=0.1.0 <0.2.0"
contributions:
  normalizer:
    media_types: [text/markdown, text/x-optional]
    timeout_ms: 1000
    retry:
      max_attempts: 1
`

// pluginWorld is one Organization with a Corpus and the real fake plugin
// process pinned for text/markdown (required) and text/x-optional (optional).
type pluginWorld struct {
	t        *testing.T
	ctx      context.Context
	pool     *pgxpool.Pool
	org      string
	scope    corpus.Scope
	corpusID string
	manifest string
	port     int
	proc     *devhost.Process
	pin      *plugins.Pin
	store    fixtureContentStores
	objects  *objectMemory
	contents content.Service
	service  normalization.Service
	sources  blobSources
	input    []byte
}

func newPluginWorld(t *testing.T, ctx context.Context, name string) *pluginWorld {
	t.Helper()
	pool := adapterPool(t, ctx)
	run := time.Now().UTC().Format("20060102T150405.000000000")
	w := &pluginWorld{t: t, ctx: ctx, pool: pool, org: "adapter-" + name + "-" + run, store: contentStores(pool), objects: &objectMemory{objects: map[string][]byte{}}}
	w.scope = corpus.Scope{Organization: w.org, Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, w.scope, corpus.CreateInput{Key: name, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	w.corpusID = c.ID
	w.manifest = filepath.Join(t.TempDir(), plugins.ManifestFile)
	if err := os.WriteFile(w.manifest, []byte(faultyManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if w.port, err = devhost.FreePort("127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	w.pin, err = plugins.LoadPin(plugins.PinConfig{Manifest: w.manifest, Endpoint: "http://127.0.0.1:" + strconv.Itoa(w.port), Routes: []plugins.RouteConfig{{MediaType: "text/markdown"}, {MediaType: "text/x-optional", Mode: plugins.RouteOptional}}})
	if err != nil {
		t.Fatal(err)
	}
	w.input = []byte("# Harbour\n\nThe tide turns twice a day.")
	w.sources = blobSources{}
	live := liveOf(t, w.pin)
	w.contents = content.Service{Submissions: w.store, Receipts: w.store, RecordStore: w.store, Versions: w.store, Materialization: w.store, Catalog: w.store, Blobs: w.objects, BlobSource: w.sources, Relations: w.store, Routes: live, Normalizations: w.store, Supersession: w.store}
	w.service = normalization.Service{Content: w.contents, Store: w.store, Signer: staticSigner{}, Plugin: pluginhttp.Normalizer{}, Pin: live}
	t.Cleanup(func() {
		if w.proc != nil {
			_ = w.proc.Stop(time.Second)
		}
	})
	return w
}

// liveOf follows a plan of pin alone, as api and worker follow theirs.
func liveOf(t *testing.T, pin *plugins.Pin) *plugins.Live {
	t.Helper()
	set, err := plugins.NewPinSet([]*plugins.Pin{pin})
	if err != nil {
		t.Fatal(err)
	}
	live, err := plugins.NewLive("", set)
	if err != nil {
		t.Fatal(err)
	}
	return live
}

// start runs the fake plugin in mode on the world's fixed port.
func (w *pluginWorld) start(mode string) {
	w.t.Helper()
	proc, err := devhost.Start(devhost.Options{Command: fakeplugin.Command(), Manifest: w.manifest, Port: w.port, Env: []string{fakeplugin.EnvEnable + "=1", fakeplugin.EnvMode + "=" + mode}})
	if err != nil {
		w.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(w.ctx, 15*time.Second)
	defer cancel()
	if err := proc.WaitHealthy(ctx); err != nil {
		w.t.Fatal(err)
	}
	w.proc = proc
}

// accept accepts a routed Blob of mediaType under recordKey.
func (w *pluginWorld) accept(recordKey, mediaType, position string) content.Receipt {
	w.t.Helper()
	data := append([]byte(recordKey+"\n"), w.input...)
	blob := content.VerifiedBlob{ID: "blob_" + recordKey, MediaType: mediaType, Blob: content.Blob{Key: w.org + "/inputs/" + recordKey, SHA256: content.Hash(data), Size: int64(len(data))}}
	w.objects.objects[blob.Blob.Key] = data
	w.sources[blob.ID] = blob
	receipt, err := w.contents.Accept(w.ctx, w.scope, content.Command{Key: "routed-" + recordKey + position, Source: content.Source{CorpusID: w.corpusID, Namespace: "docs", RecordKey: strings.SplitN(recordKey, "@", 2)[0]}, Position: position, Content: content.Text{Kind: "blob", BlobID: blob.ID, MediaType: mediaType}})
	if err != nil {
		w.t.Fatal(err)
	}
	return receipt
}

type blobSources map[string]content.VerifiedBlob

func (b blobSources) VerifiedBlob(_ context.Context, _ string, id string) (content.VerifiedBlob, error) {
	v, ok := b[id]
	if !ok {
		return content.VerifiedBlob{}, content.ErrUnverifiedBlob
	}
	return v, nil
}

func (w *pluginWorld) count(query string) int {
	w.t.Helper()
	var n int
	if err := w.pool.QueryRow(w.ctx, query, w.org).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func (w *pluginWorld) version(receiptID string) (content.Receipt, content.Version) {
	w.t.Helper()
	r, err := w.contents.Receipt(w.ctx, w.scope, receiptID)
	if err != nil || r.State != "resolved" {
		w.t.Fatalf("receipt %+v %v", r, err)
	}
	v, err := w.contents.Version(w.ctx, w.scope, r.RecordID, r.VersionID)
	if err != nil {
		w.t.Fatal(err)
	}
	return r, v
}

// TestPluginKilledMidInvocationConvergesOnOneManifest kills the plugin
// process while it holds an invocation, restarts it, and retries like the
// Temporal activity does: nothing is recorded by the lost invocation and the
// Version ends with exactly one published Manifest.
func TestPluginKilledMidInvocationConvergesOnOneManifest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	w := newPluginWorld(t, ctx, "normalizer-kill")
	w.start("hang")
	receipt := w.accept("guide", "text/markdown", "")

	done := make(chan error, 1)
	go func() { done <- w.service.Normalize(ctx, w.org, receipt.ID) }()
	time.Sleep(300 * time.Millisecond)
	_ = w.proc.Stop(0)
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "plugin_unavailable") {
			t.Fatalf("a killed plugin must be unavailability: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the invocation outlived the plugin process")
	}
	if n := w.count("SELECT count(*) FROM normalizations WHERE organization=$1"); n != 0 {
		t.Fatalf("the lost invocation recorded %d outcomes", n)
	}
	// While the plugin is down, retries stay unavailability and never count.
	if err := w.service.Normalize(ctx, w.org, receipt.ID); err == nil {
		t.Fatal("normalized without a plugin")
	}
	if n := w.count("SELECT count(*) FROM normalization_attempts WHERE organization=$1"); n != 0 {
		t.Fatalf("unavailability counted %d attempts", n)
	}

	w.start("ok")
	for i := 0; i < 3; i++ {
		if err := w.service.Normalize(ctx, w.org, receipt.ID); err != nil {
			t.Fatal(err)
		}
		if err := w.contents.Materialize(ctx, w.org, receipt.ID); err != nil {
			t.Fatal(err)
		}
	}
	if n := w.count("SELECT count(*) FROM normalizations WHERE organization=$1"); n != 1 {
		t.Fatalf("normalizations %d", n)
	}
	if n := w.count("SELECT count(*) FROM record_versions WHERE organization=$1"); n != 1 {
		t.Fatalf("versions %d", n)
	}
	r, v := w.version(receipt.ID)
	if r.Outcome != "created" || v.Availability.State == "quarantined" || len(v.Diagnostics) != 0 {
		t.Fatalf("receipt %+v version %+v", r, v)
	}
	if n, _ := v.Provenance["normalization"].(map[string]any); n["plugin_id"] != "acme.faulty" {
		t.Fatalf("provenance %+v", v.Provenance)
	}
}

// TestFailedNormalizationsQuarantineBeforeAnyCanonicalCommit drives every
// failure class through the real fake plugin: each publishes the submitted
// input Manifest quarantined, with the structured reason on the Version read
// and the Receipt, and a record.quarantined Change Event.
func TestFailedNormalizationsQuarantineBeforeAnyCanonicalCommit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	w := newPluginWorld(t, ctx, "normalizer-failures")
	w.start("by-record-key")
	for mode, code := range map[string]string{
		"terminal":             "normalizer_failed",
		"malformed-part":       "normalizer_invalid_output",
		"bad-checksum":         "normalizer_invalid_output",
		"undeclared-namespace": "normalizer_invalid_output",
		"large":                "normalizer_invalid_output",
		"invalid":              "normalizer_invalid_output",
		"slow":                 "normalizer_timeout",
		"retry":                "normalizer_retries_exhausted",
	} {
		t.Run(mode, func(t *testing.T) {
			receipt := w.accept(mode+".1", "text/markdown", "")
			if err := w.service.Normalize(ctx, w.org, receipt.ID); err != nil {
				t.Fatalf("a failure must record an outcome: %v", err)
			}
			if err := w.contents.Materialize(ctx, w.org, receipt.ID); err != nil {
				t.Fatal(err)
			}
			r, v := w.version(receipt.ID)
			if v.Availability.State != "quarantined" || v.Availability.Searchable || v.Processing.State != "blocked" {
				t.Fatalf("version %+v", v)
			}
			if len(v.Diagnostics) != 1 {
				t.Fatalf("diagnostics %+v", v.Diagnostics)
			}
			d := v.Diagnostics[0]
			if d.Code != code || d.Message == "" || d.Plugin != "acme.faulty" || d.Contribution != "normalizer" || !strings.HasPrefix(d.InvocationID, "inv_") {
				t.Fatalf("diagnostic %+v", d)
			}
			if d.Retryable != (code == "normalizer_timeout" || code == "normalizer_retries_exhausted") {
				t.Fatalf("retryable %+v", d)
			}
			// Nothing from the plugin: only the submitted input Blob Part.
			if len(v.Manifest.Parts) != 1 || v.Manifest.Parts[0].Content.Kind != "blob" || v.Provenance["normalization"] != nil {
				t.Fatalf("published %+v %+v", v.Manifest, v.Provenance)
			}
			if r.Outcome != "created" || len(r.Diagnostics) != 1 || r.Diagnostics[0].Code != code {
				t.Fatalf("receipt %+v", r)
			}
			var events int
			if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM change_events WHERE organization=$1 AND event_type='record.quarantined' AND resource_id=$2`, w.org, r.RecordID).Scan(&events); err != nil || events != 1 {
				t.Fatalf("record.quarantined events %d %v", events, err)
			}
			var parts int
			if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM version_parts WHERE organization=$1 AND version_id=$2`, w.org, r.VersionID).Scan(&parts); err != nil || parts != 0 {
				t.Fatalf("parts %d %v", parts, err)
			}
		})
	}

	t.Run("optional route falls back", func(t *testing.T) {
		receipt := w.accept("terminal.optional", "text/x-optional", "")
		if err := w.service.Normalize(ctx, w.org, receipt.ID); err != nil {
			t.Fatal(err)
		}
		if err := w.contents.Materialize(ctx, w.org, receipt.ID); err != nil {
			t.Fatal(err)
		}
		_, v := w.version(receipt.ID)
		if v.Availability.State == "quarantined" || len(v.Manifest.Parts) != 1 || v.Manifest.Parts[0].Role != "body" || !strings.Contains(v.Manifest.Parts[0].Content.Text, "The tide turns") {
			t.Fatalf("fallback version %+v", v)
		}
		n, _ := v.Provenance["normalization"].(map[string]any)
		fallback, _ := n["fallback"].(map[string]any)
		if fallback["code"] != "normalizer_failed" || n["invocation_id"] == nil {
			t.Fatalf("provenance %+v", v.Provenance)
		}
		if len(v.Diagnostics) != 1 || v.Diagnostics[0].Code != "normalizer_failed" || v.Diagnostics[0].InvocationID != n["invocation_id"] {
			t.Fatalf("diagnostics %+v", v.Diagnostics)
		}
	})

	t.Run("superseded version is not normalized", func(t *testing.T) {
		older := w.accept("ok.superseded@1", "text/markdown", "1")
		_ = w.accept("ok.superseded@2", "text/markdown", "2")
		if err := w.service.Normalize(ctx, w.org, older.ID); err != nil {
			t.Fatal(err)
		}
		if err := w.contents.Materialize(ctx, w.org, older.ID); err != nil {
			t.Fatal(err)
		}
		_, v := w.version(older.ID)
		if v.Availability.State != "quarantined" || len(v.Diagnostics) != 1 || v.Diagnostics[0].Code != "normalization_superseded" {
			t.Fatalf("version %+v", v)
		}
	})

	t.Run("conflict is recorded without overwrite", func(t *testing.T) {
		receipt := w.accept("ok.conflict", "text/markdown", "")
		if err := w.service.Normalize(ctx, w.org, receipt.ID); err != nil {
			t.Fatal(err)
		}
		r, _ := w.contents.Receipt(ctx, w.scope, receipt.ID)
		var versionID string
		_ = w.pool.QueryRow(ctx, `SELECT a.version_id FROM ingestion_receipts r JOIN accepted_revisions a ON (a.organization,a.record_id,a.slot)=(r.organization,r.record_id,r.slot) WHERE r.organization=$1 AND r.id=$2`, w.org, r.ID).Scan(&versionID)
		before, _, _ := w.store.Normalized(ctx, w.org, versionID)
		for _, c := range []content.NormalizationConflict{{InvocationID: "inv_second", ManifestSHA256: "a"}, {InvocationID: "inv_third", ManifestSHA256: "b"}} {
			if err := w.store.RecordConflict(ctx, w.org, versionID, c); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.contents.Materialize(ctx, w.org, receipt.ID); err != nil {
			t.Fatal(err)
		}
		after, _, _ := w.store.Normalized(ctx, w.org, versionID)
		if after.Manifest != before.Manifest || after.Conflict == nil || after.Conflict.InvocationID != "inv_second" {
			t.Fatalf("before %+v after %+v", before, after)
		}
		_, v := w.version(receipt.ID)
		if v.Availability.State == "quarantined" || len(v.Diagnostics) != 1 || v.Diagnostics[0].Code != "normalizer_conflict" || v.Diagnostics[0].InvocationID != "inv_second" {
			t.Fatalf("version %+v", v)
		}
	})

	t.Run("attempts are counted durably", func(t *testing.T) {
		for want := 1; want <= 3; want++ {
			got, err := w.store.CountAttempt(ctx, w.org, "version_counted", "normalizer_timeout", "inv_x")
			if err != nil || got != want {
				t.Fatalf("attempt %d: %d %v", want, got, err)
			}
		}
	})
}
