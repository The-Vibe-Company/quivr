package content_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

type stubRepository struct{ accepted content.Command }

func (s *stubRepository) Accept(_ context.Context, _ corpus.Scope, c content.Command) (content.Receipt, error) {
	s.accepted = c
	return content.Receipt{ID: "receipt_1", State: "pending"}, nil
}
func (*stubRepository) Withdraw(context.Context, corpus.Scope, content.Withdrawal) (content.Receipt, error) {
	return content.Receipt{ID: "receipt_withdrawal", State: "resolved", Outcome: "withdrawal_applied"}, nil
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
func (*stubRepository) Publish(context.Context, content.Work, content.Publication) error {
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

func manifestCommand() content.Command {
	return content.Command{
		Key:     "manifest-1",
		Source:  content.Source{CorpusID: "corpus", Namespace: "ns", RecordKey: "record"},
		Content: content.Text{Kind: "manifest"},
		Manifest: &content.Manifest{Kind: "manifest", Parts: []content.Part{
			{Key: "title", Role: "title", Content: content.Text{Kind: "text", Text: "Titre 🌞"}},
			{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: "Corps"}},
			{Key: "source", Role: "source", Content: content.Text{Kind: "blob", BlobID: "blob_1", MediaType: "application/xml"}},
		}, Relations: []content.Relation{{Type: "illustrated_by", Target: content.Source{CorpusID: "corpus", Namespace: "ns", RecordKey: "photo"}, SourceTargetRevision: "1"}}},
		Extensions: content.Extensions{"example.editorial": {SchemaVersion: "1", Data: map[string]any{"headline": "Titre", "subjects": []any{map[string]any{"code": "science", "score": 0.75}}}}},
	}
}

func manifestService() (*stubRepository, content.Service) {
	repo := &stubRepository{}
	return repo, content.Service{Repository: repo, Blobs: stubBlobs{}, BlobSource: stubSource{verified: content.VerifiedBlob{ID: "blob_1", MediaType: "application/xml", Blob: content.Blob{SHA256: "blob-checksum"}}}}
}

func TestManifestAcceptedWithVerifiedBlobPartsAndExtensions(t *testing.T) {
	repo, service := manifestService()
	if _, err := service.Accept(context.Background(), scope(), manifestCommand()); err != nil {
		t.Fatal(err)
	}
	if repo.accepted.Manifest == nil || len(repo.accepted.Manifest.Parts) != 3 || len(repo.accepted.Manifest.Relations) != 1 {
		t.Fatalf("manifest not preserved: %v", repo.accepted.Manifest)
	}
	if repo.accepted.Manifest.Parts[2].Content.BlobID != "blob_1" {
		t.Fatal("verified Blob Part reference lost")
	}
	if repo.accepted.Manifest.Parts[2].Content.BlobSHA256 != "blob-checksum" {
		t.Fatal("verified Blob Part checksum not preserved")
	}
	if repo.accepted.Extensions["example.editorial"].SchemaVersion != "1" {
		t.Fatalf("extension not preserved: %v", repo.accepted.Extensions)
	}
}

func TestManifestStructureRejections(t *testing.T) {
	cases := map[string]func(*content.Command){
		"duplicate key":     func(c *content.Command) { c.Manifest.Parts[1].Key = "title" },
		"unknown parent":    func(c *content.Command) { c.Manifest.Parts[1].ParentKey = "missing" },
		"self parent":       func(c *content.Command) { c.Manifest.Parts[1].ParentKey = "body" },
		"empty text":        func(c *content.Command) { c.Manifest.Parts[1].Content.Text = "" },
		"nul text":          func(c *content.Command) { c.Manifest.Parts[1].Content.Text = "bad\x00text" },
		"empty parts":       func(c *content.Command) { c.Manifest.Parts = nil },
		"missing relation":  func(c *content.Command) { c.Manifest.Relations[0].Target.RecordKey = "" },
		"unknown part kind": func(c *content.Command) { c.Manifest.Parts[1].Content.Kind = "manifest" },
	}
	for name, mutate := range cases {
		command := manifestCommand()
		mutate(&command)
		_, service := manifestService()
		if _, err := service.Accept(context.Background(), scope(), command); !errors.Is(err, content.ErrInvalid) && !errors.Is(err, content.ErrUnsupported) {
			t.Fatalf("%s: accepted (%v)", name, err)
		}
	}
	// A parent cycle is rejected even when every key exists.
	cycle := manifestCommand()
	cycle.Manifest.Parts[1].ParentKey = "source"
	cycle.Manifest.Parts[2].ParentKey = "body"
	_, service := manifestService()
	if _, err := service.Accept(context.Background(), scope(), cycle); !errors.Is(err, content.ErrInvalid) {
		t.Fatalf("cycle accepted: %v", err)
	}
}

func TestManifestRejectsUnverifiedBlobParts(t *testing.T) {
	cases := map[string]content.Service{
		"unknown blob":   {Repository: &stubRepository{}, Blobs: stubBlobs{}, BlobSource: stubSource{err: content.ErrUnverifiedBlob}},
		"media mismatch": {Repository: &stubRepository{}, Blobs: stubBlobs{}, BlobSource: stubSource{verified: content.VerifiedBlob{ID: "blob_1", MediaType: "image/png"}}},
		"no source":      {Repository: &stubRepository{}, Blobs: stubBlobs{}},
	}
	for name, service := range cases {
		if _, err := service.Accept(context.Background(), scope(), manifestCommand()); !errors.Is(err, content.ErrUnverifiedBlob) {
			t.Fatalf("%s: accepted (%v)", name, err)
		}
	}
}

func TestManifestRejectsUnverifiedSourceBlobIDs(t *testing.T) {
	forged := manifestCommand()
	forged.Provenance = map[string]any{"source_blob_ids": []any{"blob_missing"}}
	_, service := manifestService()
	if _, err := service.Accept(context.Background(), scope(), forged); !errors.Is(err, content.ErrUnverifiedBlob) {
		t.Fatalf("accepted forged source reference: %v", err)
	}
	verified := manifestCommand()
	verified.Provenance = map[string]any{"source_blob_ids": []any{"blob_1"}}
	_, service = manifestService()
	if _, err := service.Accept(context.Background(), scope(), verified); err != nil {
		t.Fatalf("verified source reference rejected: %v", err)
	}
}

func TestManifestStructuralBounds(t *testing.T) {
	many := manifestCommand()
	many.Manifest.Parts = []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: "x"}}}
	for i := 0; i < 300; i++ {
		many.Manifest.Parts = append(many.Manifest.Parts, content.Part{Key: fmt.Sprintf("part-%d", i), Role: "source", Content: content.Text{Kind: "text", Text: "x"}})
	}
	_, service := manifestService()
	if _, err := service.Accept(context.Background(), scope(), many); !errors.Is(err, content.ErrUnsupported) {
		t.Fatalf("oversized Part count accepted: %v", err)
	}
	oversized := manifestCommand()
	oversized.Manifest.Parts = []content.Part{{Key: strings.Repeat("k", 100000), Role: "body", Content: content.Text{Kind: "text", Text: "x"}}}
	_, service = manifestService()
	if _, err := service.Accept(context.Background(), scope(), oversized); !errors.Is(err, content.ErrUnsupported) {
		t.Fatalf("oversized Part structure accepted: %v", err)
	}
}

