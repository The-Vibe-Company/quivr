// Package mcp renders the MCP reference from the `quivr mcp` tool catalogue:
// its profiles, the tools each profile exposes, and every tool's description,
// input schema and read-only promise. The server and this page read the same
// catalogue, so a tool or profile added to it appears on the page at the next
// `make generate`.
package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr-v2/internal/online"
	"github.com/The-Vibe-Company/quivr-v2/internal/reference"
)

// Source is the catalogue to render.
type Source struct {
	// From names the catalogue the page is generated from, for the notice.
	From string
	// Intro is Markdown written under the title.
	Intro    string
	Profiles []online.MCPProfile
	Tools    []online.MCPTool
}

// Render writes the MCP reference page.
func Render(src Source) ([]byte, error) {
	if len(src.Profiles) == 0 {
		return nil, errors.New("no MCP profiles")
	}
	p := reference.NewPage("MCP reference", src.From)
	p.Para(src.Intro)

	known := map[string]bool{}
	for _, pr := range src.Profiles {
		known[pr.Name] = true
	}
	byProfile := map[string][]online.MCPTool{}
	for _, t := range src.Tools {
		if len(t.Profiles) == 0 {
			p.Fail(fmt.Errorf("tool %q is in no profile", t.Name))
		}
		for _, name := range t.Profiles {
			if !known[name] {
				p.Fail(fmt.Errorf("tool %q names unknown profile %q", t.Name, name))
			}
			byProfile[name] = append(byProfile[name], t)
		}
	}

	p.Heading(2, "Profiles")
	p.Para("`--profile` picks one profile. A profile only narrows which tools the agent sees; the API key decides, on the server, what every call may reach.")
	var rows [][]string
	for _, pr := range src.Profiles {
		rows = append(rows, []string{reference.Link(reference.Code(pr.Name), profileAnchor(pr.Name)), pr.Summary, toolLinks(byProfile[pr.Name])})
	}
	p.Table([]string{"Profile", "Summary", "Tools"}, rows)

	for _, pr := range src.Profiles {
		p.Heading(2, profileHeading(pr.Name))
		p.Para(sentence(pr.Summary))
		p.CodeBlock("sh", "quivr mcp --profile "+pr.Name)
		tools := byProfile[pr.Name]
		if len(tools) == 0 {
			p.Fail(fmt.Errorf("profile %q exposes no tool", pr.Name))
		}
		rows := make([][]string, len(tools))
		for i, t := range tools {
			rows[i] = []string{reference.Link(reference.Code(t.Name), toolAnchor(t.Name)), t.Title, yesNo(t.ReadOnly)}
		}
		p.Table([]string{"Tool", "Title", "Read-only"}, rows)
		p.Para("Instructions the agent receives when it connects:")
		p.CodeBlock("text", pr.Instructions)
	}

	p.Heading(2, "Tools")
	for _, t := range src.Tools {
		p.Heading(3, toolHeading(t.Name))
		p.Para("**" + t.Title + "**")
		p.Table([]string{"Read-only", "Idempotent", "Profiles"}, [][]string{{readOnly(t.ReadOnly), idempotent(t.Idempotent), profileLinks(t.Profiles)}})
		p.Para("Description the agent receives:")
		p.CodeBlock("text", t.Description)
		args, err := arguments(t.InputSchema)
		if err != nil {
			p.Fail(fmt.Errorf("tool %q input schema: %w", t.Name, err))
			continue
		}
		p.Table([]string{"Argument", "Type", "Required", "Description"}, args)
		if len(args) == 0 {
			p.Para("The tool takes no arguments.")
		}
		var schema bytes.Buffer
		if err := json.Indent(&schema, t.InputSchema, "", "  "); err != nil {
			p.Fail(fmt.Errorf("tool %q input schema: %w", t.Name, err))
			continue
		}
		p.Details("Input schema", func() { p.CodeBlock("json", schema.String()) })
	}
	return p.Bytes()
}

func profileHeading(name string) string { return "Profile " + name }
func profileAnchor(name string) string  { return reference.Anchor(profileHeading(name)) }
func toolHeading(name string) string    { return "Tool " + name }
func toolAnchor(name string) string     { return reference.Anchor(toolHeading(name)) }

