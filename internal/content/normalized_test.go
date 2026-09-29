package content_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

type routes map[string]bool

func (r routes) Routed(mediaType string) bool { return r[mediaType] }

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
	service := content.Service{Repository: repo, Blobs: failingBlobs{}, BlobSource: stubSource{verified: markdownBlob()}, Routes: routes{"text/markdown": true}}
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
	service := content.Service{Repository: &stubRepository{}, Blobs: failingBlobs{}, BlobSource: stubSource{verified: source}, Routes: routes{"text/markdown": true}}
	if _, err := service.Accept(context.Background(), scope(), routedCommand("application/octet-stream")); !errors.Is(err, content.ErrUnverifiedBlob) {
		t.Fatalf("unrouted non-text Blob: %v", err)
	}
	// An unrouted text Blob still resolves to inline text.
	repo := &stubRepository{}
	plain := markdownBlob()
	plain.MediaType = "text/plain"
	service = content.Service{Repository: repo, Blobs: stubBlobs{data: markdownBytes}, BlobSource: stubSource{verified: plain}, Routes: routes{"text/markdown": true}}
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
	for name, tc := range map[string]struct {
		source stubSource
		want   error
	}{
		"unverified":          {stubSource{err: content.ErrUnverifiedBlob}, content.ErrUnverifiedBlob},
		"media type mismatch": {stubSource{verified: mismatch}, content.ErrUnverifiedBlob},
		"outage":              {stubSource{err: errors.New("database unavailable")}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			service := content.Service{Repository: &stubRepository{}, Blobs: failingBlobs{}, BlobSource: tc.source, Routes: routes{"text/markdown": true}}
			_, err := service.Accept(context.Background(), scope(), routedCommand("text/markdown"))
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestClientsCannotWriteNormalizationProvenance(t *testing.T) {
	c := content.Command{Key: "k", Source: content.Source{CorpusID: "corpus", Namespace: "ns", RecordKey: "r"}, Content: content.Text{Kind: "text", Text: "x"}, Provenance: map[string]any{"normalization": map[string]any{"plugin_id": "forged"}}}
	service := content.Service{Repository: &stubRepository{}}
	if _, err := service.Accept(context.Background(), scope(), c); !errors.Is(err, content.ErrInvalid) {
		t.Fatalf("got %v", err)
	}
}

type workRepository struct {
	stubRepository
	work      content.Work
	published []content.Work
	progress  []string
}

func (r *workRepository) Work(context.Context, string, string) (content.Work, bool, error) {
	return r.work, false, nil
}
func (r *workRepository) Progress(_ context.Context, _, _, state, code string) error {
	r.progress = append(r.progress, state+":"+code)
	return nil
}
func (r *workRepository) Publish(_ context.Context, w content.Work, _ content.Publication) error {
	r.published = append(r.published, w)
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
	service := content.Service{Repository: repo, Blobs: blobs, Normalizations: normalizations{"version_1": {Manifest: stored, Provenance: provenance}}}
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

func TestMaterializeWaitsForNormalization(t *testing.T) {
	command := routedCommand("text/markdown")
	repo := &workRepository{work: content.Work{Organization: "org_a", ReceiptID: "receipt_1", VersionID: "version_1", Command: command}}
	service := content.Service{Repository: repo, Blobs: memoryBlobs{}, Normalizations: normalizations{}}
	if err := service.Materialize(context.Background(), "org_a", "receipt_1"); err == nil {
		t.Fatal("published without a normalized Manifest")
	}
	if len(repo.published) != 0 {
		t.Fatal("published")
	}
}

var _ corpus.Scope
