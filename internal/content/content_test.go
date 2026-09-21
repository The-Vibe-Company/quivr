package content_test

import (
	"context"
	"errors"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

type stubRepository struct{ accepted content.Command }

func (s *stubRepository) Accept(_ context.Context, _ corpus.Scope, c content.Command) (content.Receipt, error) {
	s.accepted = c
	return content.Receipt{ID: "receipt_1", State: "pending"}, nil
}
func (*stubRepository) Receipt(context.Context, string, string) (content.Receipt, error) {
	return content.Receipt{}, nil
}
func (*stubRepository) Record(context.Context, string, string) (content.Record, error) {
	return content.Record{}, nil
}
func (*stubRepository) Version(context.Context, string, string, string) (content.StoredVersion, error) {
	return content.StoredVersion{}, nil
}
func (*stubRepository) Work(context.Context, string, string) (content.Work, bool, error) {
	return content.Work{}, false, nil
}
func (*stubRepository) Progress(context.Context, string, string, string, string) error { return nil }
func (*stubRepository) Publish(context.Context, content.Work, content.Blob, content.Blob) error {
	return nil
}

type stubBlobs struct {
	data []byte
	err  error
}

func (s stubBlobs) Put(context.Context, string, []byte) (content.Blob, error) {
	return content.Blob{}, nil
}
func (s stubBlobs) Read(context.Context, content.Blob) ([]byte, error) { return s.data, s.err }

type stubSource struct {
	verified content.VerifiedBlob
	err      error
}

func (s stubSource) VerifiedBlob(context.Context, string, string) (content.VerifiedBlob, error) {
	return s.verified, s.err
}

func blobCommand() content.Command {
	return content.Command{
		Key:     "uploaded",
		Source:  content.Source{CorpusID: "corpus", Namespace: "ns", RecordKey: "record"},
		Content: content.Text{Kind: "blob", BlobID: "blob_1", MediaType: "text/plain"},
	}
}

func scope() corpus.Scope {
	return corpus.Scope{Organization: "org_a", Actions: []string{"content:write", "content:read"}, Corpora: []string{"*"}}
}

func TestBlobContentResolvesToImmutableText(t *testing.T) {
	repo := &stubRepository{}
	service := content.Service{
		Repository: repo,
		Blobs:      stubBlobs{data: []byte("Bonjour 🌞")},
		BlobSource: stubSource{verified: content.VerifiedBlob{ID: "blob_1", Blob: content.Blob{Key: "obj", SHA256: content.Hash([]byte("Bonjour 🌞")), Size: int64(len("Bonjour 🌞"))}, MediaType: "text/plain"}},
	}
	if _, err := service.Accept(context.Background(), scope(), blobCommand()); err != nil {
		t.Fatal(err)
	}
	if repo.accepted.Content.Kind != "text" || repo.accepted.Content.Text != "Bonjour 🌞" {
		t.Fatalf("blob did not resolve to text: %v", repo.accepted.Content)
	}
	if repo.accepted.Content.BlobID != "" {
		t.Fatal("resolved command retained a Blob reference")
	}
	ids, ok := repo.accepted.Provenance["source_blob_ids"].([]any)
	if !ok || len(ids) != 1 || ids[0] != "blob_1" {
		t.Fatalf("verified Blob provenance missing: %v", repo.accepted.Provenance)
	}
}

func TestBlobContentRejections(t *testing.T) {
	cases := map[string]content.Service{
		"unknown blob":      {Repository: &stubRepository{}, Blobs: stubBlobs{data: []byte("x")}, BlobSource: stubSource{err: content.ErrUnverifiedBlob}},
		"non-text media":    {Repository: &stubRepository{}, Blobs: stubBlobs{data: []byte("x")}, BlobSource: stubSource{verified: content.VerifiedBlob{MediaType: "image/png"}}},
		"media mismatch":    {Repository: &stubRepository{}, Blobs: stubBlobs{data: []byte("x")}, BlobSource: stubSource{verified: content.VerifiedBlob{MediaType: "text/csv"}}},
		"invalid utf8 text": {Repository: &stubRepository{}, Blobs: stubBlobs{data: []byte{0xff, 0xfe}}, BlobSource: stubSource{verified: content.VerifiedBlob{MediaType: "text/plain"}}},
	}
	for name, service := range cases {
		_, err := service.Accept(context.Background(), scope(), blobCommand())
		if !errors.Is(err, content.ErrUnverifiedBlob) && !errors.Is(err, content.ErrInvalid) && err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func TestInlineTextRejectsUnverifiedBlobReferences(t *testing.T) {
	repo := &stubRepository{}
	service := content.Service{Repository: repo, Blobs: stubBlobs{}, BlobSource: stubSource{}}
	command := blobCommand()
	command.Content = content.Text{Kind: "text", Text: "inline"}
	command.Provenance = map[string]any{"source_blob_ids": []any{"blob_1"}}
	if _, err := service.Accept(context.Background(), scope(), command); !errors.Is(err, content.ErrUnsupported) {
		t.Fatalf("accepted unverified source_blob_ids: %v", err)
	}
}
