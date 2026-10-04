package content

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/The-Vibe-Company/quivr/internal/publicerr"
)

// ErrExtensionOwned rejects a client submission that writes an extension
// namespace owned by a plugin: only that plugin's normalizer output may write
// it, so plugin data can never be forged or silently overwritten.
var ErrExtensionOwned = publicerr.ExtensionNamespaceOwned

// ExtensionRegistry is the deployment's extension namespaces: the built-in
// ones (BuiltinExtensions) and those owned by plugins, registered at startup.
// As the ExtensionValidator of client submissions it rejects owned namespaces
// with ErrExtensionOwned and validates the rest against the built-in schemas.
// Plugin output is validated against the owning plugin's declared schemas by
// the plugin package, not here.
type ExtensionRegistry struct {
	owners map[string]string
}

// NewExtensionRegistry returns a registry of the built-in namespaces only.
func NewExtensionRegistry() *ExtensionRegistry {
	return &ExtensionRegistry{owners: map[string]string{}}
}

// Own registers namespaces owned by plugin. Each must equal the plugin id or
// start with it followed by a dot, and none may be a built-in namespace or one
// already registered. Nothing is registered when any namespace is refused.
func (r *ExtensionRegistry) Own(plugin string, namespaces ...string) error {
	if plugin == "" {
		return fmt.Errorf("extension namespaces need an owning plugin id")
	}
	sorted := append([]string(nil), namespaces...)
	sort.Strings(sorted)
	for _, ns := range sorted {
		switch owner, owned := r.owners[ns]; {
		case ns != plugin && !strings.HasPrefix(ns, plugin+"."):
			return fmt.Errorf("extension namespace %q of plugin %q must be %q or start with %q", ns, plugin, plugin, plugin+".")
		case DeclaredExtension(ns):
			return fmt.Errorf("extension namespace %q of plugin %q clashes with a built-in namespace", ns, plugin)
		case owned:
			return fmt.Errorf("extension namespace %q of plugin %q is already owned by plugin %q", ns, plugin, owner)
		}
	}
	for _, ns := range sorted {
		r.owners[ns] = plugin
	}
	return nil
}

// Owner returns the plugin that owns a namespace.
func (r *ExtensionRegistry) Owner(namespace string) (string, bool) {
	owner, ok := r.owners[namespace]
	return owner, ok
}

// Declared reports whether a namespace is built in or owned by a plugin, so
// retrieval mappings may address it.
func (r *ExtensionRegistry) Declared(namespace string) bool {
	_, owned := r.owners[namespace]
	return owned || DeclaredExtension(namespace)
}

type extensionWriterKey struct{}

// WithExtensionWriter marks ctx as a submission made on behalf of a pinned
// plugin whose output was already validated against its manifest's declared
// extension schemas (a connector plugin's items). Only the engine sets it; a
// client submission never carries it.
func WithExtensionWriter(ctx context.Context, plugin string) context.Context {
	return context.WithValue(ctx, extensionWriterKey{}, plugin)
}

// Validate implements ExtensionValidator for client submissions. A namespace
// owned by the plugin of WithExtensionWriter is accepted as already validated;
// every other owned namespace is refused.
func (r *ExtensionRegistry) Validate(ctx context.Context, exts Extensions) error {
	writer, _ := ctx.Value(extensionWriterKey{}).(string)
	namespaces := make([]string, 0, len(exts))
	for ns := range exts {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces)
	rest := Extensions{}
	for _, ns := range namespaces {
		owner, owned := r.owners[ns]
		switch {
		case owned && writer != "" && owner == writer:
		case owned:
			return publicerr.WithDetail(ErrExtensionOwned, "extension namespace %q is owned by plugin %q; only that plugin's output writes it", ns, owner)
		default:
			rest[ns] = exts[ns]
		}
	}
	return BuiltinExtensions{}.Validate(ctx, rest)
}

// Declared reports whether a namespace is built in.
func (BuiltinExtensions) Declared(namespace string) bool { return DeclaredExtension(namespace) }

// ExtensionDeclared reports whether retrieval mappings may address a
// namespace: one the Service's validator declares, or a built-in one when the
// validator does not say.
func (s Service) ExtensionDeclared(namespace string) bool {
	if d, ok := s.Extensions.(interface{ Declared(string) bool }); ok {
		return d.Declared(namespace)
	}
	return DeclaredExtension(namespace)
}
