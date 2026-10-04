// Package openapi renders the API reference page from an OpenAPI 3.1 contract.
//
// The page is derived only from the contract, the files its component schemas
// reference, and the contract's example file: richer reference content comes
// from better descriptions and examples in the contract, never from editing the
// output. Every map is walked in source order, so the output is deterministic.
package openapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/The-Vibe-Company/quivr/internal/reference"
)

// Source names the contract and its example file inside FS.
type Source struct {
	FS       fs.FS
	Contract string // for example contracts/http/v0/openapi.yaml
	Examples string // JSON list of {name, schema, value}; empty for none
}

const schemaRef = "#/components/schemas/"

type example struct {
	Name   string          `json:"name"`
	Schema string          `json:"schema"`
	Value  json.RawMessage `json:"value"`
}

type renderer struct {
	src      Source
	root     *yaml.Node
	schemas  *yaml.Node
	shared   map[string]string // component name -> file that defines it
	examples map[string][]example
	page     *reference.Page
}

// Render returns the Markdown API reference for src.
func Render(src Source) ([]byte, error) {
	raw, err := fs.ReadFile(src.FS, src.Contract)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", src.Contract, err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: not an OpenAPI document", src.Contract)
	}
	r := &renderer{src: src, root: doc.Content[0], shared: map[string]string{}, examples: map[string][]example{}}
	r.schemas = get(get(r.root, "components"), "schemas")
	if err := r.inlineShared(); err != nil {
		return nil, err
	}
	if err := r.loadExamples(); err != nil {
		return nil, err
	}
	r.page = reference.NewPage("HTTP API reference", src.Contract)
	r.overview()
	r.endpoints()
	r.webhooks()
	r.components()
	return r.page.Bytes()
}

// inlineShared replaces each component that is only a $ref to another file with
// the referenced definition, rewriting that file's local references to
// component references, as bundle.py does for the other generators.
func (r *renderer) inlineShared() error {
	var err error
	each(r.schemas, func(name string, s *yaml.Node) {
		ref := str(get(s, "$ref"))
		if err != nil || ref == "" || strings.HasPrefix(ref, "#") {
			return
		}
		file, pointer, _ := strings.Cut(ref, "#")
		file = path.Join(path.Dir(r.src.Contract), file)
		var raw []byte
		if raw, err = fs.ReadFile(r.src.FS, file); err != nil {
			return
		}
		var doc yaml.Node
		if err = yaml.Unmarshal(raw, &doc); err != nil {
			err = fmt.Errorf("%s: %w", file, err)
			return
		}
		var target *yaml.Node
		if len(doc.Content) == 1 {
			target = resolve(doc.Content[0], pointer)
		}
		if target == nil {
			err = fmt.Errorf("component %s: %s not found", name, ref)
			return
		}
		*s = *clean(target, true)
		rewriteRefs(s, "#/$defs/", schemaRef)
		r.shared[name] = file
	})
	return err
}

func (r *renderer) loadExamples() error {
	if r.src.Examples == "" {
		return nil
	}
	raw, err := fs.ReadFile(r.src.FS, r.src.Examples)
	if err != nil {
		return err
	}
	var list []example
	if err := json.Unmarshal(raw, &list); err != nil {
		return fmt.Errorf("%s: %w", r.src.Examples, err)
	}
	for _, ex := range list {
		if get(r.schemas, ex.Schema) == nil {
			return fmt.Errorf("%s: example %s names unknown schema %s", r.src.Examples, ex.Name, ex.Schema)
		}
		r.examples[ex.Schema] = append(r.examples[ex.Schema], ex)
	}
	return nil
}

func (r *renderer) overview() {
	info := get(r.root, "info")
	p := r.page
	p.Para(fmt.Sprintf("%s, version %s.", str(get(info, "title")), reference.Code(str(get(info, "version")))))
	p.Para(str(get(info, "description")))
	p.Heading(2, "Authentication")
	var rows [][]string
	each(get(get(r.root, "components"), "securitySchemes"), func(name string, s *yaml.Node) {
		kind := str(get(s, "type"))
		switch kind {
		case "http":
			kind = "HTTP " + reference.Code(str(get(s, "scheme")))
		case "apiKey":
			kind = "API key in " + str(get(s, "in")) + " " + reference.Code(str(get(s, "name")))
		}
		rows = append(rows, []string{reference.Code(name), kind, str(get(s, "description"))})
	})
	var required []string
	for _, req := range items(get(r.root, "security")) {
		each(req, func(name string, _ *yaml.Node) { required = append(required, reference.Code(name)) })
	}
	if len(required) > 0 {
		p.Para("Every endpoint requires " + strings.Join(required, " or ") + " unless it says otherwise.")
	}
	p.Table([]string{"Scheme", "Type", "Description"}, rows)
}

