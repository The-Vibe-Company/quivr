package content_test

import (
	"context"
	"errors"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
)

func ownedRegistry(t *testing.T) *content.ExtensionRegistry {
	t.Helper()
	r := content.NewExtensionRegistry()
	if err := r.Own("acme-md", "acme-md", "acme-md.outline"); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRegistryDeclaresBuiltinAndOwnedNamespaces(t *testing.T) {
	r := ownedRegistry(t)
	for ns, want := range map[string]bool{"example.editorial": true, "connector.rss": true, "acme-md": true, "acme-md.outline": true, "acme-md.other": false, "undeclared": false} {
		if got := r.Declared(ns); got != want {
			t.Errorf("Declared(%q) = %t, want %t", ns, got, want)
		}
	}
	if owner, ok := r.Owner("acme-md.outline"); !ok || owner != "acme-md" {
		t.Fatalf("owner %q %t", owner, ok)
	}
	if _, ok := r.Owner("example.editorial"); ok {
		t.Fatal("a built-in namespace has a plugin owner")
	}
}

func TestRegistryRefusesForeignAndClashingNamespaces(t *testing.T) {
	for name, tc := range map[string]struct {
		plugin    string
		namespace string
	}{
		"not prefixed by the plugin id": {"acme-md", "other.outline"},
		"prefix without a dot":          {"acme-md", "acme-mdx"},
		"clash with a built-in":         {"example", "example.editorial"},
		"owned by another plugin":       {"acme-md.outline", "acme-md.outline"},
		"registered twice":              {"acme-md", "acme-md.outline"},
		"empty plugin id":               {"", "acme-md.x"},
	} {
		t.Run(name, func(t *testing.T) {
			r := ownedRegistry(t)
			if err := r.Own(tc.plugin, tc.namespace); err == nil {
				t.Fatalf("registered %q for %s", tc.namespace, tc.plugin)
			}
			if owner, _ := r.Owner("acme-md.outline"); owner != "acme-md" {
				t.Fatalf("a refused registration changed the owner to %q", owner)
			}
		})
	}
}

// Clients may not write a plugin-owned namespace, top-level or on a Part; the
// rejection carries its own explicit public code. Built-in namespaces keep
// their validation.
func TestClientWritesToOwnedNamespacesAreRejected(t *testing.T) {
	r := ownedRegistry(t)
	ctx := context.Background()
	owned := content.Extensions{"acme-md.outline": {SchemaVersion: "1", Data: map[string]any{"heading_count": 2}}}
	err := r.Validate(ctx, owned)
	if !errors.Is(err, content.ErrExtensionOwned) {
		t.Fatalf("owned namespace write: %v", err)
	}
	if code, _ := publicerr.Code(err); code != "extension_namespace_owned" {
		t.Fatalf("code %q", code)
	}
	if err := r.Validate(ctx, content.Extensions{"example.editorial": {SchemaVersion: "1", Data: map[string]any{"headline": "x"}}}); err != nil {
		t.Fatalf("built-in namespace rejected: %v", err)
	}
	if err := r.Validate(ctx, content.Extensions{"example.editorial": {SchemaVersion: "1", Data: map[string]any{"headline": 42}}}); !errors.Is(err, content.ErrInvalid) {
		t.Fatalf("invalid built-in data: %v", err)
	}

	_, service := manifestService()
	service.Extensions = r
	command := manifestCommand()
	command.Extensions = owned
	if _, err := service.Accept(ctx, scope(), command); !errors.Is(err, content.ErrExtensionOwned) {
		t.Fatalf("top-level owned write accepted: %v", err)
	}
	command = manifestCommand()
	command.Manifest.Parts[1].Extensions = owned
	if _, err := service.Accept(ctx, scope(), command); !errors.Is(err, content.ErrExtensionOwned) {
		t.Fatalf("Part owned write accepted: %v", err)
	}
	if _, err := service.Accept(ctx, scope(), manifestCommand()); err != nil {
		t.Fatalf("built-in write rejected: %v", err)
	}
	if !service.ExtensionDeclared("acme-md.outline") || service.ExtensionDeclared("acme-md.other") {
		t.Fatal("the Service does not report the registry's namespaces")
	}
	if !(content.Service{}).ExtensionDeclared("example.editorial") {
		t.Fatal("a Service without a registry lost the built-in namespaces")
	}
}
