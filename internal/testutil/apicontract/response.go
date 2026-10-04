// Package apicontract checks test HTTP responses against the published contract.
package apicontract

import (
	"bytes"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/The-Vibe-Company/quivr/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

type response struct {
	Content map[string]struct{ Schema any } `yaml:"content"`
}
type operation struct {
	Responses      map[string]response `yaml:"responses"`
	PluginResponse *struct {
		Header   string   `yaml:"header"`
		Value    string   `yaml:"value"`
		Statuses []string `yaml:"statuses"`
	} `yaml:"x-quivr-plugin-response"`
}
type route struct {
	path    string
	pattern *regexp.Regexp
	methods map[string]operation
}
type checker struct {
	routes   []route
	aliases  map[string]string
	registry *contracts.HTTPRegistry
}

var load = sync.OnceValues(func() (*checker, error) {
	var doc struct {
		Aliases map[string]string               `yaml:"x-quivr-route-aliases"`
		Paths   map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(contracts.OpenAPI(), &doc); err != nil {
		return nil, err
	}
	registry, err := contracts.HTTP()
	if err != nil {
		return nil, err
	}
	c := &checker{registry: registry, aliases: doc.Aliases}
	for path, item := range doc.Paths {
		methods := map[string]operation{}
		for method, node := range item {
			switch method {
			case "get", "put", "post", "delete", "options", "head", "patch", "trace":
			default:
				continue // Path-item parameters and metadata are not operations.
			}
			var op operation
			if err := node.Decode(&op); err != nil {
				return nil, err
			}
			methods[method] = op
		}
		parts := strings.Split(path, "/")
		for i, part := range parts {
			if part == "{path}" && i == len(parts)-1 {
				// The connector API contract defines {path} to include nested segments.
				parts[i] = ".+"
			} else if strings.HasPrefix(part, "{") {
				parts[i] = "[^/]+"
			} else {
				parts[i] = regexp.QuoteMeta(part)
			}
		}
		c.routes = append(c.routes, route{path, regexp.MustCompile("^" + strings.Join(parts, "/") + "$"), methods})
	}
	sort.Slice(c.routes, func(i, j int) bool { return c.routes[i].path < c.routes[j].path })
	return c, nil
})

// Check resolves method/path/status/media before validating the complete body.
func Check(method, path string, status int, headers http.Header, body []byte) error {
	c, err := load()
	if err == nil {
		err = c.check(method, path, status, headers, body)
	}
	if err != nil {
		return fmt.Errorf("%s %s HTTP %d: %w", method, path, status, err)
	}
	return nil
}

func (c *checker) check(method, path string, status int, headers http.Header, body []byte) error {
	if status == http.StatusNoContent && len(body) != 0 {
		return fmt.Errorf("HTTP 204 cannot carry a response body")
	}
	contentType := headers.Get("Content-Type")
	u, err := url.Parse(path)
	if err != nil {
		return err
	}
	if alias, ok := c.aliases[u.Path]; ok {
		u.Path = alias
	}
	var chosen *route
	for i := range c.routes {
		if c.routes[i].path == u.Path {
			chosen = &c.routes[i]
			break
		}
		if c.routes[i].pattern.MatchString(u.Path) {
			if chosen != nil {
				return fmt.Errorf("ambiguous contract route")
			}
			chosen = &c.routes[i]
		}
	}
	if chosen == nil && status == http.StatusNotFound {
		return c.validate(contracts.HTTPSchema("Error"), "application/json", contentType, body)
	}
	if chosen == nil {
		return fmt.Errorf("route absent from contract")
	}
	op, ok := chosen.methods[strings.ToLower(method)]
	if !ok {
		if status == http.StatusMethodNotAllowed {
			return c.validate(contracts.HTTPSchema("Error"), "application/json", contentType, body)
		}
		return fmt.Errorf("method absent from contract for %s", chosen.path)
	}
	key := strconv.Itoa(status)
	r, ok := op.Responses[key]
	if !ok {
		key = fmt.Sprintf("%dXX", status/100)
		r, ok = op.Responses[key]
	}
	if !ok {
		key = "default"
		r, ok = op.Responses[key]
	}
	if !ok {
		return fmt.Errorf("status absent from contract")
	}
	if variant := op.PluginResponse; variant != nil && len(headers.Values(variant.Header)) > 0 {
		if values := headers.Values(variant.Header); len(values) != 1 || values[0] != variant.Value {
			return fmt.Errorf("invalid %s response variant", variant.Header)
		}
		if !slices.Contains(variant.Statuses, strconv.Itoa(status)) && !slices.Contains(variant.Statuses, fmt.Sprintf("%dXX", status/100)) {
			return fmt.Errorf("status absent from plugin response contract")
		}
		// This operation declares the marked reply's status/media/body plugin-owned.
		return nil
	}
	if len(r.Content) == 0 {
		// Webhook response bodies/content types are explicitly plugin-owned.
		return nil
	}
	media, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return fmt.Errorf("invalid response Content-Type %q: %w", contentType, err)
	}
	if _, ok := r.Content[media]; !ok {
		return fmt.Errorf("media %q absent from contract", media)
	}
	escape := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1") }
	ref := contracts.BaseURL + "http/v0/openapi.yaml#/paths/" + escape(chosen.path) + "/" + strings.ToLower(method) + "/responses/" + key + "/content/" + escape(media) + "/schema"
	return c.validate(ref, media, contentType, body)
}

func (c *checker) validate(ref, media, contentType string, body []byte) error {
	actual, _, err := mime.ParseMediaType(contentType)
	if err != nil || actual != media {
		return fmt.Errorf("expected Content-Type %s, got %q", media, contentType)
	}
	schema, err := c.registry.Schema(ref)
	if err != nil {
		return err
	}
	var instance any = string(body)
	if media == "application/json" {
		instance, err = jsonschema.UnmarshalJSON(bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("invalid JSON: %w", err)
		}
	}
	return schema.Validate(instance)
}

// Handler validates every response without changing what its caller receives.
// SSE is declared as a string; validate its headers and type at the first
// flush, forwarding chunks immediately instead of waiting for stream closure.
func Handler(t *testing.T, next http.Handler) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checked := &writer{ResponseWriter: w, t: t, request: r}
		next.ServeHTTP(checked, r)
		checked.check()
	})
}

type writer struct {
	http.ResponseWriter
	t           *testing.T
	request     *http.Request
	status      int
	contentType string
	headers     http.Header
	body        bytes.Buffer
	stream      bool
	validated   bool
}

func (w *writer) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status, w.contentType = status, w.Header().Get("Content-Type")
	w.headers = w.Header().Clone()
	w.stream = strings.HasPrefix(w.contentType, "text/event-stream")
	w.ResponseWriter.WriteHeader(status)
	if w.stream {
		w.check()
	}
}
func (w *writer) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if !w.stream {
		_, _ = w.body.Write(b)
	}
	return w.ResponseWriter.Write(b)
}
func (w *writer) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *writer) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *writer) check() {
	if w.validated {
		return
	}
	w.validated = true
	if w.status == 0 {
		w.status = http.StatusOK
		w.contentType = w.Header().Get("Content-Type")
		w.headers = w.Header().Clone()
	}
	if err := Check(w.request.Method, w.request.URL.String(), w.status, w.headers, w.body.Bytes()); err != nil {
		w.t.Errorf("response violates OpenAPI: %v", err)
	}
}
