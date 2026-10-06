package content_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

type routes map[string]bool

func (r routes) Routed(_ context.Context, mediaType string) bool { return r[mediaType] }

var markdownBytes = []byte("# Title\n\nBody")

func markdownBlob() content.VerifiedBlob {
	return content.VerifiedBlob{ID: "blob_md", MediaType: "text/markdown", Blob: content.Blob{Key: "org/obj", SHA256: content.Hash(markdownBytes), Size: int64(len(markdownBytes))}}
}

func routedCommand(mediaType string) content.Command {
	return content.Command{
		Key:     "routed",
		Source:  content.Source{CorpusID: "corpus", Namespace: "ns", RecordKey: "record"},
		Content: content.Text{Kind: "blob", BlobID: "blob_md", MediaType: mediaType},
	}
}

// failingBlobs proves a routed Blob is never read at acceptance.
type failingBlobs struct{}

func (failingBlobs) Put(context.Context, string, []byte) (content.Blob, error) {
	return content.Blob{}, errors.New("unexpected put")
}
func (failingBlobs) Read(context.Context, content.Blob) ([]byte, error) {
	return nil, errors.New("a routed Blob must not be read at acceptance")
}

func TestRoutedBlobIsAcceptedFromTheSubmittedInput(t *testing.T) {
	repo := &stubRepository{}
	service := content.Service{Submissions: repo, Receipts: repo, RecordStore: repo, Versions: repo, Materialization: repo, Blobs: failingBlobs{}, BlobSource: stubSource{verified: markdownBlob()}, Routes: routes{"text/markdown": true}}
	if _, err := service.Accept(context.Background(), scope(), routedCommand("text/markdown")); err != nil {
		t.Fatal(err)
	}
	got := repo.accepted
	if got.Content.Kind != "blob" || got.Content.BlobID != "blob_md" || got.Content.MediaType != "text/markdown" || got.Content.BlobSHA256 != content.Hash(markdownBytes) || got.Content.Text != "" {
		t.Fatalf("routed Blob not kept as a verified reference: %+v", got.Content)
	}
	if ids, _ := got.Provenance["source_blob_ids"].([]any); len(ids) != 1 || ids[0] != "blob_md" {
		t.Fatalf("provenance %+v", got.Provenance)
	}
	// Identity derives from the submitted input: another checksum is another digest,
	// and a text submission of the same bytes is a different identity.
	other := got
	other.Content.BlobSHA256 = content.Hash([]byte("other"))
	if content.Digest(got) == content.Digest(other) {
		t.Fatal("the digest ignores the input checksum")
	}
	withExtensions := got
	withExtensions.Extensions = content.Extensions{"core.source": {SchemaVersion: "1", Data: map[string]any{"x": 1}}}
	if content.Digest(got) == content.Digest(withExtensions) {
		t.Fatal("the digest ignores submitted extensions")
	}
	if content.Digest(got) != content.Digest(got) {
		t.Fatal("digest not deterministic")
	}
}

func TestUnroutedBlobsKeepTheExistingPath(t *testing.T) {
	source := markdownBlob()
	source.MediaType = "application/octet-stream"
	service := content.Service{Submissions: &stubRepository{}, Receipts: &stubRepository{}, RecordStore: &stubRepository{}, Versions: &stubRepository{}, Materialization: &stubRepository{}, Blobs: failingBlobs{}, BlobSource: stubSource{verified: source}, Routes: routes{"text/markdown": true}}
	if _, err := service.Accept(context.Background(), scope(), routedCommand("application/octet-stream")); !errors.Is(err, content.ErrUnverifiedBlob) {
		t.Fatalf("unrouted non-text Blob: %v", err)
	}
	// An unrouted text Blob still resolves to inline text.
	repo := &stubRepository{}
	plain := markdownBlob()
	plain.MediaType = "text/plain"
	service = content.Service{Submissions: repo, Receipts: repo, RecordStore: repo, Versions: repo, Materialization: repo, Blobs: stubBlobs{data: markdownBytes}, BlobSource: stubSource{verified: plain}, Routes: routes{"text/markdown": true}}
	if _, err := service.Accept(context.Background(), scope(), routedCommand("text/plain")); err != nil {
		t.Fatal(err)
	}
	if repo.accepted.Content.Kind != "text" || repo.accepted.Content.Text != string(markdownBytes) {
		t.Fatalf("unrouted text Blob %+v", repo.accepted.Content)
	}
}

