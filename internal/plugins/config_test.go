package plugins_test

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

func TestValidateConfigurationAgainstTheManifestSchema(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixtures, "manifests/valid/full.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := plugins.Validate(raw).Manifest
	if issues := plugins.ValidateConfiguration(m, []byte(`{"max_pages": 3, "keep_blob_part": true}`)); len(issues) != 0 {
		t.Fatalf("valid configuration rejected: %+v", issues)
	}
	issues := plugins.ValidateConfiguration(m, []byte(`{"max_pages": 0, "unknown": 1}`))
	if !reflect.DeepEqual(codes(issues), []string{plugins.CodeInvalidConfiguration}) || len(issues) != 2 {
		t.Fatalf("issues %+v", issues)
	}
	paths := []string{issues[0].Path, issues[1].Path}
	sort.Strings(paths)
	if !reflect.DeepEqual(paths, []string{"/configuration", "/configuration/max_pages"}) {
		t.Fatalf("paths %v: %+v", paths, issues)
	}
	if issues := plugins.ValidateConfiguration(m, []byte(`[]`)); len(issues) != 1 {
		t.Fatalf("non-object accepted: %+v", issues)
	}
	minimal, err := os.ReadFile(filepath.Join(fixtures, "manifests/valid/minimal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if issues := plugins.ValidateConfiguration(plugins.Validate(minimal).Manifest, []byte(`{"anything": true}`)); len(issues) != 0 {
		t.Fatalf("manifest without a configuration schema must accept any object: %+v", issues)
	}
}
