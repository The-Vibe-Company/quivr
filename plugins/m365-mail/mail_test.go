package main

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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/sdks/go/quivrplugin"
)

var now = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

const cfgJSON = `{"tenant_id":"00000000-0000-0000-0000-000000000000","mailbox":"monitoring@example.org","backfill_since":"2026-09-28T09:00:00Z"}`
const secretJSON = `{"client_id":"11111111-1111-1111-1111-111111111111","client_secret":"test-secret-not-real"}`

// request decodes a protocol request the way the SDK does, so the
// credential is a quivrplugin.Credential.
func request(t *testing.T, into any, g *fakeGraph, config, credential string, checkpoint json.RawMessage, extra map[string]any) {
	t.Helper()
	if checkpoint == nil {
		checkpoint = json.RawMessage("null")
	}
	body := map[string]any{"invocation_id": "inv", "contribution": "connector", "organization_id": "org_a", "configuration": g.configuration(),
		"connector":  map[string]any{"instance_id": "connector_1", "kind": Kind, "config": json.RawMessage(config)},
		"credential": json.RawMessage(credential), "checkpoint": checkpoint, "now": now.Format(time.RFC3339), "page_in_run": 0, "reads_today": 0}
	for k, v := range extra {
		body[k] = v
	}
	raw, _ := json.Marshal(body)
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatal(err)
	}
}

func fetchWith(t *testing.T, g *fakeGraph, c *Mail, config, credential string, checkpoint json.RawMessage) (*quivrplugin.Page, error) {
	t.Helper()
	var r quivrplugin.FetchRequest
	request(t, &r, g, config, credential, checkpoint, nil)
	return c.Fetch(context.Background(), &r)
}

func fetch(t *testing.T, g *fakeGraph, c *Mail, checkpoint json.RawMessage) (*quivrplugin.Page, error) {
	t.Helper()
	return fetchWith(t, g, c, cfgJSON, secretJSON, checkpoint)
}

func checkpointOf(t *testing.T, p *quivrplugin.Page) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(p.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func typed(t *testing.T, err error) *quivrplugin.Error {
	t.Helper()
	var e *quivrplugin.Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v is not a classified plugin error", err)
	}
	return e
}

