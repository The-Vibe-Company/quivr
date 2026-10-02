package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// APIRoute is a named ingress route provided by a push kind. Only quivr_key
// authentication is supported; authorization belongs to the engine.
type APIRoute struct {
	Name          string          `json:"name"`
	Method        string          `json:"method"`
	Path          string          `json:"path"`
	Auth          string          `json:"auth"`
	RequestSchema json.RawMessage `json:"request_schema,omitempty"`
}

// APIReceiver declares the routes served by a Connector Receiver.
type APIReceiver interface{ APIRoutes() []APIRoute }

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
		if (route.Method != "GET" && route.Method != "POST") || route.Auth != "quivr_key" || len(route.Path) > 1024 || !routePath.MatchString(route.Path) {
			return nil, fmt.Errorf("route %q requires GET/POST, a safe relative path and quivr_key auth", route.Name)
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
			doc, err := jsonschema.UnmarshalJSON(bytesReader(route.RequestSchema))
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
	ErrInvalidAPIBody    = errors.New("invalid connector API JSON body")
	ErrInvalidAPIRequest = errors.New("invalid connector API request")
	ErrPushItemRejected  = errors.New("connector push item rejected")
)

// DeliverAPI resolves a declared route and authorizes it before opening any
// credential or calling the plugin. The resolved target and provider remain
// fixed for this delivery, even if the active registry changes concurrently.
func (r Relay) DeliverAPI(ctx context.Context, scope corpus.Scope, id, path string, req Relayed) (RelayAnswer, error) {
	if !scope.Allows(corpus.ActionConnectorPush) {
		return RelayAnswer{}, corpus.ErrForbidden
	}
	target, err := r.Store.LoadDelivery(ctx, id)
	if errors.Is(err, corpus.ErrNotFound) {
		return RelayAnswer{}, corpus.ErrNotFound
	}
	if err != nil {
		return unavailable("storage_unavailable"), nil
	}
	if !target.Enabled || target.Organization != scope.Organization || !scope.Contains(target.CorpusID) {
		return RelayAnswer{}, corpus.ErrNotFound
	}
	entry, ok := r.Registry.current()[target.Kind]
	receiver, pushes := entry.connector.(Receiver)
	if !ok || !pushes || !receiver.Pushes() {
		return RelayAnswer{}, corpus.ErrNotFound
	}
	route, allow := resolveRoute(entry.routes, req.Method, path)
	if route == nil {
		if allow != "" {
			return RelayAnswer{Status: 405, Allow: allow}, nil
		}
		return RelayAnswer{}, corpus.ErrNotFound
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
	req.Path = path
	return r.deliver(ctx, target, entry.connector, receiver, req, route.Name, body)
}