type operation struct {
	method, path string
	pathItem, op *yaml.Node
}

var methods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

func operations(paths *yaml.Node) []operation {
	var ops []operation
	each(paths, func(p string, item *yaml.Node) { ops = append(ops, pathOperations(p, item)...) })
	return ops
}

func pathOperations(p string, item *yaml.Node) []operation {
	var ops []operation
	each(item, func(method string, op *yaml.Node) {
		for _, m := range methods {
			if m == method {
				ops = append(ops, operation{strings.ToUpper(method), p, item, op})
			}
		}
	})
	return ops
}

// group is the resource an endpoint belongs to: the first path segment after
// the version, for example "Saved queries" for /v0/saved-queries/{id}.
func group(p string) string {
	segments := strings.Split(strings.Trim(p, "/"), "/")
	if len(segments[0]) > 1 && segments[0][0] == 'v' && strings.Trim(segments[0][1:], "0123456789") == "" {
		segments = append(segments[1:], "")
	}
	name := strings.ReplaceAll(segments[0], "-", " ")
	if name == "" {
		return "Root"
	}
	first, size := utf8.DecodeRuneInString(name)
	return string(unicode.ToUpper(first)) + name[size:]
}

func (r *renderer) endpoints() {
	ops := operations(get(r.root, "paths"))
	if len(ops) == 0 {
		return
	}
	p := r.page
	p.Heading(2, "Endpoints")
	var order []string
	groups := map[string][]operation{}
	var rows [][]string
	for _, o := range ops {
		g := group(o.path)
		if groups[g] == nil {
			order = append(order, g)
		}
		groups[g] = append(groups[g], o)
	}
	for _, g := range order {
		for _, o := range groups[g] {
			title := reference.Code(o.method + " " + o.path)
			rows = append(rows, []string{reference.Link(title, reference.Anchor(title)), reference.Code(str(get(o.op, "operationId"))), permissions(o.op)})
		}
	}
	p.Table([]string{"Endpoint", "Operation", "Permissions"}, rows)
	for _, g := range order {
		p.Heading(3, g)
		for _, o := range groups[g] {
			p.Heading(4, reference.Code(o.method+" "+o.path))
			r.operation(o)
		}
	}
}

func (r *renderer) webhooks() {
	hooks := get(r.root, "webhooks")
	if len(items(hooks)) == 0 {
		return
	}
	r.page.Heading(2, "Webhooks")
	r.page.Para("Requests the server sends to a receiver you run; they are not routes of this API.")
	each(hooks, func(name string, item *yaml.Node) {
		for _, o := range pathOperations("", item) {
			r.page.Heading(3, reference.Code(name)+" ("+o.method+")")
			r.operation(o)
		}
	})
}

func permissions(op *yaml.Node) string {
	var out []string
	for _, n := range items(get(op, "x-required-permissions")) {
		out = append(out, reference.Code(n.Value))
	}
	return strings.Join(out, ", ")
}

