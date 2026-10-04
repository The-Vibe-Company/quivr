package quivrplugin

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// schemaFiles are byte-identical copies of contracts/plugins/v0 and
// contracts/shared/v0, kept in sync by scripts/sync_schemas.py.
//
//go:embed schemas
var schemaFiles embed.FS

const schemaBase = "https://quivr.invalid/contracts/"

var (
	compileOnce sync.Once
	compiler    *jsonschema.Compiler
	compileErr  error
	compiledMu  sync.Mutex
	compiled    = map[string]*jsonschema.Schema{}
)

func protocolSchema(name string) (*jsonschema.Schema, error) {
	compileOnce.Do(func() {
		compiler = jsonschema.NewCompiler()
		compileErr = fs.WalkDir(schemaFiles, "schemas", func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			raw, err := schemaFiles.ReadFile(path)
			if err != nil {
				return err
			}
			doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			if err != nil {
				return err
			}
			return compiler.AddResource(schemaBase+strings.TrimPrefix(path, "schemas/"), doc)
		})
	})
	if compileErr != nil {
		return nil, compileErr
	}
	compiledMu.Lock()
	defer compiledMu.Unlock()
	if s, ok := compiled[name]; ok {
		return s, nil
	}
	s, err := compiler.Compile(schemaBase + name)
	if err != nil {
		return nil, err
	}
	compiled[name] = s
	return s, nil
}

// validate checks a JSON document against a Plugin Protocol schema and
// returns the first failing leaf as a JSON Pointer and a message.
func validate(name string, doc []byte) error {
	schema, err := protocolSchema(name)
	if err != nil {
		return err
	}
	return validateWith(schema, doc)
}

func validateWith(schema *jsonschema.Schema, doc []byte) error {
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if err != nil {
		return fmt.Errorf("not JSON: %w", err)
	}
	err = schema.Validate(instance)
	var verr *jsonschema.ValidationError
	if errors.As(err, &verr) {
		return firstLeaf(verr)
	}
	return err
}

func firstLeaf(e *jsonschema.ValidationError) error {
	for len(e.Causes) > 0 {
		e = e.Causes[0]
	}
	path := "/" + strings.Join(e.InstanceLocation, "/")
	return fmt.Errorf("%s: %s", path, strings.TrimPrefix(e.Error(), fmt.Sprintf("at '%s': ", path)))
}

type denyLoader struct{}

func (denyLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema references are not allowed (%s)", url)
}

// compileDeclared compiles a schema the manifest declares (config or
// credential), without resolving external references.
func compileDeclared(raw []byte) (*jsonschema.Schema, error) {
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(denyLoader{})
	const url = "https://quivr.invalid/plugin/declared-schema.json"
	if err := c.AddResource(url, value); err != nil {
		return nil, err
	}
	return c.Compile(url)
}