// The kind's schemas and defaults are the manifest's; the plugin serves them.
func TestTheManifestDeclaresTheKindAndItsAttachments(t *testing.T) {
	p, err := quivrplugin.New("quivr-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m := p.Manifest()
	kind := m.Connector.Kinds[Kind]
	if m.ID != "connector.m365_mail" || kind.DefaultIntervalSeconds != 60 || m.Connector.Attachments == nil || m.Connector.Attachments.MaxBytes != MaxAttachmentBytes {
		t.Fatalf("manifest %+v", m)
	}
	if _, err := p.MustConnector(Kind, New(nil)).Handler(); err != nil {
		t.Fatal(err)
	}
}

// Collection starts at most 7 days back: a first poll with an older
// backfill_since is refused as a configuration error, and nothing is read.
func TestABackfillOlderThanSevenDaysIsRefusedAtTheFirstPoll(t *testing.T) {
	g := newFakeGraph(t)
	old := `{"tenant_id":"t","mailbox":"m","backfill_since":"2026-09-20T00:00:00Z"}`
	_, err := fetchWith(t, g, g.connector(), old, secretJSON, nil)
	if e := typed(t, err); e.Class != quivrplugin.ClassSource || e.Code != "invalid_config" || g.tokens != 0 {
		t.Fatalf("%+v (tokens %d)", e, g.tokens)
	}
	// A later poll keeps collecting from its checkpoint.
	if _, err := fetchWith(t, g, g.connector(), old, secretJSON, json.RawMessage(`{"since":"2026-09-20T00:00:00Z"}`)); err != nil {
		t.Fatalf("an aged window must not stop a running instance: %v", err)
	}
	if _, err := fetchWith(t, g, g.connector(), `{"tenant_id":"t","mailbox":"m"}`, secretJSON, nil); err != nil {
		t.Fatalf("backfill is optional: %v", err)
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
			_, err := fetch(t, g, g.connector(), nil)
			if e := typed(t, err); e.Class != quivrplugin.ClassAccess || e.Code != tc.code {
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
	page, err := fetch(t, g, c, nil)
	if err != nil || len(page.Items) != 1 || len(slept) != 1 || slept[0] != 3*time.Second {
		t.Fatalf("items %v err %v slept %v", page, err, slept)
	}
	// A long Retry-After ends the run and carries the delay to the scheduler.
	g.failNext("/messages/delta", failure{status: 429, retryAfter: "120"})
	_, err = fetch(t, g, c, nil)
	if e := typed(t, err); e.Class != quivrplugin.ClassTransient || e.Code != "throttled" || e.RetryAfter != 120*time.Second {
		t.Fatalf("%+v", e)
	}
	g.failNext("/messages/delta", failure{status: 503}, failure{status: 503}, failure{status: 503})
	_, err = fetch(t, g, c, nil)
	if e := typed(t, err); e.Class != quivrplugin.ClassTransient || e.Code != "source_unavailable" {
		t.Fatalf("%+v", e)
	}
}

func TestAnAccessTokenIsCachedRenewedOnceAndPerSecret(t *testing.T) {
	g := newFakeGraph(t)
	g.addMessage("m1", "2026-09-28T10:00:00Z")
	c := g.connector()
	if _, err := fetch(t, g, c, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := fetch(t, g, c, nil); err != nil || g.tokens != 1 {
		t.Fatalf("err %v tokens %d; the token must be cached", err, g.tokens)
	}
	g.failNext("/messages/delta", failure{status: 401, code: "InvalidAuthenticationToken"})
	if _, err := fetch(t, g, c, nil); err != nil || g.tokens != 2 {
		t.Fatalf("err %v tokens %d; a refused token is renewed once", err, g.tokens)
	}
	g.secret = "rotated-secret-not-real"
	if _, err := fetchWith(t, g, c, cfgJSON, `{"client_id":"11111111-1111-1111-1111-111111111111","client_secret":"rotated-secret-not-real"}`, nil); err != nil || g.tokens != 3 {
		t.Fatalf("err %v tokens %d; a rotated secret gets its own token", err, g.tokens)
	}
}

func TestCheckCredentialAsksForAToken(t *testing.T) {
	g := newFakeGraph(t)
	c := g.connector()
	var r quivrplugin.CredentialRequest
	request(t, &r, g, cfgJSON, secretJSON, nil, nil)
	if _, err := c.CheckCredential(context.Background(), &r); err != nil || g.tokens != 1 {
		t.Fatalf("err %v tokens %d", err, g.tokens)
	}
	g.tokenErr = "7000222"
	g.tokens = 0
	c = g.connector()
	if _, err := c.CheckCredential(context.Background(), &r); typed(t, err).Code != "secret_expired" {
		t.Fatalf("%v", err)
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
	if _, err := fetchWith(t, g, g.connector(), cfgJSON, string(cred), nil); err != nil {
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
	_, err := fetch(t, g, g.connector(), nil)
	if strings.Contains(err.Error(), "test-secret") || strings.Contains(err.Error(), "token-") {
		t.Fatalf("error leaks a secret: %v", err)
	}
}

// The bearer token only goes to the configured Graph endpoint: a stored
// checkpoint link or an attachment listing's next page on another host is
// never followed, and collection carries on within Graph.
func TestLinksOutsideTheGraphEndpointAreNeverFollowed(t *testing.T) {
	var mu sync.Mutex
	var foreign []string
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		foreign = append(foreign, r.URL.String()+" with "+r.Header.Get("Authorization"))
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{}, "@odata.deltaLink": "http://" + r.Host + "/v1.0/done"})
	}))
	t.Cleanup(elsewhere.Close)
	g := newFakeGraph(t)
	g.addMessage("m1", "2026-09-28T10:00:00Z", fileAttachment("a1", "r.pdf", "application/pdf", "%PDF", 0))
	g.attachmentsNext = elsewhere.URL + "/v1.0/users/monitoring@example.org/messages/m1/attachments?$skiptoken=1"
	stored := json.RawMessage(`{"since":"2026-09-28T09:00:00Z","link":"` + elsewhere.URL + `/v1.0/users/monitoring@example.org/mailFolders/inbox/messages/delta?$deltatoken=0"}`)
	page, err := fetch(t, g, g.connector(), stored)
	mu.Lock()
	defer mu.Unlock()
	if len(foreign) != 0 {
		t.Fatalf("requests sent to another host: %v", foreign)
	}
	if err != nil || len(page.Items) != 1 || len(page.Items[0].Attachments) != 2 {
		t.Fatalf("collection within Graph: page %+v err %v", page, err)
	}
}

// An attachment's bytes come from its ref: the raw attachment or the HTML
// body; an attachment removed at the source is a source error.
func TestAttachmentRefsReadTheirBytes(t *testing.T) {
	g := newFakeGraph(t)
	g.addMessage("m1", "2026-09-28T10:00:00Z", fileAttachment("a1", "r.pdf", "application/pdf", "%PDF bytes", 0))
	c := g.connector()
	page, err := fetch(t, g, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	item := page.Items[0]
	for _, at := range item.Attachments {
		var r quivrplugin.AttachmentRequest
		request(t, &r, g, cfgJSON, secretJSON, nil, map[string]any{"item": map[string]any{"record_key": item.RecordKey}, "attachment": at})
		rc, err := c.OpenAttachment(context.Background(), &r)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		if at.Key == "attachment-01" && string(b) != "%PDF bytes" || at.Key == "original_body" && (int64(len(b)) != *at.SizeBytes || hash(b) != at.SHA256) {
			t.Fatalf("%s read %q", at.Key, b)
		}
	}
	delete(g.files["m1"], "a1")
	var r quivrplugin.AttachmentRequest
	request(t, &r, g, cfgJSON, secretJSON, nil, map[string]any{"item": map[string]any{"record_key": item.RecordKey}, "attachment": item.Attachments[1]})
	if _, err := c.OpenAttachment(context.Background(), &r); typed(t, err).Code != "attachment_gone" {
		t.Fatalf("%v", err)
	}
}

// An attachment larger than announced is recorded in the mail's
// attachments_skipped with its name, as the connector did before streaming.
func TestASkippedAttachmentIsRecordedInTheMailHeaders(t *testing.T) {
	g := newFakeGraph(t)
	g.addMessage("m1", "2026-09-28T10:00:00Z", fileAttachment("a1", "r.pdf", "application/pdf", "%PDF", 0))
	c := g.connector()
	page, err := fetch(t, g, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	item := page.Items[0]
	var r quivrplugin.AttachmentRequest
	request(t, &r, g, cfgJSON, secretJSON, nil, map[string]any{"item": map[string]any{"record_key": item.RecordKey, "extensions": item.Extensions}, "attachment": item.Attachments[1]})
	exts, err := c.SkippedAttachment(context.Background(), &r, quivrplugin.SkipTooLarge)
	if err != nil {
		t.Fatal(err)
	}
	skipped := exts[MailExtension].Data["attachments_skipped"].([]any)
	if len(skipped) != 1 || skipped[0].(map[string]any)["name"] != "r.pdf" || skipped[0].(map[string]any)["reason"] != "too_large" || skipped[0].(map[string]any)["size"] != int64(4) {
		t.Fatalf("skipped %v", skipped)
	}
}
