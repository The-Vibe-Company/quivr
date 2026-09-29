package openapi_test

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/The-Vibe-Company/quivr-v2/internal/reference/openapi"
)

// The fixture exercises what an integrator reads: grouped endpoints with
// parameters, bodies, headers and error descriptions, a receiver webhook, a
// schema inlined from a shared file, nested properties, conditional rules and
// an example. expected.md is written by hand, not produced by the renderer.
func TestRenderFixtureContract(t *testing.T) {
	got, err := openapi.Render(openapi.Source{
		FS:       os.DirFS("testdata"),
		Contract: "contract/http/openapi.yaml",
		Examples: "contract/http/examples.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/expected.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("rendered page differs from testdata/expected.md:\n%s", got)
	}
}

// A contract the page cannot represent faithfully must fail generation rather
// than commit a page with dead links or orphaned examples.
func TestRenderRefusesWhatItCannotRepresent(t *testing.T) {
	const head = "openapi: 3.1.0\ninfo: {title: T, version: '1'}\n"
	cases := map[string]struct {
		fsys fstest.MapFS
		want string
	}{
		"reference to an unknown schema": {want: "unknown schema", fsys: fstest.MapFS{
			"api.yaml": {Data: []byte(head + "components:\n  schemas:\n    A: {$ref: '#/components/schemas/Missing'}\n")},
		}},
		"unresolved reusable response": {want: "unresolved reference", fsys: fstest.MapFS{
			"api.yaml": {Data: []byte(head + "paths:\n  /v1/a:\n    get:\n      responses:\n        '200': {$ref: '#/components/responses/Missing'}\n")},
		}},
		"example of an unknown schema": {want: "names unknown schema", fsys: fstest.MapFS{
			"api.yaml":      {Data: []byte(head + "components:\n  schemas:\n    A: {type: string}\n")},
			"examples.json": {Data: []byte(`[{"name": "x", "schema": "B", "value": "v"}]`)},
		}},
		"two headings with one anchor": {want: "duplicate heading anchor", fsys: fstest.MapFS{
			"api.yaml": {Data: []byte(head + "components:\n  schemas:\n    Schemas: {type: string}\n")},
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			src := openapi.Source{FS: c.fsys, Contract: "api.yaml"}
			if _, ok := c.fsys["examples.json"]; ok {
				src.Examples = "examples.json"
			}
			if _, err := openapi.Render(src); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Render error = %v, want one containing %q", err, c.want)
			}
		})
	}
}