func TestExtensionSchemaValidation(t *testing.T) {
	valid := content.Extensions{"example.editorial": {SchemaVersion: "1", Data: map[string]any{"headline": "Titre", "subjects": []any{map[string]any{"code": "science", "score": 0.75}}, "flags": map[string]any{"urgent": true}, "extra": nil}}}
	if err := (content.BuiltinExtensions{}).Validate(context.Background(), valid); err != nil {
		t.Fatalf("declared schema rejected: %v", err)
	}
	invalid := map[string]content.Extensions{
		"unknown namespace": {"uninstalled": {SchemaVersion: "1", Data: map[string]any{}}},
		"unknown version":   {"example.editorial": {SchemaVersion: "2", Data: map[string]any{}}},
		"wrong data type":   {"example.editorial": {SchemaVersion: "1", Data: map[string]any{"headline": 42}}},
		"bad subject":       {"example.editorial": {SchemaVersion: "1", Data: map[string]any{"subjects": []any{map[string]any{"score": 0.5}}}}},
	}
	for name, exts := range invalid {
		if err := (content.BuiltinExtensions{}).Validate(context.Background(), exts); !errors.Is(err, content.ErrInvalid) && !errors.Is(err, content.ErrUnsupported) {
			t.Fatalf("%s: accepted (%v)", name, err)
		}
	}
}

