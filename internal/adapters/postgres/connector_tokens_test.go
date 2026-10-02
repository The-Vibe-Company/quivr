package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost/fakeplugin"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
)

// The real store owns token lifetime, durable revocation and competing rotation.
// The service owns secret generation/comparison and scoped administration. No
// fake implements those contracts; expiry is moved in SQL instead of sleeping.
func TestConnectorTokensStayScopedAndRotateWithoutExtendingOldSecrets(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	scope := corpus.Scope{Organization: fmt.Sprintf("adapter-tokens-%d", time.Now().UnixNano()), Actions: []string{"corpora:write", "connectors:write", "connectors:admin"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "tokens", Name: "Push tokens"})
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.ConnectorStore{ContentStore: postgres.ContentStore{Pool: pool}}
	registry, _ := connectors.NewRegistry(fakeplugin.FixtureConnector{})
	sealer, _ := connectors.NewSealer("adapter-token-test-key-012345678901234")
	service := connectors.Service{Store: store, Tokens: store, Registry: registry, Sealer: sealer}
	create := func(key string) connectors.Instance {
		in, err := service.Create(ctx, scope, connectors.CreateInput{Key: key, CorpusID: c.ID, Namespace: key, Kind: "fixture", Config: json.RawMessage(`{"script":[]}`)})
		if err != nil {
			t.Fatal(err)
		}
		return in
	}
	first, second := create("first"), create("second")
	issued, err := service.CreateToken(ctx, scope, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Secret == "" || issued.Token.ID == "" || issued.Token.Prefix == "" || issued.Token.CreatedAt.IsZero() {
		t.Fatal("missing issuance metadata")
	}
	if err := service.AuthenticateToken(ctx, scope.Organization, first.ID, issued.Secret); err != nil {
		t.Fatal(err)
	}
	// Keep the public token ID and valid encoding while changing secret bytes:
	// removing the digest comparison must not authenticate by ID alone.
	wrongByte := byte('A')
	if issued.Secret[37] == wrongByte {
		wrongByte = 'B'
	}
	wrongSecret := issued.Secret[:37] + string(wrongByte) + issued.Secret[38:]
	for _, tc := range []struct{ org, instance, secret string }{
		{scope.Organization, second.ID, issued.Secret}, {"another-org", first.ID, issued.Secret},
		{scope.Organization, first.ID, issued.Secret[:len(issued.Secret)-1] + "!"},
		{scope.Organization, first.ID, wrongSecret},
	} {
		if err := service.AuthenticateToken(ctx, tc.org, tc.instance, tc.secret); !errors.Is(err, connectors.ErrInvalidInstanceToken) {
			t.Fatalf("foreign/invalid token accepted: %v", err)
		}
	}
	var stored []byte
	if err := pool.QueryRow(ctx, "SELECT hash FROM connector_instance_tokens WHERE organization=$1 AND connector_id=$2 AND id=$3", scope.Organization, first.ID, issued.Token.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 32 || bytes.Contains(stored, []byte(issued.Secret)) {
		t.Fatal("token must be stored only as a fixed-length digest")
	}
	for _, bad := range []corpus.Scope{
		{Organization: scope.Organization, Actions: []string{"connectors:write"}, Corpora: []string{"*"}},
		{Organization: scope.Organization, Actions: []string{"connectors:admin"}, Corpora: []string{"other-corpus"}},
		{Organization: "another-org", Actions: []string{"connectors:admin"}, Corpora: []string{"*"}},
	} {
		if _, err := service.CreateToken(ctx, bad, first.ID); err == nil {
			t.Fatal("unscoped issuance allowed")
		}
		if _, err := service.ListTokens(ctx, bad, first.ID, "", 10); err == nil {
			t.Fatal("unscoped listing allowed")
		}
		if _, err := service.RotateToken(ctx, bad, first.ID, issued.Token.ID); err == nil {
			t.Fatal("unscoped rotation allowed")
		}
		if _, err := service.RevokeToken(ctx, bad, first.ID, issued.Token.ID); err == nil {
			t.Fatal("unscoped revocation allowed")
		}
	}
	if _, err := service.RotateToken(ctx, scope, second.ID, issued.Token.ID); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatalf("foreign token rotation: %v", err)
	}
	if _, err := service.RevokeToken(ctx, scope, second.ID, issued.Token.ID); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatalf("foreign token revocation: %v", err)
	}

	var wg sync.WaitGroup
	results := make(chan connectors.IssuedToken, 2)
	failures := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			replacement, err := service.RotateToken(ctx, scope, first.ID, issued.Token.ID)
			if err != nil {
				failures <- err
			} else {
				results <- replacement
			}
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	if len(results) != 1 || len(failures) != 1 {
		t.Fatalf("rotation winners=%d refusals=%d", len(results), len(failures))
	}
	if err := <-failures; !errors.Is(err, connectors.ErrTokenInactive) {
		t.Fatalf("rotation refusal: %v", err)
	}
	replacement := <-results
	if replacement.Secret == issued.Secret || replacement.Token.ID == issued.Token.ID {
		t.Fatal("rotation did not issue a distinct token")
	}
	for _, secret := range []string{issued.Secret, replacement.Secret} {
		if err := service.AuthenticateToken(ctx, scope.Organization, first.ID, secret); err != nil {
			t.Fatalf("overlap rejected: %v", err)
		}
	}
	tokens, err := service.ListTokens(ctx, scope, first.ID, "", 10)
	if err != nil || len(tokens) != 2 {
		t.Fatalf("token metadata count=%d err=%v", len(tokens), err)
	}
	for _, token := range tokens {
		if token.ID == issued.Token.ID {
			if token.RotatedAt == nil || token.ValidUntil == nil || token.ValidUntil.Sub(*token.RotatedAt) != 5*time.Minute {
				t.Fatal("overlap must be exactly five minutes")
			}
		}
	}
	// Boundary is strict: at expiry the old token is refused immediately.
	if _, err := pool.Exec(ctx, "UPDATE connector_instance_tokens SET valid_until=clock_timestamp() WHERE organization=$1 AND connector_id=$2 AND id=$3", scope.Organization, first.ID, issued.Token.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.AuthenticateToken(ctx, scope.Organization, first.ID, issued.Secret); !errors.Is(err, connectors.ErrInvalidInstanceToken) {
		t.Fatalf("expired token: %v", err)
	}
	if _, err := service.RevokeToken(ctx, scope, first.ID, replacement.Token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RevokeToken(ctx, scope, first.ID, replacement.Token.ID); err != nil {
		t.Fatalf("revoke replay: %v", err)
	}
	if err := service.AuthenticateToken(ctx, scope.Organization, first.ID, replacement.Secret); !errors.Is(err, connectors.ErrInvalidInstanceToken) {
		t.Fatalf("revoked token: %v", err)
	}
	if _, err := service.RotateToken(ctx, scope, first.ID, replacement.Token.ID); !errors.Is(err, connectors.ErrTokenInactive) {
		t.Fatalf("revoked rotation: %v", err)
	}
	final, err := service.CreateToken(ctx, scope, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Disable(ctx, scope, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.AuthenticateToken(ctx, scope.Organization, first.ID, final.Secret); !errors.Is(err, connectors.ErrInvalidInstanceToken) {
		t.Fatalf("disabled authentication: %v", err)
	}
	if _, err := service.CreateToken(ctx, scope, first.ID); !errors.Is(err, connectors.ErrDisabled) {
		t.Fatalf("disabled issuance: %v", err)
	}
	if _, err := service.RevokeToken(ctx, scope, first.ID, final.Token.ID); err != nil {
		t.Fatalf("disabled token must still be revocable: %v", err)
	}
}

type tokenPushSource struct {
	fakeplugin.FixtureConnector
	calls  int
	leaked bool
}

func (*tokenPushSource) Pushes() bool { return true }
func (*tokenPushSource) APIRoutes() []connectors.APIRoute {
	return []connectors.APIRoute{
		{Name: "push", Method: "POST", Path: "events", Auth: "instance_token"},
		{Name: "challenge", Method: "GET", Path: "challenge", Auth: "instance_token"},
		{Name: "key", Method: "POST", Path: "key-events", Auth: "quivr_key"},
	}
}
func (s *tokenPushSource) Receive(_ context.Context, r connectors.ReceiveRequest) (connectors.Delivery, error) {
	s.calls++
	s.leaked = s.leaked || len(r.Request.Headers["authorization"]) > 0
	return connectors.Delivery{Accepted: true, Status: 200, Body: "challenge"}, nil
}

// This owns transport/auth integration against real durable tokens. The only
// stand-in is the source receiver; it does not implement token authorization.
func TestConnectorTokenHTTPShowsSecretsOnceAndAuthenticatesBeforeReceiving(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	scope := corpus.Scope{Organization: fmt.Sprintf("adapter-token-http-%d", time.Now().UnixNano()), Actions: []string{"corpora:write", "connectors:write", "connectors:admin", "connector:push"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "tokens", Name: "Push tokens"})
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.ConnectorStore{ContentStore: postgres.ContentStore{Pool: pool}}
	source := &tokenPushSource{}
	registry, err := connectors.NewRegistry(source)
	if err != nil {
		t.Fatal(err)
	}
	sealer, _ := connectors.NewSealer("adapter-token-http-key-012345678901234")
	service := connectors.Service{Store: store, Tokens: store, Registry: registry, Sealer: sealer}
	create := func(key string) connectors.Instance {
		in, err := service.Create(ctx, scope, connectors.CreateInput{Key: key, CorpusID: c.ID, Namespace: key, Kind: "fixture", Config: json.RawMessage(`{"script":[]}`)})
		if err != nil {
			t.Fatal(err)
		}
		return in
	}
	first, second := create("first"), create("second")
	keys := map[string]corpus.Scope{
		"admin":   scope,
		"writer":  {Organization: scope.Organization, Actions: []string{"connectors:write", "connectors:read", "connector:push"}, Corpora: []string{"*"}},
		"fenced":  {Organization: scope.Organization, Actions: []string{"connectors:admin"}, Corpora: []string{"other-corpus"}},
		"foreign": {Organization: "another-org", Actions: []string{"connectors:admin"}, Corpora: []string{"*"}},
	}
	handler, err := httpapi.New(nil, content.Service{}, retrieval.Service{}, uploads.Service{}, keys, []byte("adapter-token-http-cursor-key-012345678901"), httpapi.WithConnectors(service), httpapi.WithRelay(connectors.Relay{Store: store, Registry: registry, Tokens: service}))
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, key string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		req = req.WithContext(ctx)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s: want %d got %d code/body omitted to keep issuance secrets private", method, path, want, rec.Code)
		}
		if want >= 400 {
			var envelope struct {
				Code      string `json:"code"`
				Message   string `json:"message"`
				Retryable *bool  `json:"retryable"`
			}
			if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") || json.Unmarshal(rec.Body.Bytes(), &envelope) != nil || envelope.Code == "" || envelope.Message == "" || envelope.Retryable == nil {
				t.Fatal("engine failure must use the JSON Error envelope")
			}
		}
		return rec
	}
	base := "/v0/connectors/" + first.ID + "/tokens"
	for _, key := range []string{"writer", "fenced", "foreign"} {
		want := 404
		if key == "writer" {
			want = 403
		}
		call("POST", base, key, want)
		call("GET", base, key, want)
	}
	var issued struct {
		Token struct {
			ID string `json:"token_id"`
		} `json:"token"`
		Secret string `json:"secret"`
	}
	rec := call("POST", base, "admin", 201)
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("secret issuance may be cached")
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if issued.Secret == "" || issued.Token.ID == "" {
		t.Fatal("missing secret/token id")
	}
	list := call("GET", base, "admin", 200)
	if strings.Contains(list.Body.String(), issued.Secret) || strings.Contains(list.Body.String(), `"secret"`) || strings.Contains(list.Body.String(), `"hash"`) {
		t.Fatal("list returned private token material")
	}
	tokenPath := base + "/" + issued.Token.ID
	for _, key := range []string{"writer", "fenced", "foreign"} {
		want := 404
		if key == "writer" {
			want = 403
		}
		call("POST", tokenPath+"/rotate", key, want)
		call("DELETE", tokenPath, key, want)
	}
	// A token ID for another instance is never a management authority.
	foreignPath := "/v0/connectors/" + second.ID + "/tokens/" + issued.Token.ID
	call("POST", foreignPath+"/rotate", "admin", 404)
	call("DELETE", foreignPath, "admin", 404)
	api := "/v0/connectors/" + first.ID + "/api/"
	before := source.calls
	for _, key := range []string{"", "admin", "writer", "unknown"} {
		call("POST", api+"events", key, 401)
	}
	call("POST", "/v0/connectors/"+second.ID+"/api/events", issued.Secret, 401)
	call("POST", api+"key-events", issued.Secret, 401)
	call("GET", base, issued.Secret, 401)
	call("GET", "/v0/corpora", issued.Secret, 401)
	if source.calls != before {
		t.Fatal("refused authentication reached the source")
	}
	push := call("POST", api+"events", issued.Secret, 202)
	if strings.TrimSpace(push.Body.String()) != `{"receipts":[]}` {
		t.Fatal("push did not return receipts")
	}
	challenge := call("GET", api+"challenge", issued.Secret, 200)
	if challenge.Body.String() != "challenge" || source.calls != before+2 || source.leaked {
		t.Fatal("challenge dispatch or bearer stripping failed")
	}
	call("POST", api+"key-events", "writer", 202)
	// Rotation is an issuance; every subsequent metadata response stays secret-free.
	rotated := call("POST", tokenPath+"/rotate", "admin", 201)
	if rotated.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("rotation issuance may be cached")
	}
	var replacement struct {
		Token struct {
			ID string `json:"token_id"`
		} `json:"token"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(rotated.Body.Bytes(), &replacement); err != nil {
		t.Fatal(err)
	}
	if replacement.Secret == "" || replacement.Secret == issued.Secret {
		t.Fatal("rotation did not return a new secret once")
	}
	var page struct {
		Items []struct {
			ID string `json:"token_id"`
		} `json:"items"`
		After string `json:"next_after"`
	}
	if err := json.Unmarshal(call("GET", base+"?limit=1", "admin", 200).Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.After == "" {
		t.Fatal("missing token pagination")
	}
	var next struct {
		Items []struct {
			ID string `json:"token_id"`
		} `json:"items"`
		After string `json:"next_after"`
	}
	if err := json.Unmarshal(call("GET", base+"?limit=1&after="+page.After, "admin", 200).Body.Bytes(), &next); err != nil || len(next.Items) != 1 || next.Items[0].ID == page.Items[0].ID || next.After != "" {
		t.Fatal("token page skipped or repeated an item")
	}
	for _, query := range []string{"?limit=0", "?limit=101", "?limit=", "?after=" + strings.Repeat("x", 129), "?extra=1"} {
		call("GET", base+query, "admin", 422)
	}
	call("POST", tokenPath+"/rotate", "admin", 409)
	revoked := call("DELETE", tokenPath, "admin", 200)
	if strings.Contains(revoked.Body.String(), issued.Secret) || strings.Contains(revoked.Body.String(), `"secret"`) {
		t.Fatal("revocation returned a secret")
	}
	before = source.calls
	call("POST", api+"events", issued.Secret, 401)
	if source.calls != before {
		t.Fatal("revoked token reached source")
	}
	call("POST", api+"events", replacement.Secret, 202)
	call("DELETE", base+"/"+replacement.Token.ID, "admin", 200)
	call("GET", api+"challenge", replacement.Secret, 401)
}
