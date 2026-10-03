package connectors

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// APIRoute is a named ingress route provided by a push kind.
// The engine enforces the declared authentication and replay policy.
type APIRoute struct {
	Name          string          `json:"name"`
	Method        string          `json:"method"`
	Path          string          `json:"path"`
	Auth          string          `json:"auth"`
	RequestSchema json.RawMessage `json:"request_schema,omitempty"`
	Signature     *Signature      `json:"signature,omitempty"`
}

var routeName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var routePath = regexp.MustCompile(`^(?:[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*|\{[a-z][a-z0-9_]*\})(?:/(?:[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*|\{[a-z][a-z0-9_]*\}))*$`)

// ValidateAPIRoutes checks names, safe relative paths, ambiguity and local
// request schemas. Both plugin admission and the registry use these rules.
func ValidateAPIRoutes(routes []APIRoute) error {
	_, err := compileRoutes(routes)
	return err
}

type apiRoute struct {
	APIRoute
	schema *jsonschema.Schema
}

type localSchemas struct{}

func (localSchemas) Load(uri string) (any, error) {
	return nil, fmt.Errorf("external schema references are forbidden: %s", uri)
}

func compileRoutes(routes []APIRoute) ([]apiRoute, error) {
	if len(routes) == 0 || len(routes) > 32 {
		return nil, errors.New("declare 1 to 32 API routes")
	}
	out := make([]apiRoute, 0, len(routes))
	names := map[string]bool{}
	for i, route := range routes {
		if !routeName.MatchString(route.Name) || names[route.Name] {
			return nil, fmt.Errorf("invalid or repeated route name %q", route.Name)
		}
		names[route.Name] = true
		if (route.Method != "GET" && route.Method != "POST") || (route.Auth != "quivr_key" && route.Auth != "instance_token" && route.Auth != "signature") || len(route.Path) > 1024 || !routePath.MatchString(route.Path) {
			return nil, fmt.Errorf("route %q requires GET/POST, a safe relative path and supported auth", route.Name)
		}
		if err := validateSignature(route); err != nil {
			return nil, err
		}
		params := map[string]bool{}
		for _, segment := range strings.Split(route.Path, "/") {
			if strings.HasPrefix(segment, "{") {
				if params[segment] {
					return nil, fmt.Errorf("route %q repeats a path parameter", route.Name)
				}
				params[segment] = true
			}
		}
		for _, other := range routes[:i] {
			if pathsOverlap(route.Path, other.Path) && specificity(route.Path) == specificity(other.Path) && (route.Path != other.Path || route.Method == other.Method) {
				return nil, fmt.Errorf("routes %q and %q have ambiguous paths", route.Name, other.Name)
			}
		}
		entry := apiRoute{APIRoute: route}
		if route.RequestSchema != nil {
			doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(route.RequestSchema))
			if err != nil {
				return nil, err
			}
			compiler := jsonschema.NewCompiler()
			compiler.DefaultDraft(jsonschema.Draft2020)
			compiler.UseLoader(localSchemas{})
			const uri = "https://quivr.invalid/connector-api/schema.json"
			if err := compiler.AddResource(uri, doc); err != nil {
				return nil, err
			}
			entry.schema, err = compiler.Compile(uri)
			if err != nil {
				return nil, fmt.Errorf("route %q request_schema: %w", route.Name, err)
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

func specificity(path string) int {
	n := 0
	for _, s := range strings.Split(path, "/") {
		if !strings.HasPrefix(s, "{") {
			n++
		}
	}
	return n
}
func pathsOverlap(a, b string) bool {
	aa, bb := strings.Split(a, "/"), strings.Split(b, "/")
	if len(aa) != len(bb) {
		return false
	}
	for i := range aa {
		if aa[i] != bb[i] && !strings.HasPrefix(aa[i], "{") && !strings.HasPrefix(bb[i], "{") {
			return false
		}
	}
	return true
}
func matchPath(pattern, path string) bool {
	a, b := strings.Split(pattern, "/"), strings.Split(path, "/")
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if b[i] == "" || b[i] == "." || b[i] == ".." {
			return false
		}
		if !strings.HasPrefix(a[i], "{") && a[i] != b[i] {
			return false
		}
	}
	return true
}

// resolveRoute prefers literal segments over templates, even on a method
// mismatch: a template must not shadow a more specific path's Allow list.
func resolveRoute(routes []apiRoute, method, path string) (*apiRoute, string) {
	best := -1
	var chosen *apiRoute
	var allow []string
	for i := range routes {
		route := &routes[i]
		if !matchPath(route.Path, path) {
			continue
		}
		score := specificity(route.Path)
		if score < best {
			continue
		}
		if score > best {
			best = score
			chosen = nil
			allow = nil
		}
		allow = append(allow, route.Method)
		if route.Method == method {
			chosen = route
		}
	}
	sort.Strings(allow)
	return chosen, strings.Join(allow, ", ")
}

var (
	ErrInvalidIdempotencyKey = publicerr.InvalidIdempotencyKey
	ErrInvalidAPIBody        = publicerr.InvalidJson
	ErrInvalidAPIRequest     = publicerr.WithField(publicerr.InvalidSchema, "/body")
	ErrPushItemRejected      = publicerr.ItemRejected
)

// DeliverAPI resolves a declared route and authorizes it before opening any
// credential or calling the plugin. The resolved target and provider remain
// fixed for this delivery, even if the active registry changes concurrently.
func (r Relay) DeliverAPI(ctx context.Context, scope corpus.Scope, id, path string, req Relayed) (RelayAnswer, error) {
	auth := APIAuth{}
	if scope.Organization != "" {
		auth.Scope = &scope
	}
	return r.DeliverAPIWithAuth(ctx, auth, id, path, req)
}

// APIAuth carries at most one bearer credential. Signature routes may be
// called without one. A recognized key is never tried as an instance token.
type APIAuth struct {
	Scope         *corpus.Scope
	InstanceToken string
}

type TokenAuthenticator interface {
	AuthenticateToken(context.Context, string, string, string) error
}

// DeliverAPIWithAuth keeps the resolved target and route fixed through auth,
// validation and delivery. No plugin call precedes engine authentication.
func (r Relay) DeliverAPIWithAuth(ctx context.Context, auth APIAuth, id, path string, req Relayed, prepare ...func() (Relayed, error)) (RelayAnswer, error) {
	if auth.Scope != nil && auth.InstanceToken != "" {
		return RelayAnswer{}, ErrInvalidInstanceToken
	}
	if auth.Scope != nil {
		if err := auth.Scope.Require(corpus.ActionConnectorDeliver); err != nil {
			return RelayAnswer{}, err
		}
	}
	for _, load := range prepare {
		var err error
		req, err = load()
		if err != nil {
			return RelayAnswer{}, err
		}
	}
	target, err := r.Store.LoadDelivery(ctx, id)
	if errors.Is(err, corpus.ErrNotFound) {
		return RelayAnswer{}, corpus.ErrNotFound
	}
	if err != nil {
		return unavailable("storage_unavailable"), nil
	}
	if !target.Enabled || (auth.Scope != nil && (target.Organization != auth.Scope.Organization || !auth.Scope.Contains(target.CorpusID))) {
		return RelayAnswer{}, corpus.ErrNotFound
	}
	entry, ok := r.Registry.current()[target.Kind]
	receiver := entry.descriptor.Receiver
	if !ok || receiver == nil {
		return RelayAnswer{}, corpus.ErrNotFound
	}
	route, allow := resolveRoute(entry.routes, req.Method, path)
	if route == nil {
		if allow != "" {
			return RelayAnswer{Status: 405, Allow: allow}, nil
		}
		return RelayAnswer{}, corpus.ErrNotFound
	}
	switch route.Auth {
	case "quivr_key":
		if auth.Scope == nil {
			if auth.InstanceToken != "" {
				return RelayAnswer{}, ErrInvalidInstanceToken
			}
			return RelayAnswer{}, ErrAPIKeyRequired
		}
	case "instance_token":
		if auth.Scope != nil || auth.InstanceToken == "" {
			return RelayAnswer{}, ErrInvalidInstanceToken
		}
		if r.Tokens == nil {
			return RelayAnswer{}, ErrTokensUnavailable
		}
		if err := r.Tokens.AuthenticateToken(ctx, target.Organization, target.ID, auth.InstanceToken); err != nil {
			return RelayAnswer{}, err
		}
	case "signature":
		// Provider signature verification and replay checks own authentication.
	default:
		return RelayAnswer{}, corpus.ErrForbidden
	}
	return r.deliverRoute(ctx, target, entry, receiver, route, path, req)
}

// deliverProtectedRoute follows mode-specific authentication and replay guards.
func (r Relay) deliverProtectedRoute(ctx context.Context, target Target, connector Connector, receiver Receiver, route *apiRoute, path string, req Relayed) (RelayAnswer, error) {
	// Validate replay metadata only after definitive route authentication.
	rawKey := ""
	if len(req.IdempotencyKeys) > 1 {
		return RelayAnswer{}, ErrInvalidIdempotencyKey
	}
	if len(req.IdempotencyKeys) == 1 {
		rawKey = req.IdempotencyKeys[0]
		if len(rawKey) > 256 || strings.TrimSpace(rawKey) == "" || strings.ContainsAny(rawKey, "\r\n") {
			return RelayAnswer{}, ErrInvalidIdempotencyKey
		}
	}
	body := json.RawMessage(req.Body)
	if len(body) == 0 && req.Method == "GET" {
		body = json.RawMessage(`null`)
	}
	if !json.Valid(body) {
		return RelayAnswer{}, ErrInvalidAPIBody
	}
	if route.schema != nil {
		if err := validateJSON(route.schema, body); err != nil {
			return RelayAnswer{}, ErrInvalidAPIRequest
		}
	}
	delete(req.Headers, "authorization")
	req.Path = path
	invoke := func() (RelayAnswer, error) {
		return r.deliver(ctx, target, connector, receiver, req, route.Name, body)
	}
	if r.Protection == nil {
		return invoke()
	}
	cfg, err := r.PushConfig.Resolve()
	if err != nil {
		return unavailable("connectors_unavailable"), nil
	}
	policy := PushPolicy{RatePerSecond: cfg.RatePerSecond, Burst: cfg.Burst}
	if target.PushPolicy != nil {
		if err := target.PushPolicy.Validate(); err != nil {
			return unavailable("connectors_unavailable"), nil
		}
		policy.AllowedCIDRs = target.PushPolicy.AllowedCIDRs
		if target.PushPolicy.RatePerSecond != 0 {
			policy.RatePerSecond = target.PushPolicy.RatePerSecond
		}
		if target.PushPolicy.Burst != 0 {
			policy.Burst = target.PushPolicy.Burst
		}
	}
	if !policy.allowsIP(req.ClientIP) {
		return RelayAnswer{Status: 403, ErrorCode: "ip_not_allowed"}, nil
	}
	ttl, _ := time.ParseDuration(cfg.IdempotencyTTL)
	key := ""
	// Signature authentication belongs to the provider. Replaying a cached
	// answer could skip verification after the signature window or key rotation.
	// Its dedicated replay guard owns duplicates; every admitted call reaches
	// the provider, including synchronous challenges.
	if rawKey != "" && route.Auth != "signature" {
		digest := sha256.Sum256([]byte(rawKey))
		key = hex.EncodeToString(digest[:])
	}
	return r.Protection.ProtectPush(ctx, PushAttempt{Organization: target.Organization, InstanceID: target.ID, KeyHash: key, RatePerSecond: policy.RatePerSecond, Burst: policy.Burst, TTL: ttl}, invoke)
}