func TestOversizedGenericJSONIsBounded(t *testing.T) {
	large := map[string]any{}
	for i := 0; i < 4096; i++ {
		large[content.StableID("k", string(rune('a'+i%26)), string(rune(i)))] = "value"
	}
	exts := content.Extensions{"example.editorial": {SchemaVersion: "1", Data: large}}
	if err := (content.BuiltinExtensions{}).Validate(context.Background(), exts); err != nil {
		t.Fatalf("schema validation ran before the bound: %v", err)
	}
	command := manifestCommand()
	command.Extensions = exts
	_, service := manifestService()
	if _, err := service.Accept(context.Background(), scope(), command); !errors.Is(err, content.ErrUnsupported) {
		t.Fatalf("oversized generic JSON accepted: %v", err)
	}
}

// A verification lookup that cannot complete is an infrastructure failure, not
// proof that the Blob is unverified: callers must be able to retry it.
func TestBlobVerificationOutageStaysRetryable(t *testing.T) {
	outage := stubSource{err: context.DeadlineExceeded}
	withSourceBlob := manifestCommand()
	withSourceBlob.Manifest.Parts = withSourceBlob.Manifest.Parts[:2]
	withSourceBlob.Provenance = map[string]any{"source_blob_ids": []any{"blob_1"}}
	cases := map[string]content.Command{
		"Blob content":         blobCommand(),
		"Manifest Blob Part":   manifestCommand(),
		"provenance Blob refs": withSourceBlob,
	}
	for name, command := range cases {
		service := content.Service{Repository: &stubRepository{}, Blobs: stubBlobs{}, BlobSource: outage}
		_, err := service.Accept(context.Background(), scope(), command)
		if errors.Is(err, content.ErrUnverifiedBlob) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s: outage reported as %v", name, err)
		}
	}
}

// TestCheckManifestIsTheEngineStructuralRule proves the exported check applies
// the same structural rules as acceptance, with actionable messages, and runs
// the per-Part hook in Part order before later structural failures.
func TestCheckManifestIsTheEngineStructuralRule(t *testing.T) {
	base := func() *content.Manifest {
		return &content.Manifest{Kind: "manifest", Parts: []content.Part{
			{Key: "doc", Role: "document", Content: content.Text{Kind: "text", Text: "a"}},
			{Key: "page", ParentKey: "doc", Role: "page", Content: content.Text{Kind: "text", Text: "b"}},
		}}
	}
	if err := content.CheckManifest(base(), nil); err != nil {
		t.Fatalf("valid Manifest rejected: %v", err)
	}
	dup := base()
	dup.Parts[1].Key = "doc"
	dup.Parts[1].ParentKey = ""
	detail := func(err error) string {
		var v *content.ManifestViolation
		if !errors.As(err, &v) {
			t.Fatalf("not a ManifestViolation: %v", err)
		}
		return v.Detail
	}
	err := content.CheckManifest(dup, nil)
	if !errors.Is(err, content.ErrInvalid) || err.Error() != "invalid_input" || !strings.Contains(detail(err), `"doc"`) {
		t.Fatalf("duplicate key: %v", err)
	}
	cycle := base()
	cycle.Parts[0].ParentKey = "page"
	if err := content.CheckManifest(cycle, nil); !errors.Is(err, content.ErrInvalid) || !strings.Contains(detail(err), "cycle") {
		t.Fatalf("cycle: %v", err)
	}
	hookErr := errors.New("hook")
	var seen []int
	err = content.CheckManifest(dup, func(i int) error {
		seen = append(seen, i)
		return hookErr
	})
	if !errors.Is(err, hookErr) || len(seen) != 1 || seen[0] != 0 {
		t.Fatalf("hook precedence changed: err=%v seen=%v", err, seen)
	}
}