func (r *renderer) operation(o operation) {
	p := r.page
	facts := []string{"Operation " + reference.Code(str(get(o.op, "operationId"))) + "."}
	optionalAuth := false
	for _, req := range items(get(o.op, "security")) {
		if len(req.Content) == 0 {
			optionalAuth = true
		}
	}
	if perms := permissions(o.op); perms != "" {
		if optionalAuth {
			facts = append(facts, "API-key calls require "+perms+".")
		} else {
			facts = append(facts, "Requires "+perms+".")
		}
	}
	if sec := get(o.op, "security"); sec != nil {
		var schemes []string
		for _, req := range items(sec) {
			if len(req.Content) == 0 {
				schemes = append(schemes, "no API key (see operation description)")
			}
			each(req, func(name string, _ *yaml.Node) { schemes = append(schemes, reference.Code(name)) })
		}
		if len(schemes) == 0 {
			facts = append(facts, "No authentication.")
		} else {
			facts = append(facts, "Authentication: "+strings.Join(schemes, " or ")+".")
		}
	}
	if str(get(o.op, "deprecated")) == "true" {
		facts = append(facts, "**Deprecated.**")
	}
	p.Para(strings.Join(facts, " "))
	p.Para(str(get(o.op, "summary")))
	p.Para(str(get(o.op, "description")))

	var params [][]string
	for _, list := range []*yaml.Node{get(o.pathItem, "parameters"), get(o.op, "parameters")} {
		for _, prm := range items(list) {
			prm = r.deref(prm)
			params = append(params, []string{
				reference.Code(str(get(prm, "name"))), str(get(prm, "in")), r.typeOf(get(prm, "schema")),
				yes(str(get(prm, "required")) == "true"), r.details(prm, get(prm, "schema")),
			})
		}
	}
	if len(params) > 0 {
		p.Para("**Parameters**")
		p.Table([]string{"Name", "In", "Type", "Required", "Description"}, params)
	}

	var bodies []bodyDetail
	if body := r.deref(get(o.op, "requestBody")); body != nil {
		label := "**Request body**"
		if str(get(body, "required")) == "true" {
			label += " (required)"
		}
		p.Para(label + ": " + r.content(get(body, "content")) + " " + str(get(body, "description")))
		bodies = append(bodies, bodyDetail{"Request body", get(body, "content")})
	}

	var responses [][]string
	each(get(o.op, "responses"), func(status string, resp *yaml.Node) {
		resp = r.deref(resp)
		body := r.content(get(resp, "content"))
		each(get(resp, "headers"), func(name string, h *yaml.Node) {
			h = r.deref(h)
			line := "Header " + reference.Code(name) + ": " + r.typeOf(get(h, "schema")) + ". " + str(get(h, "description"))
			body = strings.TrimSpace(body + "\n\n" + line)
		})
		responses = append(responses, []string{reference.Code(status), body, str(get(resp, "description"))})
		bodies = append(bodies, bodyDetail{reference.Code(status) + " response", get(resp, "content")})
	})
	if len(responses) > 0 {
		p.Para("**Responses**")
		p.Table([]string{"Status", "Body", "Description"}, responses)
	}
	for _, b := range bodies {
		r.bodyDetails(b)
	}
}

type bodyDetail struct {
	label   string
	content *yaml.Node
}

// bodyDetails lists the fields of an inline body schema, which has no schema
// section of its own, and the body examples of each media type.
func (r *renderer) bodyDetails(b bodyDetail) {
	p := r.page
	each(b.content, func(media string, m *yaml.Node) {
		what := b.label + " " + reference.Code(media)
		if s := get(m, "schema"); s != nil && get(s, "$ref") == nil {
			var rows [][]string
			if get(s, "items") != nil && get(get(s, "items"), "$ref") == nil {
				r.rows(get(s, "items"), "[].", &rows)
			} else {
				r.rows(s, "", &rows)
			}
			if len(rows) > 0 {
				p.Para(what + " fields:")
				p.Table([]string{"Field", "Type", "Required", "Description"}, rows)
			}
		}
		if ex := get(m, "example"); ex != nil {
			p.Para("Example " + what + ":")
			p.CodeBlock("", exampleText(ex))
		}
		each(get(m, "examples"), func(name string, ex *yaml.Node) {
			ex = r.deref(ex)
			p.Para("Example " + reference.Code(name) + ", " + what + ": " + str(get(ex, "summary")))
			p.CodeBlock("", exampleText(get(ex, "value")))
		})
	})
}

// content describes the media types of a request or response body.
func (r *renderer) content(c *yaml.Node) string {
	var out []string
	each(c, func(media string, m *yaml.Node) {
		entry := reference.Code(media)
		if s := get(m, "schema"); s != nil {
			entry += " " + r.typeOf(s)
		}
		out = append(out, entry)
	})
	return strings.Join(out, "; ")
}

func (r *renderer) components() {
	if len(items(r.schemas)) == 0 {
		return
	}
	p := r.page
	p.Heading(2, "Schemas")
	each(r.schemas, func(name string, s *yaml.Node) {
		p.Heading(3, reference.Code(name))
		if file, ok := r.shared[name]; ok {
			p.Para("Defined in " + reference.Code(file) + ", which other contracts share.")
		}
		p.Para(str(get(s, "description")))
		if get(s, "properties") == nil {
			p.Para("Type: " + r.typeOf(s) + ". " + strings.Join(constraints(s), " "))
		}
		if d := get(s, "discriminator"); d != nil {
			var cases []string
			each(get(d, "mapping"), func(value string, ref *yaml.Node) {
				cases = append(cases, reference.Code(value)+" selects "+r.link(ref.Value))
			})
			p.Para("The " + reference.Code(str(get(d, "propertyName"))) + " property selects the variant: " + strings.Join(cases, ", ") + ".")
		}
		var rows [][]string
		r.rows(s, "", &rows)
		p.Table([]string{"Field", "Type", "Required", "Description"}, rows)
		if hasRules(s) {
			p.Para("Further rules (conditional requirements or combinations) are in the full schema below.")
		}
		for _, ex := range r.examples[name] {
			p.Para("Example " + reference.Code(ex.Name) + ":")
			var buf bytes.Buffer
			if err := json.Indent(&buf, ex.Value, "", "  "); err != nil {
				p.Fail(fmt.Errorf("example %s: %w", ex.Name, err))
			}
			p.CodeBlock("json", buf.String())
		}
		p.Details("Full schema", func() { p.CodeBlock("yaml", dump(s)) })
	})
}

