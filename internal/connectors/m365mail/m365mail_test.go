package m365mail

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

var now = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

const cfgJSON = `{"tenant_id":"00000000-0000-0000-0000-000000000000","mailbox":"monitoring@example.org","backfill_since":"2026-09-28T09:00:00Z"}`
const secretJSON = `{"client_id":"11111111-1111-1111-1111-111111111111","client_secret":"test-secret-not-real"}`

func fetch(t *testing.T, c *Connector, checkpoint json.RawMessage) (connectors.Page, error) {
	t.Helper()
	return c.Fetch(context.Background(), connectors.FetchRequest{Config: json.RawMessage(cfgJSON), Credential: json.RawMessage(secretJSON), Checkpoint: checkpoint, Now: now})
}

func typed(t *testing.T, err error) *connectors.Error {
	t.Helper()
	var e *connectors.Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v is not a typed connector error", err)
	}
	return e
}

func TestSchemasAcceptSecretOrCertificateAndBoundTheBackfill(t *testing.T) {
	registry, err := connectors.NewRegistry(New("https://login.invalid", "https://graph.invalid/v1.0", nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Lookup("m365_mail"); !ok {
		t.Fatal("kind not registered")
	}
	c := New("", "", nil)
	if c.DefaultInterval() != time.Minute {
		t.Fatal("mail polls every minute by default")
	}
	if err = c.CheckConfig(json.RawMessage(cfgJSON), now); err != nil {
		t.Fatalf("backfill within 7 days: %v", err)
	}
	if err = c.CheckConfig(json.RawMessage(`{"tenant_id":"t","mailbox":"m","backfill_since":"2026-09-20T00:00:00Z"}`), now); err == nil {
		t.Fatal("a backfill older than 7 days must be refused")
	}
	if err = c.CheckConfig(json.RawMessage(`{"tenant_id":"t","mailbox":"m"}`), now); err != nil {
		t.Fatalf("backfill is optional: %v", err)
	}
}

func TestInitialDeltaStartsAtTheBackfillAndPagesUntilTheDeltaLink(t *testing.T) {
	g := newFakeGraph(t)
	g.addMessage("old", "2026-09-28T08:00:00Z")
	g.addMessage("m1", "2026-09-28T10:00:00Z")
	g.addMessage("m2", "2026-09-28T10:01:00Z")
	g.addMessage("m3", "2026-09-28T10:02:00Z")
	c := g.connector()
	page, err := fetch(t, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !page.More || len(page.Items) != 2 || page.Items[0].RecordKey != "<m1@example.org>" {
		t.Fatalf("first page %+v", page)
	}
	page, err = fetch(t, c, page.Checkpoint)
	if err != nil || page.More || len(page.Items) != 1 || page.Items[0].RecordKey != "<m3@example.org>" {
		t.Fatalf("second page %+v %v", page, err)
	}
	if !strings.Contains(string(page.Checkpoint), "deltatoken") {
		t.Fatalf("round end must checkpoint the delta link: %s", page.Checkpoint)
	}
	// Incremental round: only the new mail.
	g.addMessage("m4", "2026-09-28T11:00:00Z")
	page, err = fetch(t, c, page.Checkpoint)
	if err != nil || len(page.Items) != 1 || page.Items[0].RecordKey != "<m4@example.org>" {
		t.Fatalf("incremental %+v %v", page, err)
	}
	if g.tokens != 1 {
		t.Fatalf("token requested %d times; it must be cached", g.tokens)
	}
}

func TestDeletedOrMovedMailsAreIgnored(t *testing.T) {
	g := newFakeGraph(t)
	g.addMessage("m1", "2026-09-28T10:00:00Z")
	g.removed = []string{"gone"}
	page, err := fetch(t, g.connector(), nil)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("%+v %v", page, err)
	}
	for _, item := range page.Items {
		if item.Withdraw {
			t.Fatal("a deleted mail must not withdraw its Record")
		}
	}
}

func TestAnExpiredDeltaTokenResyncsTheWindowFromTheStart(t *testing.T) {
	g := newFakeGraph(t)
	g.pageSize = 10
	g.addMessage("m1", "2026-09-28T10:00:00Z")
	c := g.connector()
	page, err := fetch(t, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	g.expireTok = true
	page, err = fetch(t, c, page.Checkpoint)
	if err != nil {
		t.Fatalf("expiry must resync, not fail: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].RecordKey != "<m1@example.org>" || page.Items[0].Revision == "" {
		t.Fatalf("resync items %+v", page.Items)
	}
	var cp checkpoint
	_ = json.Unmarshal(page.Checkpoint, &cp)
	if !cp.Since.Equal(time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("resync window start %v", cp.Since)
	}
}

func TestAMessageMapsToTitleBodyOriginalAndAttachmentParts(t *testing.T) {
	g := newFakeGraph(t)
	g.pageSize = 10
	g.addMessage("m1", "2026-09-28T10:00:00Z",
		fileAttachment("a1", "report.pdf", "application/pdf", "%PDF-1.7 report", 0),
		fileAttachment("a2", "huge.zip", "application/zip", "", 26<<20),
		fakeAttachment{meta: map[string]any{"@odata.type": "#microsoft.graph.referenceAttachment", "id": "a3", "name": "link", "size": 10}})
	page, err := fetch(t, g.connector(), nil)
	if err != nil {
		t.Fatal(err)
	}
	item := page.Items[0]
	if item.Manifest == nil || len(item.Manifest.Parts) != 2 {
		t.Fatalf("manifest %+v", item.Manifest)
	}
	title, body := item.Manifest.Parts[0], item.Manifest.Parts[1]
	if title.Role != "title" || title.Content.Text != "Subject m1" || body.Role != "body" || body.Content.Text != "Hello m1" {
		t.Fatalf("title %+v body %+v", title, body)
	}
	if len(item.Attachments) != 2 || item.Attachments[0].Role != "original_body" || item.Attachments[0].MediaType != "text/html" || item.Attachments[1].MediaType != "application/pdf" {
		t.Fatalf("attachments %+v", item.Attachments)
	}
	rc, err := item.Attachments[1].Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "%PDF-1.7 report" {
		t.Fatalf("attachment bytes %q", b)
	}
	if item.Attachments[1].Extensions[AttachmentExtension].Data["name"] != "report.pdf" {
		t.Fatalf("attachment extension %+v", item.Attachments[1].Extensions)
	}
	ext := item.Extensions[MailExtension]
	if ext.SchemaVersion != "1" || ext.Data["conversation_id"] != "conv-m1" || ext.Data["internet_message_id"] != "<m1@example.org>" {
		t.Fatalf("extension %+v", ext)
	}
	skipped, _ := ext.Data["attachments_skipped"].([]any)
	reasons := map[string]bool{}
	for _, s := range skipped {
		reasons[s.(map[string]any)["reason"].(string)] = true
	}
	if !reasons["too_large"] || !reasons["reference_attachment"] {
		t.Fatalf("skipped %+v", skipped)
	}
}

func TestRevisionIgnoresReadStateButFollowsContent(t *testing.T) {
	msg := message{ID: "m1", InternetMessageID: "<m1@example.org>", Subject: "S", ReceivedDateTime: "2026-09-28T10:00:00Z"}
	msg.Body.ContentType, msg.Body.Content = "text", "Hello"
	a := revision(msg, nil)
	msg.IsRead = true
	if revision(msg, nil) != a {
		t.Fatal("reading a mail must not create a new Version")
	}
	msg.Body.Content = "Hello, corrected"
	if revision(msg, nil) == a {
		t.Fatal("changed content must produce a new revision")
	}
}

func TestRecordKeyFallsBackToTheImmutableGraphID(t *testing.T) {
	if k := recordKey(message{ID: "AAMk-immutable"}); k != "graph:AAMk-immutable" {
		t.Fatal(k)
	}
}

func TestHTMLBecomesReadableText(t *testing.T) {
	got := htmlToText(`<html><head><style>p{}</style><title>t</title></head><body><h1>Titre</h1><p>Première&nbsp;ligne<br>deuxième</p><ul><li>un</li><li>deux</li></ul><script>alert(1)</script></body></html>`)
	want := "Titre\nPremière ligne\ndeuxième\nun\ndeux"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestAccessFailuresAreTypedAccessErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		fragment string
		f        failure
		tokenErr string
		code     string
	}{
		"mailbox outside the access scope": {fragment: "/messages/delta", f: failure{status: 403, code: "ErrorAccessDenied"}, code: "mailbox_access_denied"},
		"unknown mailbox":                  {fragment: "/messages/delta", f: failure{status: 404, code: "ErrorInvalidUser"}, code: "mailbox_not_found"},
		"unknown folder":                   {fragment: "/messages/delta", f: failure{status: 404, code: "ErrorItemNotFound"}, code: "folder_not_found"},
		"expired secret":                   {tokenErr: "7000222", code: "secret_expired"},
		"wrong secret":                     {tokenErr: "7000215", code: "invalid_client_credential"},
		"unknown application":              {tokenErr: "700016", code: "app_not_found"},
		"unknown tenant":                   {tokenErr: "90002", code: "tenant_not_found"},
	} {
		t.Run(name, func(t *testing.T) {
			g := newFakeGraph(t)
			g.tokenErr = tc.tokenErr
			if tc.fragment != "" {
				g.failNext(tc.fragment, tc.f)
			}
			_, err := fetch(t, g.connector(), nil)
			if e := typed(t, err); e.Class != connectors.ClassAccess || e.Code != tc.code {
				t.Fatalf("got %+v want access/%s", e, tc.code)
			}
		})
	}
}

func TestThrottlingHonoursRetryAfter(t *testing.T) {
	g := newFakeGraph(t)
	g.addMessage("m1", "2026-09-28T10:00:00Z")
	c := g.connector()
	var slept []time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	g.failNext("/messages/delta", failure{status: 429, code: "TooManyRequests", retryAfter: "3"})
	page, err := fetch(t, c, nil)
	if err != nil || len(page.Items) != 1 || len(slept) != 1 || slept[0] != 3*time.Second {
		t.Fatalf("items %d err %v slept %v", len(page.Items), err, slept)
	}
	// A long Retry-After ends the run and carries the delay to the scheduler.
	g.failNext("/messages/delta", failure{status: 429, retryAfter: "120"})
	_, err = fetch(t, c, nil)
	if e := typed(t, err); e.Class != connectors.ClassTransient || e.Code != "throttled" || e.RetryAfter != 120*time.Second {
		t.Fatalf("%+v", e)
	}
	g.failNext("/messages/delta", failure{status: 503}, failure{status: 503}, failure{status: 503})
	_, err = fetch(t, c, nil)
	if e := typed(t, err); e.Class != connectors.ClassTransient || e.Code != "source_unavailable" {
		t.Fatalf("%+v", e)
	}
}

func TestAnExpiredAccessTokenIsRenewedOnce(t *testing.T) {
	g := newFakeGraph(t)
	g.addMessage("m1", "2026-09-28T10:00:00Z")
	c := g.connector()
	if _, err := fetch(t, c, nil); err != nil {
		t.Fatal(err)
	}
	g.failNext("/messages/delta", failure{status: 401, code: "InvalidAuthenticationToken"})
	if _, err := fetch(t, c, nil); err != nil || g.tokens != 2 {
		t.Fatalf("err %v tokens %d", err, g.tokens)
	}
}

func TestARotatedSecretGetsItsOwnToken(t *testing.T) {
	g := newFakeGraph(t)
	c := g.connector()
	if _, err := fetch(t, c, nil); err != nil {
		t.Fatal(err)
	}
	g.secret = "rotated-secret-not-real"
	_, err := c.Fetch(context.Background(), connectors.FetchRequest{Config: json.RawMessage(cfgJSON), Credential: json.RawMessage(`{"client_id":"11111111-1111-1111-1111-111111111111","client_secret":"rotated-secret-not-real"}`), Now: now})
	if err != nil || g.tokens != 2 {
		t.Fatalf("err %v tokens %d", err, g.tokens)
	}
}

func TestCheckpointLinksOutsideTheGraphEndpointAreNotFollowed(t *testing.T) {
	g := newFakeGraph(t)
	g.addMessage("m1", "2026-09-28T10:00:00Z")
	page, err := fetch(t, g.connector(), json.RawMessage(`{"link":"https://attacker.invalid/steal","since":"2026-09-28T09:00:00Z"}`))
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("%+v %v", page, err)
	}
}

func TestCertificateCredentialSignsAVerifiableAssertion(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "quivr-test"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	cred, _ := json.Marshal(map[string]string{"client_id": "11111111-1111-1111-1111-111111111111",
		"certificate_pem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		"private_key_pem": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))})
	g := newFakeGraph(t)
	if _, err := g.connector().Fetch(context.Background(), connectors.FetchRequest{Config: json.RawMessage(cfgJSON), Credential: cred, Now: now}); err != nil {
		t.Fatal(err)
	}
	if g.form.Get("client_assertion_type") != "urn:ietf:params:oauth:client-assertion-type:jwt-bearer" || g.form.Get("client_secret") != "" {
		t.Fatalf("form %v", g.form)
	}
	parts := strings.Split(g.form.Get("client_assertion"), ".")
	if len(parts) != 3 {
		t.Fatal("assertion is not a JWT")
	}
	var header, claims map[string]any
	h, _ := base64.RawURLEncoding.DecodeString(parts[0])
	c, _ := base64.RawURLEncoding.DecodeString(parts[1])
	_ = json.Unmarshal(h, &header)
	_ = json.Unmarshal(c, &claims)
	thumb := sha256.Sum256(der)
	if header["alg"] != "PS256" || header["x5t#S256"] != base64.RawURLEncoding.EncodeToString(thumb[:]) {
		t.Fatalf("header %v", header)
	}
	if claims["iss"] != "11111111-1111-1111-1111-111111111111" || claims["sub"] != claims["iss"] || !strings.HasSuffix(claims["aud"].(string), "/00000000-0000-0000-0000-000000000000/oauth2/v2.0/token") || claims["jti"] == "" {
		t.Fatalf("claims %v", claims)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPSS(&key.PublicKey, crypto.SHA256, digest[:], sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}); err != nil {
		t.Fatalf("assertion signature: %v", err)
	}
}

func TestErrorsNeverCarryTheSecretOrToken(t *testing.T) {
	g := newFakeGraph(t)
	g.tokenErr = "7000222"
	_, err := fetch(t, g.connector(), nil)
	if strings.Contains(err.Error(), "test-secret") || strings.Contains(err.Error(), "token-") {
		t.Fatalf("error leaks a secret: %v", err)
	}
}

func TestMappedExtensionsValidateAgainstTheDeclaredSchemas(t *testing.T) {
	g := newFakeGraph(t)
	g.pageSize = 10
	g.addMessage("m1", "2026-09-28T10:00:00Z", fileAttachment("a1", "r.pdf", "application/pdf", "%PDF", 0),
		fileAttachment("a2", "huge.zip", "application/zip", "", 26<<20))
	page, err := fetch(t, g.connector(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Round-trip through JSON as the ingestion path stores it.
	var exts content.Extensions
	raw, _ := json.Marshal(page.Items[0].Extensions)
	_ = json.Unmarshal(raw, &exts)
	if err = (content.BuiltinExtensions{}).Validate(context.Background(), exts); err != nil {
		t.Fatalf("mail extension: %v", err)
	}
	raw, _ = json.Marshal(page.Items[0].Attachments[1].Extensions)
	_ = json.Unmarshal(raw, &exts)
	if err = (content.BuiltinExtensions{}).Validate(context.Background(), exts); err != nil {
		t.Fatalf("attachment extension: %v", err)
	}
}

func TestHeaderStringsAreCleanedAndRecipientListsBounded(t *testing.T) {
	msg := message{ID: "m1", InternetMessageID: "<m1@example.org>", Subject: "Alerte\x00 urgente", ReceivedDateTime: "2026-09-28T10:00:00Z"}
	msg.Body.ContentType, msg.Body.Content = "text", "Hello"
	for i := 0; i < 250; i++ {
		var a address
		a.EmailAddress.Name, a.EmailAddress.Address = "Nom\x00", "r@example.org"
		msg.To = append(msg.To, a)
	}
	item := session{c: New("", "", nil), cfg: config{Folder: "inbox"}}.mapMessage(msg, nil)
	data := item.Extensions[MailExtension].Data
	raw, _ := json.Marshal(data)
	if strings.Contains(string(raw), `\u0000`) {
		t.Fatal("a NUL byte reached the header extension")
	}
	if len(data["to"].([]any)) != maxRecipients || data["to_count"] != 250 {
		t.Fatalf("to %d count %v", len(data["to"].([]any)), data["to_count"])
	}
	var exts content.Extensions
	_ = json.Unmarshal(mustJSON(item.Extensions), &exts)
	if err := (content.BuiltinExtensions{}).Validate(context.Background(), exts); err != nil {
		t.Fatal(err)
	}
}

func TestAnAttachmentSkippedWhileStreamingIsListedWithItsReason(t *testing.T) {
	g := newFakeGraph(t)
	g.addMessage("m1", "2026-09-28T10:00:00Z", fileAttachment("a1", "r.pdf", "application/pdf", "%PDF", 0))
	page, err := fetch(t, g.connector(), nil)
	if err != nil {
		t.Fatal(err)
	}
	item := page.Items[0]
	item.Attachments[1].Skip("too_large")
	skipped := item.Extensions[MailExtension].Data["attachments_skipped"].([]any)
	if len(skipped) != 1 || skipped[0].(map[string]any)["name"] != "r.pdf" {
		t.Fatalf("skipped %v", skipped)
	}
}

func TestAStoredLinkOutsideTheEndpointRestartsWithinSevenDays(t *testing.T) {
	g := newFakeGraph(t)
	page, err := fetch(t, g.connector(), json.RawMessage(`{"link":"https://elsewhere.invalid/x","since":"2026-01-01T00:00:00Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	var cp checkpoint
	_ = json.Unmarshal(page.Checkpoint, &cp)
	if cp.Since.Before(now.Add(-MaxBackfill)) {
		t.Fatalf("restart window %v exceeds 7 days", cp.Since)
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
