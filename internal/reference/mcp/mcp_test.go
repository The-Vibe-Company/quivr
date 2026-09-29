package mcp_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/online"
	"github.com/The-Vibe-Company/quivr-v2/internal/reference/mcp"
)

// The fixture is a catalogue with two profiles sharing a tool, a tool that
// changes data, arguments in a non-alphabetical order with constraints, and a
// tool without arguments. expected.md is written by hand, not produced by the
// renderer.
var fixture = mcp.Source{
	From:  "catalogue.go",
	Intro: "Tools of `tool mcp`.",
	Profiles: []online.MCPProfile{
		{Name: "read", Summary: "read items; no tool changes data", Instructions: "Call find first."},
		{Name: "write", Summary: "add items", Instructions: "Call add, then find."},
	},
	Tools: []online.MCPTool{
		{
			Name: "find", Title: "Find items", Description: "Find items.\nReturns {\"items\": [...]}.",
			InputSchema: json.RawMessage(`{"type": "object", "required": ["query"], "properties": {
				"query": {"type": "string", "minLength": 1, "description": "what to find"},
				"kinds": {"type": "array", "maxItems": 3, "uniqueItems": true, "items": {"type": "string", "enum": ["a", "b"]}},
				"limit": {"type": "integer", "minimum": 1, "maximum": 50, "description": "maximum hits"}
			}}`),
			Profiles: []string{"read", "write"},
			ReadOnly: true,
		},
		{
			Name: "add", Title: "Add an item", Description: "Add an item.",
			InputSchema: json.RawMessage(`{"type": "object"}`),
			Profiles:    []string{"write"},
		},
	},
}

func TestRenderFixtureCatalogue(t *testing.T) {
	got, err := mcp.Render(fixture)
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

// A catalogue the page cannot represent faithfully must fail generation rather
// than commit a page with dead links or a wrong argument table.
func TestRenderRefusesWhatItCannotRepresent(t *testing.T) {
	read := []online.MCPProfile{{Name: "read"}}
	tool := func(schema string, profiles ...string) []online.MCPTool {
		return []online.MCPTool{{Name: "t", InputSchema: json.RawMessage(schema), Profiles: profiles}}
	}
	cases := map[string]struct {
		profiles []online.MCPProfile
		tools    []online.MCPTool
		want     string
	}{
		"tool in an unknown profile":   {read, tool(`{"type": "object"}`, "read", "admin"), `unknown profile "admin"`},
		"tool in no profile":           {read, tool(`{"type": "object"}`), "is in no profile"},
		"profile without tools":        {append(read, online.MCPProfile{Name: "empty"}), tool(`{"type": "object"}`, "read"), `profile "empty" exposes no tool`},
		"schema that is not an object": {read, tool(`{"type": "string"}`, "read"), "want object"},
		"required argument missing":    {read, tool(`{"type": "object", "required": ["q"]}`, "read"), `required argument "q"`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := mcp.Render(mcp.Source{From: "x", Profiles: c.profiles, Tools: c.tools}); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Render error = %v, want one containing %q", err, c.want)
			}
		})
	}
}