// rows lists the properties of an object schema, descending into inline
// objects, arrays of inline objects (name[].) and maps of inline objects
// (name.*.) with dotted names.
func (r *renderer) rows(s *yaml.Node, prefix string, out *[][]string) {
	required := map[string]bool{}
	for _, n := range items(get(s, "required")) {
		required[n.Value] = true
	}
	each(get(s, "properties"), func(name string, prop *yaml.Node) {
		field := prefix + name
		*out = append(*out, []string{reference.Code(field), r.typeOf(prop), yes(required[name]), r.details(prop, prop)})
		switch {
		case hasFields(prop):
			r.rows(prop, field+".", out)
		case hasFields(get(prop, "items")):
			r.rows(get(prop, "items"), field+"[].", out)
		}
	})
	if ap := get(s, "additionalProperties"); hasFields(ap) {
		r.rows(ap, prefix+"*.", out)
	}
}

// hasFields reports whether s is an inline schema with properties of its own
// or map values that have some.
func hasFields(s *yaml.Node) bool {
	if s == nil || s.Kind != yaml.MappingNode || get(s, "$ref") != nil {
		return false
	}
	return get(s, "properties") != nil || hasFields(get(s, "additionalProperties"))
}

// details is a description followed by the constraints of schema.
func (r *renderer) details(described, schema *yaml.Node) string {
	parts := []string{str(get(described, "description"))}
	parts = append(parts, constraints(schema)...)
	return strings.TrimSpace(strings.Join(parts, " "))
}

func hasRules(s *yaml.Node) bool {
	for _, k := range []string{"if", "allOf", "not", "dependentRequired", "dependentSchemas", "patternProperties", "propertyNames"} {
		if get(s, k) != nil {
			return true
		}
	}
	return get(s, "type") != nil && (get(s, "anyOf") != nil || get(s, "oneOf") != nil)
}

func (r *renderer) link(ref string) string {
	name, ok := strings.CutPrefix(ref, schemaRef)
	if !ok || get(r.schemas, name) == nil {
		r.page.Fail(fmt.Errorf("reference to unknown schema %s", ref))
		return reference.Code(ref)
	}
	title := reference.Code(name)
	return reference.Link(title, reference.Anchor(title))
}

// typeOf describes the type of schema in a few words.
func (r *renderer) typeOf(s *yaml.Node) string {
	if s == nil {
		return "any"
	}
	if ref := str(get(s, "$ref")); ref != "" {
		return r.link(ref)
	}
	if t := get(s, "type"); t != nil {
		var names []string
		if t.Kind == yaml.SequenceNode {
			for _, n := range t.Content {
				names = append(names, n.Value)
			}
		} else {
			names = []string{t.Value}
		}
		for i, n := range names {
			switch n {
			case "array":
				names[i] = "array of " + r.typeOf(get(s, "items"))
			case "object":
				if ap := get(s, "additionalProperties"); ap != nil && ap.Kind == yaml.MappingNode && get(s, "properties") == nil {
					names[i] = "map of " + r.typeOf(ap)
				} else {
					names[i] = "object"
				}
			default:
				if f := str(get(s, "format")); f != "" {
					n += " (" + f + ")"
				}
				names[i] = n
			}
		}
		return strings.Join(names, " or ")
	}
	for _, k := range []string{"oneOf", "anyOf"} {
		if v := get(s, k); v != nil {
			var alts []string
			for _, alt := range v.Content {
				alts = append(alts, r.typeOf(alt))
			}
			return "one of " + strings.Join(alts, ", ")
		}
	}
	if c := get(s, "const"); c != nil {
		return "constant"
	}
	return "any"
}

