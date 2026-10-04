package content_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

type stubRepository struct {
	accepted    content.Command
	newRevision bool
}

func (s *stubRepository) Accept(_ context.Context, _ corpus.Scope, c content.Command) (content.Receipt, error) {
	s.accepted = c
	return content.Receipt{ID: "receipt_1", State: "pending", NewRevision: s.newRevision}, nil
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

// The usage view counts a document received only when its command reserved
// a new revision: a replay or a revision the Record already had is not a
// new document (THE-798).
func TestAcceptObservesOnlyNewRevisions(t *testing.T) {
	for _, c := range []struct {
		newRevision bool
		want        []string
	}{{true, []string{"org_a/news-feed"}}, {false, nil}} {
		var received []string
		service := content.Service{Submissions: &stubRepository{newRevision: c.newRevision}, Receipts: &stubRepository{newRevision: c.newRevision}, RecordStore: &stubRepository{newRevision: c.newRevision}, Versions: &stubRepository{newRevision: c.newRevision}, Materialization: &stubRepository{newRevision: c.newRevision}, Received: func(org, namespace string) { received = append(received, org+"/"+namespace) }}
		command := content.Command{Key: "k", Source: content.Source{CorpusID: "corpus", Namespace: "news-feed", RecordKey: "r"}, Content: content.Text{Kind: "text", Text: "Bonjour"}}
		if _, err := service.Accept(context.Background(), scope(), command); err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(received) != fmt.Sprint(c.want) {
			t.Fatalf("new revision %v: observed %v; want %v", c.newRevision, received, c.want)
		}
	}
}

func TestBlobContentResolvesToImmutableText(t *testing.T) {
	repo := &stubRepository{}
	service := content.Service{
		Submissions: repo, Receipts: repo, RecordStore: repo, Versions: repo, Materialization: repo,
		Blobs:      stubBlobs{data: []byte("Bonjour 🌞")},
		BlobSource: stubSource{verified: content.VerifiedBlob{ID: "blob_1", Blob: content.Blob{Key: "obj", SHA256: content.Hash([]byte("Bonjour 🌞")), Size: int64(len("Bonjour 🌞"))}, MediaType: "text/html"}},
	}
	if _, err := service.Accept(context.Background(), scope(), func() content.Command { c := blobCommand(); c.Content.MediaType = "text/html"; return c }()); err != nil {
		t.Fatal(err)
	}
	if repo.accepted.SourceMediaType != "text/html" {
		t.Fatalf("accepted source media type = %q, want text/html after Blob resolution", repo.accepted.SourceMediaType)
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
	text := []byte("Bonjour")
	verified := func(media string, data []byte) content.VerifiedBlob {
		return content.VerifiedBlob{ID: "blob_1", MediaType: media, Blob: content.Blob{Key: "obj", SHA256: content.Hash(data), Size: int64(len(data))}}
	}
	for name, tc := range map[string]struct {
		media  string
		source stubSource
		stored []byte
		want   error
	}{
		"unknown blob":      {"text/plain", stubSource{err: content.ErrUnverifiedBlob}, text, content.ErrUnverifiedBlob},
		"non-text media":    {"image/png", stubSource{verified: verified("image/png", text)}, text, content.ErrUnverifiedBlob},
		"media mismatch":    {"text/plain", stubSource{verified: verified("text/csv", text)}, text, content.ErrUnverifiedBlob},
		"empty blob":        {"text/plain", stubSource{verified: verified("text/plain", nil)}, nil, content.ErrUnsupported},
		"altered bytes":     {"text/plain", stubSource{verified: verified("text/plain", text)}, []byte("Bonsoir"), content.ErrUnverifiedBlob},
		"invalid utf8 text": {"text/plain", stubSource{verified: verified("text/plain", []byte{0xff, 0xfe})}, []byte{0xff, 0xfe}, content.ErrInvalid},
	} {
		command := blobCommand()
		command.Content.MediaType = tc.media
		service := content.Service{Submissions: &stubRepository{}, Receipts: &stubRepository{}, RecordStore: &stubRepository{}, Versions: &stubRepository{}, Materialization: &stubRepository{}, Blobs: stubBlobs{data: tc.stored}, BlobSource: tc.source}
		if _, err := service.Accept(context.Background(), scope(), command); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
	}
}

func TestInlineTextRejectsUnverifiedBlobReferences(t *testing.T) {
	repo := &stubRepository{}
	service := content.Service{Submissions: repo, Receipts: repo, RecordStore: repo, Versions: repo, Materialization: repo, Blobs: stubBlobs{}, BlobSource: stubSource{}}
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
	return repo, content.Service{Submissions: repo, Receipts: repo, RecordStore: repo, Versions: repo, Materialization: repo, Blobs: stubBlobs{}, BlobSource: stubSource{verified: content.VerifiedBlob{ID: "blob_1", MediaType: "application/xml", Blob: content.Blob{SHA256: "blob-checksum"}}}}
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

// Each structural rejection is a ManifestViolation: its error is the bare
// public code, so logs never echo submitted keys, and its Detail names what is
// wrong for the Plugin Contract Runner.
func TestManifestStructureRejections(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*content.Command)
		want   error
		detail string
	}{
		"duplicate key":     {func(c *content.Command) { c.Manifest.Parts[1].Key = "title" }, content.ErrInvalid, `duplicate Part key "title"`},
		"unknown parent":    {func(c *content.Command) { c.Manifest.Parts[1].ParentKey = "missing" }, content.ErrInvalid, `unknown parent "missing"`},
		"self parent":       {func(c *content.Command) { c.Manifest.Parts[1].ParentKey = "body" }, content.ErrInvalid, `"body" is its own parent`},
		"empty text":        {func(c *content.Command) { c.Manifest.Parts[1].Content.Text = "" }, content.ErrInvalid, `"body" text`},
		"nul text":          {func(c *content.Command) { c.Manifest.Parts[1].Content.Text = "bad\x00text" }, content.ErrInvalid, `"body" text`},
		"empty parts":       {func(c *content.Command) { c.Manifest.Parts = nil }, content.ErrInvalid, "at least one Part"},
		"missing relation":  {func(c *content.Command) { c.Manifest.Relations[0].Target.RecordKey = "" }, content.ErrInvalid, "Relation 0"},
		"unknown part kind": {func(c *content.Command) { c.Manifest.Parts[1].Content.Kind = "manifest" }, content.ErrUnsupported, `content kind "manifest"`},
		// A parent cycle is rejected even when every key exists.
		"parent cycle": {func(c *content.Command) {
			c.Manifest.Parts[1].ParentKey = "source"
			c.Manifest.Parts[2].ParentKey = "body"
		}, content.ErrInvalid, "parent cycle"},
	} {
		command := manifestCommand()
		tc.mutate(&command)
		_, service := manifestService()
		_, err := service.Accept(context.Background(), scope(), command)
		var violation *content.ManifestViolation
		if !errors.Is(err, tc.want) || !errors.As(err, &violation) {
			t.Errorf("%s: got %v, want a ManifestViolation of %v", name, err, tc.want)
			continue
		}
		if err.Error() != tc.want.Error() || !strings.Contains(violation.Detail, tc.detail) {
			t.Errorf("%s: error %q with detail %q; want %q naming %s", name, err, violation.Detail, tc.want, tc.detail)
		}
	}
}

func TestManifestRejectsUnverifiedBlobParts(t *testing.T) {
	cases := map[string]content.Service{
		"unknown blob":   {Submissions: &stubRepository{}, Receipts: &stubRepository{}, RecordStore: &stubRepository{}, Versions: &stubRepository{}, Materialization: &stubRepository{}, Blobs: stubBlobs{}, BlobSource: stubSource{err: content.ErrUnverifiedBlob}},
		"media mismatch": {Submissions: &stubRepository{}, Receipts: &stubRepository{}, RecordStore: &stubRepository{}, Versions: &stubRepository{}, Materialization: &stubRepository{}, Blobs: stubBlobs{}, BlobSource: stubSource{verified: content.VerifiedBlob{ID: "blob_1", MediaType: "image/png"}}},
		"no source":      {Submissions: &stubRepository{}, Receipts: &stubRepository{}, RecordStore: &stubRepository{}, Versions: &stubRepository{}, Materialization: &stubRepository{}, Blobs: stubBlobs{}},
	}
	for name, service := range cases {
		if _, err := service.Accept(context.Background(), scope(), manifestCommand()); !errors.Is(err, content.ErrUnverifiedBlob) {
			t.Fatalf("%s: accepted (%v)", name, err)
		}
	}
	// A Blob Part is verified right after its own structural checks, so it is
	// reported before a structural failure of a later Part.
	later := manifestCommand()
	later.Manifest.Parts = append(later.Manifest.Parts, content.Part{Key: "title", Role: "title", Content: content.Text{Kind: "text", Text: "again"}})
	if _, err := cases["unknown blob"].Accept(context.Background(), scope(), later); !errors.Is(err, content.ErrUnverifiedBlob) {
		t.Fatalf("unverified Blob Part before a later duplicate key: %v", err)
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
	for name, tc := range map[string]struct {
		exts content.Extensions
		want error
	}{
		"unknown namespace": {content.Extensions{"uninstalled": {SchemaVersion: "1", Data: map[string]any{}}}, content.ErrUnsupported},
		"unknown version":   {content.Extensions{"example.editorial": {SchemaVersion: "2", Data: map[string]any{}}}, content.ErrUnsupported},
		"wrong data type":   {content.Extensions{"example.editorial": {SchemaVersion: "1", Data: map[string]any{"headline": 42}}}, content.ErrInvalid},
		"bad subject":       {content.Extensions{"example.editorial": {SchemaVersion: "1", Data: map[string]any{"subjects": []any{map[string]any{"score": 0.5}}}}}, content.ErrInvalid},
	} {
		if err := (content.BuiltinExtensions{}).Validate(context.Background(), tc.exts); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
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
	withSourceBlob := manifestCommand()
	withSourceBlob.Manifest.Parts = withSourceBlob.Manifest.Parts[:2]
	withSourceBlob.Provenance = map[string]any{"source_blob_ids": []any{"blob_1"}}
	cases := map[string]content.Command{
		"Blob content":         blobCommand(),
		"Manifest Blob Part":   manifestCommand(),
		"provenance Blob refs": withSourceBlob,
	}
	// An expired deadline and a client that went away mid-batch both leave the
	// Blob unjudged: the command stays retryable, never unverified_blob.
	for _, outage := range []error{context.DeadlineExceeded, context.Canceled} {
		for name, command := range cases {
			service := content.Service{Submissions: &stubRepository{}, Receipts: &stubRepository{}, RecordStore: &stubRepository{}, Versions: &stubRepository{}, Materialization: &stubRepository{}, Blobs: stubBlobs{}, BlobSource: stubSource{err: outage}}
			_, err := service.Accept(context.Background(), scope(), command)
			if errors.Is(err, content.ErrUnverifiedBlob) || !errors.Is(err, outage) {
				t.Fatalf("%s (%v): outage reported as %v", name, outage, err)
			}
		}
	}
}
