package m365mail

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
)

// credential is the decrypted Deposited Credential: a client secret or a
// certificate with its private key. It lives only for one Fetch.
type credential struct {
	ClientID       string `json:"client_id"`
	ClientSecret   string `json:"client_secret"`
	CertificatePEM string `json:"certificate_pem"`
	PrivateKeyPEM  string `json:"private_key_pem"`
}

func parseCredential(raw json.RawMessage) (credential, error) {
	var c credential
	if len(raw) == 0 || json.Unmarshal(raw, &c) != nil || c.ClientID == "" || (c.ClientSecret == "") == (c.CertificatePEM == "") {
		return c, errors.New("invalid m365_mail credential")
	}
	return c, nil
}

// tokenCache keeps app-only access tokens in process memory only, keyed by a
// digest of the endpoint, tenant and credential: rotating the credential
// changes the key, so the next run uses the new secret.
type tokenCache struct {
	mu      sync.Mutex
	entries map[string]cachedToken
}

type cachedToken struct {
	value   string
	expires time.Time
}

func newTokenCache() *tokenCache { return &tokenCache{entries: map[string]cachedToken{}} }

// tokenMargin renews a token this long before it expires.
const tokenMargin = 5 * time.Minute

func (s session) cacheKey() string {
	h := sha256.New()
	for _, part := range []string{s.c.login, s.cfg.TenantID, s.cred.ClientID, s.cred.ClientSecret, s.cred.CertificatePEM, s.cred.PrivateKeyPEM} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s session) forgetToken() {
	s.c.tokens.mu.Lock()
	delete(s.c.tokens.entries, s.cacheKey())
	s.c.tokens.mu.Unlock()
}

// token returns a cached or newly issued access token.
func (s session) token(ctx context.Context) (string, error) {
	key := s.cacheKey()
	s.c.tokens.mu.Lock()
	cached, ok := s.c.tokens.entries[key]
	s.c.tokens.mu.Unlock()
	if ok && time.Now().Before(cached.expires) {
		return cached.value, nil
	}
	tokenURL := s.c.login + "/" + url.PathEscape(s.cfg.TenantID) + "/oauth2/v2.0/token"
	form := url.Values{"client_id": {s.cred.ClientID}, "scope": {"https://graph.microsoft.com/.default"}, "grant_type": {"client_credentials"}}
	if s.cred.ClientSecret != "" {
		form.Set("client_secret", s.cred.ClientSecret)
	} else {
		assertion, err := clientAssertion(s.cred, tokenURL, time.Now())
		if err != nil {
			return "", connectors.AccessError("invalid_certificate")
		}
		form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
		form.Set("client_assertion", assertion)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", connectors.SourceError("invalid_config")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.c.client.Do(req)
	if err != nil {
		return "", connectors.TransientError("token_unavailable")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return "", &connectors.Error{Class: connectors.ClassTransient, Code: "token_unavailable", RetryAfter: retryAfter(resp.Header.Get("Retry-After"))}
	}
	if resp.StatusCode != http.StatusOK {
		return "", tokenError(body)
	}
	var issued struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if json.Unmarshal(body, &issued) != nil || issued.AccessToken == "" {
		return "", connectors.TransientError("token_unavailable")
	}
	expires := time.Now().Add(time.Duration(issued.ExpiresIn)*time.Second - tokenMargin)
	s.c.tokens.mu.Lock()
	s.c.tokens.entries[key] = cachedToken{value: issued.AccessToken, expires: expires}
	s.c.tokens.mu.Unlock()
	return issued.AccessToken, nil
}

// tokenError maps Microsoft identity platform (AADSTS) codes to stable health
// codes. The response body is never propagated: it may echo request data.
func tokenError(body []byte) error {
	var e struct {
		Error string `json:"error"`
		Codes []int  `json:"error_codes"`
	}
	_ = json.Unmarshal(body, &e)
	for _, code := range e.Codes {
		switch code {
		case 7000222:
			return connectors.AccessError("secret_expired")
		case 7000215, 700027, 700024, 7000274:
			return connectors.AccessError("invalid_client_credential")
		case 700016:
			return connectors.AccessError("app_not_found")
		case 90002, 90023:
			return connectors.AccessError("tenant_not_found")
		case 65001, 7000112, 700025:
			return connectors.AccessError("consent_missing")
		}
	}
	return connectors.AccessError("token_refused")
}

// clientAssertion builds the certificate credential JWT: PS256, x5t#S256
// thumbprint, audience = token endpoint, issuer = subject = client id.
func clientAssertion(c credential, audience string, now time.Time) (string, error) {
	certBlock, _ := pem.Decode([]byte(c.CertificatePEM))
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		return "", errors.New("certificate_pem")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return "", err
	}
	keyBlock, _ := pem.Decode([]byte(c.PrivateKeyPEM))
	if keyBlock == nil {
		return "", errors.New("private_key_pem")
	}
	var key *rsa.PrivateKey
	if parsed, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes); err == nil {
		key, _ = parsed.(*rsa.PrivateKey)
	} else if parsed, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes); err == nil {
		key = parsed
	}
	if key == nil {
		return "", errors.New("private key must be RSA")
	}
	thumb := sha256.Sum256(cert.Raw)
	jti := make([]byte, 16)
	if _, err = rand.Read(jti); err != nil {
		return "", err
	}
	header, _ := json.Marshal(map[string]string{"alg": "PS256", "typ": "JWT", "x5t#S256": base64.RawURLEncoding.EncodeToString(thumb[:])})
	claims, _ := json.Marshal(map[string]any{"aud": audience, "iss": c.ClientID, "sub": c.ClientID, "jti": hex.EncodeToString(jti), "nbf": now.Unix(), "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix()})
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPSS(rand.Reader, key, crypto.SHA256, digest[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