func TestRoutedBlobRejections(t *testing.T) {
	mismatch := markdownBlob()
	mismatch.MediaType = "text/plain"
	outage := errors.New("database unavailable")
	for name, tc := range map[string]struct {
		source stubSource
		want   error
	}{
		"unverified":          {stubSource{err: content.ErrUnverifiedBlob}, content.ErrUnverifiedBlob},
		"media type mismatch": {stubSource{verified: mismatch}, content.ErrUnverifiedBlob},
		// An outage leaves the Blob unjudged: it stays retryable, never unverified_blob.
		"outage": {stubSource{err: outage}, outage},
	} {
		t.Run(name, func(t *testing.T) {
			service := content.Service{Submissions: &stubRepository{}, Receipts: &stubRepository{}, RecordStore: &stubRepository{}, Versions: &stubRepository{}, Materialization: &stubRepository{}, Blobs: failingBlobs{}, BlobSource: tc.source, Routes: routes{"text/markdown": true}}
			_, err := service.Accept(context.Background(), scope(), routedCommand("text/markdown"))
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestClientsCannotWriteNormalizationProvenance(t *testing.T) {
	c := content.Command{Key: "k", Source: content.Source{CorpusID: "corpus", Namespace: "ns", RecordKey: "r"}, Content: content.Text{Kind: "text", Text: "x"}, Provenance: map[string]any{"normalization": map[string]any{"plugin_id": "forged"}}}
	service := content.Service{Submissions: &stubRepository{}, Receipts: &stubRepository{}, RecordStore: &stubRepository{}, Versions: &stubRepository{}, Materialization: &stubRepository{}}
	if _, err := service.Accept(context.Background(), scope(), c); !errors.Is(err, content.ErrInvalid) {
		t.Fatalf("got %v", err)
	}
}

type workRepository struct {
	stubRepository
	work         content.Work
	published    []content.Work
	publications []content.Publication
	progress     []string
}

func (r *workRepository) Work(context.Context, string, string) (content.Work, bool, error) {
	return r.work, false, nil
}
func (r *workRepository) Progress(_ context.Context, _, _, state, code string) error {
	r.progress = append(r.progress, state+":"+code)
	return nil
}
func (r *workRepository) Publish(_ context.Context, w content.Work, p content.Publication) error {
	r.published = append(r.published, w)
	r.publications = append(r.publications, p)
	return nil
}

type memoryBlobs map[string][]byte

func (m memoryBlobs) Put(_ context.Context, org string, data []byte) (content.Blob, error) {
	b := content.Blob{Key: org + "/" + content.Hash(data), SHA256: content.Hash(data), Size: int64(len(data))}
	m[b.Key] = data
	return b, nil
}
func (m memoryBlobs) Read(_ context.Context, b content.Blob) ([]byte, error) {
	data, ok := m[b.Key]
	if !ok {
		return nil, content.ErrArtifactMissing
	}
	return data, nil
}

type normalizations map[string]content.Normalized

func (n normalizations) Normalized(_ context.Context, _, versionID string) (content.Normalized, bool, error) {
	v, ok := n[versionID]
	return v, ok, nil
}

func TestMaterializePublishesTheNormalizedManifestWithProvenance(t *testing.T) {
	blobs := memoryBlobs{}
	manifest := content.Manifest{Kind: "manifest", Parts: []content.Part{{Key: "title", Role: "title", Content: content.Text{Kind: "text", Text: "Title"}}, {Key: "section-1", Role: "section", Content: content.Text{Kind: "text", Text: "Body"}}}}
	raw, _ := json.Marshal(manifest)
	stored, _ := blobs.Put(context.Background(), "org_a", raw)
	command := routedCommand("text/markdown")
	command.Content.BlobSHA256 = content.Hash(markdownBytes)
	command.Provenance = map[string]any{"source_blob_ids": []any{"blob_md"}, "producer": "client"}
	repo := &workRepository{work: content.Work{Organization: "org_a", ReceiptID: "receipt_1", VersionID: "version_1", Command: command}}
	provenance := content.Normalization{PluginID: "acme.markdown", PluginVersion: "1.0.0", PluginAPI: "0.1.0", Contribution: "normalizer", InvocationID: "inv_1", IdempotencyKey: "key", InputSHA256: content.Hash(markdownBytes)}
	service := content.Service{Submissions: repo, Receipts: repo, RecordStore: repo, Versions: repo, Materialization: repo, Blobs: blobs, Normalizations: normalizations{"version_1": {Manifest: stored, Provenance: provenance}}}
	if err := service.Materialize(context.Background(), "org_a", "receipt_1"); err != nil {
		t.Fatal(err)
	}
	if len(repo.published) != 1 {
		t.Fatalf("published %d", len(repo.published))
	}
	got := repo.published[0].Command.Provenance
	if got["producer"] != "client" || got["source_blob_ids"].([]any)[0] != "blob_md" {
		t.Fatalf("acquirer provenance changed: %+v", got)
	}
	n, ok := got["normalization"].(map[string]any)
	if !ok || n["plugin_id"] != "acme.markdown" || n["invocation_id"] != "inv_1" || n["input_sha256"] != content.Hash(markdownBytes) || n["contribution"] != "normalizer" {
		t.Fatalf("normalization provenance %+v", got)
	}
	if _, leaked := command.Provenance["normalization"]; leaked {
		t.Fatal("the accepted Command was mutated")
	}
	// The published Manifest and Part text come from the normalizer output.
	var found bool
	for _, data := range blobs {
		if string(data) == "Title\nBody" {
			found = true
		}
	}
	if !found {
		t.Fatal("normalized text not published")
	}
}

// Plugin output extensions are published on the Version beside the submitted
// ones; the accepted Command is not mutated.
func TestMaterializePublishesNormalizerExtensions(t *testing.T) {
	blobs := memoryBlobs{}
	manifest := content.Manifest{Kind: "manifest", Parts: []content.Part{{Key: "section-1", Role: "body", Content: content.Text{Kind: "text", Text: "Body"}}}}
	raw, _ := json.Marshal(manifest)
	stored, _ := blobs.Put(context.Background(), "org_a", raw)
	command := routedCommand("text/markdown")
	command.Content.BlobSHA256 = content.Hash(markdownBytes)
	command.Extensions = content.Extensions{
		"example.editorial": {SchemaVersion: "1", Data: map[string]any{"headline": "Submitted"}},
		"quivr.metadata":    {SchemaVersion: "1", Data: map[string]any{"language": "en", "author": []any{"Submitted author"}}},
	}
	repo := &workRepository{work: content.Work{Organization: "org_a", ReceiptID: "receipt_1", VersionID: "version_1", Command: command}}
	produced := content.Extensions{
		"acme.markdown.outline": {SchemaVersion: "1", Data: map[string]any{"heading_count": float64(1)}},
		"quivr.metadata":        {SchemaVersion: "1", Data: map[string]any{"author": []any{"Normalized author"}}},
	}
	service := content.Service{Submissions: repo, Receipts: repo, RecordStore: repo, Versions: repo, Materialization: repo, Blobs: blobs, Normalizations: normalizations{"version_1": {Manifest: stored, Provenance: content.Normalization{PluginID: "acme.markdown"}, Extensions: produced}}}
	if err := service.Materialize(context.Background(), "org_a", "receipt_1"); err != nil {
		t.Fatal(err)
	}
	got := repo.published[0].Command.Extensions
	if len(got) != 3 || got["example.editorial"].Data["headline"] != "Submitted" || got["acme.markdown.outline"].Data["heading_count"] != float64(1) || got["quivr.metadata"].Data["language"] != "en" || got["quivr.metadata"].Data["author"].([]any)[0] != "Normalized author" {
		t.Fatalf("published extensions %+v", got)
	}
	if len(command.Extensions) != 2 || len(repo.work.Command.Extensions) != 2 || command.Extensions["quivr.metadata"].Data["author"].([]any)[0] != "Submitted author" {
		t.Fatal("the accepted Command was mutated")
	}
}

func TestMaterializeWaitsForNormalization(t *testing.T) {
	command := routedCommand("text/markdown")
	repo := &workRepository{work: content.Work{Organization: "org_a", ReceiptID: "receipt_1", VersionID: "version_1", Command: command}}
	service := content.Service{Submissions: repo, Receipts: repo, RecordStore: repo, Versions: repo, Materialization: repo, Blobs: memoryBlobs{}, Routes: routes{"text/markdown": true}, Normalizations: normalizations{}}
	if err := service.Materialize(context.Background(), "org_a", "receipt_1"); err == nil {
		t.Fatal("published without a normalized Manifest")
	}
	if len(repo.published) != 0 {
		t.Fatal("published")
	}
}

type supersession struct{ withdrawn, superseded bool }

func (s supersession) Superseded(context.Context, string, string, string) (bool, bool, error) {
	return s.withdrawn, s.superseded, nil
}

func routedWork() *workRepository {
	command := routedCommand("text/markdown")
	command.Content.BlobSHA256 = content.Hash(markdownBytes)
	command.Provenance = map[string]any{"source_blob_ids": []any{"blob_md"}, "producer": "client"}
	return &workRepository{work: content.Work{Organization: "org_a", ReceiptID: "receipt_1", RecordID: "record_1", VersionID: "version_1", Command: command}}
}

// A failed normalization publishes the submitted input Manifest quarantined
// with the recorded reason; nothing from the plugin is published.
func TestMaterializeQuarantinesAFailedNormalization(t *testing.T) {
	repo := routedWork()
	failed := content.Normalized{Outcome: content.OutcomeFailed, InputBlobID: "blob_md",
		Provenance: content.Normalization{PluginID: "acme.markdown", PluginVersion: "1.0.0", PluginAPI: "0.1.0", Contribution: "normalizer", InvocationID: "inv_9", IdempotencyKey: "key", InputSHA256: content.Hash(markdownBytes)},
		Failure:    &content.NormalizationFailure{Code: "normalizer_invalid_output", Message: "duplicate Part key"}}
	service := content.Service{Submissions: repo, Receipts: repo, RecordStore: repo, Versions: repo, Materialization: repo, Blobs: memoryBlobs{}, Routes: routes{"text/markdown": true}, Normalizations: normalizations{"version_1": failed}}
	if err := service.Materialize(context.Background(), "org_a", "receipt_1"); err != nil {
		t.Fatal(err)
	}
	if len(repo.publications) != 1 {
		t.Fatalf("published %d", len(repo.publications))
	}
	q := repo.publications[0].Quarantine
	want := content.Diagnostic{Code: "normalizer_invalid_output", Message: "duplicate Part key", Plugin: "acme.markdown", Contribution: "normalizer", InvocationID: "inv_9"}
	if q == nil || *q != want {
		t.Fatalf("quarantine %+v", q)
	}
	if len(repo.publications[0].Parts) != 0 {
		t.Fatalf("plugin output published: %+v", repo.publications[0].Parts)
	}
	if _, ok := repo.published[0].Command.Provenance["normalization"]; ok {
		t.Fatal("a failed invocation is published as normalization provenance")
	}
}

func TestMaterializePublishesAFallbackWithItsProvenance(t *testing.T) {
	repo := routedWork()
	blobs := memoryBlobs{}
	raw, _ := json.Marshal(content.ManifestFor(content.Command{Content: content.Text{Kind: "text", Text: string(markdownBytes)}}))
	stored, _ := blobs.Put(context.Background(), "org_a", raw)
	fallback := content.Normalized{Outcome: content.OutcomeFallback, Manifest: stored,
		Provenance: content.Normalization{PluginID: "acme.markdown", PluginVersion: "1.0.0", PluginAPI: "0.1.0", Contribution: "normalizer", InvocationID: "inv_2", IdempotencyKey: "key", InputSHA256: content.Hash(markdownBytes), Fallback: &content.NormalizationFallback{Code: "normalizer_failed", Message: "cannot read"}},
		Failure:    &content.NormalizationFailure{Code: "normalizer_failed", Message: "cannot read"}}
	service := content.Service{Submissions: repo, Receipts: repo, RecordStore: repo, Versions: repo, Materialization: repo, Blobs: blobs, Routes: routes{"text/markdown": true}, Normalizations: normalizations{"version_1": fallback}}
	if err := service.Materialize(context.Background(), "org_a", "receipt_1"); err != nil {
		t.Fatal(err)
	}
	p := repo.publications[0]
	if p.Quarantine != nil || len(p.Parts) != 1 || p.Parts[0].Role != "body" {
		t.Fatalf("fallback publication %+v", p)
	}
	n := repo.published[0].Command.Provenance["normalization"].(map[string]any)
	if f, _ := n["fallback"].(map[string]any); f["code"] != "normalizer_failed" || n["invocation_id"] != "inv_2" {
		t.Fatalf("fallback provenance %+v", n)
	}
}

func TestMaterializeWithoutAnOutcome(t *testing.T) {
	for name, tc := range map[string]struct {
		supersession supersession
		routes       routes
		mediaType    string
		quarantine   string
		builtin      bool
		conflict     bool
	}{
		"withdrawn":           {supersession: supersession{withdrawn: true}, routes: routes{"text/markdown": true}, mediaType: "text/markdown", conflict: true},
		"superseded":          {supersession: supersession{superseded: true}, routes: routes{"text/markdown": true}, mediaType: "text/markdown", quarantine: "normalization_superseded"},
		"route removed, text": {routes: routes{}, mediaType: "text/markdown", builtin: true},
		"route removed, pdf":  {routes: routes{}, mediaType: "application/pdf", quarantine: "normalizer_unrouted"},
	} {
		t.Run(name, func(t *testing.T) {
			repo := routedWork()
			repo.work.Command.Content.MediaType = tc.mediaType
			source := markdownBlob()
			source.MediaType = tc.mediaType
			service := content.Service{Submissions: repo, Receipts: repo, RecordStore: repo, Versions: repo, Materialization: repo, Blobs: stubBlobs{data: markdownBytes}, BlobSource: stubSource{verified: source}, Routes: tc.routes, Normalizations: normalizations{}, Supersession: tc.supersession}
			if err := service.Materialize(context.Background(), "org_a", "receipt_1"); err != nil {
				t.Fatal(err)
			}
			if len(repo.publications) != 1 {
				t.Fatalf("published %d", len(repo.publications))
			}
			p := repo.publications[0]
			switch {
			case tc.conflict:
				if p.Manifest != (content.Blob{}) || p.Quarantine != nil {
					t.Fatalf("withdrawn publication %+v", p)
				}
			case tc.quarantine != "":
				if p.Quarantine == nil || p.Quarantine.Code != tc.quarantine || p.Quarantine.Message == "" || len(p.Parts) != 0 {
					t.Fatalf("publication %+v", p)
				}
			case tc.builtin:
				if p.Quarantine != nil || len(p.Parts) != 1 || p.Parts[0].Role != "body" {
					t.Fatalf("publication %+v", p)
				}
			}
		})
	}
}

var _ corpus.Scope
