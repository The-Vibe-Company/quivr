// Package routing serves generated contract routes without implicit HEAD,
// redirects or path normalization. Identifiers use the original decoded path,
// matching the HTTP API's established behavior.
package routing

import (
	"fmt"
	"net/http"
	"strings"
)

// PathMatch gives a transport its matched path when the method is absent.
// Handler is nil for an unknown path. A dynamic plugin route may explicitly
// call Handler with the original method to preserve plugin-owned dispatch.
type PathMatch struct {
	Pattern string
	Handler http.HandlerFunc
}

type route struct {
	method, path string
	segments     []string
	handler      http.HandlerFunc
	specificity  int
}

// Mux implements the generated server's ServeMux registration interface.
// Registrations finish before it serves concurrent requests.
type Mux struct {
	routes   []route
	aliases  map[string]string
	fallback func(http.ResponseWriter, *http.Request, PathMatch)
}

func New(fallback func(http.ResponseWriter, *http.Request, PathMatch)) *Mux {
	return &Mux{fallback: fallback, aliases: map[string]string{}}
}

func (m *Mux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok || method == "" || path == "" || handler == nil {
		panic("contract route requires method, path and handler")
	}
	for _, r := range m.routes {
		if r.method == method && r.path == path {
			panic("duplicate contract route: " + pattern)
		}
	}
	segments := strings.Split(path, "/")
	specificity := 0
	for _, segment := range segments {
		if !strings.HasPrefix(segment, "{") {
			specificity++
		}
	}
	m.routes = append(m.routes, route{method, path, segments, handler, specificity})
}

// HandleAlias records a contract-declared exact alias after route registration.
// A missing canonical target fails startup instead of becoming a dead route.
func (m *Mux) HandleAlias(alias, target string) error {
	for _, route := range m.routes {
		if route.path == target {
			m.aliases[alias] = target
			return nil
		}
	}
	return fmt.Errorf("route alias %s has no contract target %s", alias, target)
}

// Pattern resolves a registered template before authentication. Unmatched paths
// never become log fields; aliases and unsupported methods use the same routes.
func (m *Mux) Pattern(req *http.Request) string {
	path := req.URL.Path
	if target, ok := m.aliases[path]; ok {
		path = target
	}
	parts := strings.Split(path, "/")
	chosen, anyMethod := -1, -1
	for i, r := range m.routes {
		if _, ok := match(r.segments, parts); !ok {
			continue
		}
		if anyMethod < 0 || r.specificity > m.routes[anyMethod].specificity {
			anyMethod = i
		}
		if r.method == req.Method && (chosen < 0 || r.specificity > m.routes[chosen].specificity) {
			chosen = i
		}
	}
	if chosen >= 0 {
		return m.routes[chosen].path
	}
	if anyMethod >= 0 {
		return m.routes[anyMethod].path
	}
	return "unmatched"
}

func (m *Mux) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	path := req.URL.Path
	if target, ok := m.aliases[path]; ok {
		path = target
	}
	parts := strings.Split(path, "/")
	chosen, anyMethod := -1, -1
	var values, anyValues map[string]string
	for i, r := range m.routes {
		params, ok := match(r.segments, parts)
		if !ok {
			continue
		}
		if anyMethod < 0 || r.specificity > m.routes[anyMethod].specificity {
			anyMethod, anyValues = i, params
		}
		if r.method == req.Method && (chosen < 0 || r.specificity > m.routes[chosen].specificity) {
			chosen, values = i, params
		}
	}
	if chosen >= 0 {
		for name, value := range values {
			req.SetPathValue(name, value)
		}
		m.routes[chosen].handler(w, req)
		return
	}
	var matched PathMatch
	if anyMethod >= 0 {
		for name, value := range anyValues {
			req.SetPathValue(name, value)
		}
		matched = PathMatch{m.routes[anyMethod].path, m.routes[anyMethod].handler}
	}
	if m.fallback != nil {
		m.fallback(w, req, matched)
	} else {
		http.NotFound(w, req)
	}
}

func match(pattern, parts []string) (map[string]string, bool) {
	var values map[string]string
	for i, segment := range pattern {
		if i >= len(parts) {
			return nil, false
		}
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			name := strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}")
			if values == nil {
				values = map[string]string{}
			}
			if name == "path" && i == len(pattern)-1 {
				values[name] = strings.Join(parts[i:], "/")
				return values, true
			}
			values[name] = parts[i]
		} else if segment != parts[i] {
			return nil, false
		}
	}
	return values, len(pattern) == len(parts)
}