func toolLinks(tools []online.MCPTool) string {
	links := make([]string, len(tools))
	for i, t := range tools {
		links[i] = reference.Link(reference.Code(t.Name), toolAnchor(t.Name))
	}
	return strings.Join(links, ", ")
}

func profileLinks(names []string) string {
	links := make([]string, len(names))
	for i, n := range names {
		links[i] = reference.Link(reference.Code(n), profileAnchor(n))
	}
	return strings.Join(links, ", ")
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func idempotent(b bool) string {
	if b {
		return "yes: repeating the same call has no further effect"
	}
	return "no"
}

func readOnly(b bool) string {
	if b {
		return "yes: the tool changes no data"
	}
	return "no: the tool can change data"
}

// schema is the part of a JSON Schema the argument table shows.
type schema struct {
	Type        any      `json:"type"`
	Description string   `json:"description"`
	Enum        []any    `json:"enum"`
	Items       *schema  `json:"items"`
	Required    []string `json:"required"`
	MinLength   *int     `json:"minLength"`
	MaxLength   *int     `json:"maxLength"`
	Minimum     *float64 `json:"minimum"`
	Maximum     *float64 `json:"maximum"`
	MinItems    *int     `json:"minItems"`
	MaxItems    *int     `json:"maxItems"`
	UniqueItems bool     `json:"uniqueItems"`
}

// arguments lists the top-level properties of an object schema in the order
// the catalogue declares them.
func arguments(raw json.RawMessage) ([][]string, error) {
	var root struct {
		schema
		Properties json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	if root.Type != "object" {
		return nil, fmt.Errorf("type is %v, want object", root.Type)
	}
	names, err := objectKeys(root.Properties)
	if err != nil {
		return nil, err
	}
	var props map[string]schema
	if len(names) > 0 {
		if err := json.Unmarshal(root.Properties, &props); err != nil {
			return nil, err
		}
	}
	required := map[string]bool{}
	for _, r := range root.Required {
		if _, ok := props[r]; !ok {
			return nil, fmt.Errorf("required argument %q is not a property", r)
		}
		required[r] = true
	}
	rows := make([][]string, len(names))
	for i, n := range names {
		s := props[n]
		desc := s.Description
		if c := constraints(s); c != "" {
			desc = strings.TrimSpace(desc + "\n\n" + c)
		}
		rows[i] = []string{reference.Code(n), typeOf(s), yesNo(required[n]), desc}
	}
	return rows, nil
}

// objectKeys returns the keys of a JSON object in document order.
func objectKeys(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("properties is not an object")
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

func typeOf(s schema) string {
	t := fmt.Sprint(s.Type)
	if s.Type == nil {
		t = "any"
	}
	if t == "array" && s.Items != nil {
		return "array of " + typeOf(*s.Items)
	}
	return t
}

// constraints states the schema's limits in words.
func constraints(s schema) string {
	var parts []string
	if len(s.Enum) > 0 {
		vals := make([]string, len(s.Enum))
		for i, v := range s.Enum {
			vals[i] = reference.Code(fmt.Sprint(v))
		}
		parts = append(parts, "One of "+strings.Join(vals, ", ")+".")
	}
	for _, c := range []struct {
		format string
		n      *float64
	}{
		{"Minimum %g.", s.Minimum}, {"Maximum %g.", s.Maximum},
		{"At least %g characters.", num(s.MinLength)}, {"At most %g characters.", num(s.MaxLength)},
		{"At least %g items.", num(s.MinItems)}, {"At most %g items.", num(s.MaxItems)},
	} {
		if c.n != nil {
			text := fmt.Sprintf(c.format, *c.n)
			if *c.n == 1 {
				text = strings.NewReplacer(" items.", " item.", " characters.", " character.").Replace(text)
			}
			parts = append(parts, text)
		}
	}
	if s.UniqueItems {
		parts = append(parts, "Items are unique.")
	}
	if s.Items != nil {
		if c := constraints(*s.Items); c != "" {
			parts = append(parts, "Each item: "+lowerFirst(c))
		}
	}
	return strings.Join(parts, " ")
}

func num(n *int) *float64 {
	if n == nil {
		return nil
	}
	f := float64(*n)
	return &f
}

// sentence capitalizes s and ends it with a full stop.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

func lowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToLower(r)) + s[n:]
}