// constraints lists the validation keywords of schema as sentences.
func constraints(s *yaml.Node) []string {
	var out []string
	add := func(format string, key string) {
		if v := get(s, key); v != nil {
			out = append(out, fmt.Sprintf(format, scalar(v)))
		}
	}
	if e := get(s, "enum"); e != nil {
		var values []string
		for _, v := range e.Content {
			values = append(values, scalar(v))
		}
		out = append(out, "One of "+strings.Join(values, ", ")+".")
	}
	add("Always %s.", "const")
	add("Default %s.", "default")
	add("Pattern %s.", "pattern")
	add("Minimum length %s.", "minLength")
	add("Maximum length %s.", "maxLength")
	add("Minimum %s.", "minimum")
	add("Maximum %s.", "maximum")
	add("Greater than %s.", "exclusiveMinimum")
	add("Less than %s.", "exclusiveMaximum")
	add("At least %s items.", "minItems")
	add("At most %s items.", "maxItems")
	add("At least %s properties.", "minProperties")
	add("At most %s properties.", "maxProperties")
	if str(get(s, "uniqueItems")) == "true" {
		out = append(out, "Items are unique.")
	}
	if str(get(s, "readOnly")) == "true" {
		out = append(out, "Read-only.")
	}
	if str(get(s, "writeOnly")) == "true" {
		out = append(out, "Write-only.")
	}
	if str(get(s, "deprecated")) == "true" {
		out = append(out, "Deprecated.")
	}
	if items := get(s, "items"); items != nil && get(items, "$ref") == nil {
		if nested := constraints(items); len(nested) > 0 {
			out = append(out, "Each item: "+strings.Join(nested, " "))
		}
	}
	return out
}

// scalar renders a value as inline code: scalars verbatim, others as JSON.
func scalar(n *yaml.Node) string {
	if n.Kind == yaml.ScalarNode {
		return reference.Code(n.Value)
	}
	var v any
	_ = n.Decode(&v)
	b, _ := json.Marshal(v)
	return reference.Code(string(b))
}

func exampleText(n *yaml.Node) string {
	if n.Kind == yaml.ScalarNode {
		return n.Value
	}
	var v any
	_ = n.Decode(&v)
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func yes(b bool) string {
	if b {
		return "yes"
	}
	return ""
}

// deref follows a local $ref to a reusable component (parameter, body, response).
func (r *renderer) deref(n *yaml.Node) *yaml.Node {
	ref := str(get(n, "$ref"))
	if ref == "" {
		return n
	}
	if strings.HasPrefix(ref, "#") {
		if target := resolve(r.root, strings.TrimPrefix(ref, "#")); target != nil {
			return target
		}
	}
	r.page.Fail(fmt.Errorf("unresolved reference %s", ref))
	return n
}

// dump renders a schema as YAML, without comments.
func dump(s *yaml.Node) string {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	_ = enc.Encode(clean(s, false))
	_ = enc.Close()
	return buf.String()
}

// get returns the value of key in a mapping node, or nil.
func get(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// each calls fn for every key of a mapping node, in source order.
func each(m *yaml.Node, fn func(key string, value *yaml.Node)) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		fn(m.Content[i].Value, m.Content[i+1])
	}
}

// items returns the children of n, or nil when n is absent.
func items(n *yaml.Node) []*yaml.Node {
	if n == nil {
		return nil
	}
	return n.Content
}

func str(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

// resolve follows a JSON Pointer such as /$defs/Part from n.
func resolve(n *yaml.Node, pointer string) *yaml.Node {
	if pointer == "" || pointer == "/" {
		return n
	}
	for _, token := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		if n = get(n, token); n == nil {
			return nil
		}
	}
	return n
}

// clean deep-copies n without comments; plain also drops the JSON quoting and
// flow style a JSON source carries, so it prints like the YAML contract.
func clean(n *yaml.Node, plain bool) *yaml.Node {
	c := *n
	c.HeadComment, c.LineComment, c.FootComment = "", "", ""
	if plain {
		c.Style = 0
	}
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, child := range n.Content {
		c.Content[i] = clean(child, plain)
	}
	return &c
}

func rewriteRefs(n *yaml.Node, from, to string) {
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if v := n.Content[i+1]; n.Content[i].Value == "$ref" && v.Kind == yaml.ScalarNode {
				if strings.HasPrefix(v.Value, from) {
					v.Value = to + strings.TrimPrefix(v.Value, from)
				}
			}
		}
	}
	for _, child := range n.Content {
		rewriteRefs(child, from, to)
	}
}
