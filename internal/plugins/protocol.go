package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/The-Vibe-Company/quivr-v2/contracts"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

var (
	schemasOnce sync.Once
	schemas     map[string]*jsonschema.Schema
	schemasErr  error
)

func protocolSchema(file string) (*jsonschema.Schema, error) {
	schemasOnce.Do(func() {
		compiler, err := contracts.NewCompiler()
		if err != nil {
			schemasErr = err
			return
		}
		schemas = map[string]*jsonschema.Schema{}
		for name := range contracts.PluginSchemas() {
			if schemas[name], err = compiler.Compile(contracts.PluginSchema(name)); err != nil {
				schemasErr = err
				return
			}
		}
	})
	if schemasErr != nil {
		return nil, schemasErr
	}
	schema, ok := schemas[file]
	if !ok {
		return nil, fmt.Errorf("unknown Plugin Protocol schema %q", file)
	}
	return schema, nil
}

func pointer(tokens []string) string {
	var sb strings.Builder
	for _, token := range tokens {
		sb.WriteString("/" + pointerToken(token))
	}
	return sb.String()
}

func decodeInstance(raw []byte) (any, error) {
	return jsonschema.UnmarshalJSON(bytes.NewReader(raw))
}

// validateAgainst returns one schema_violation issue per failing leaf.
func validateAgainst(file string, instance any) ([]Issue, error) {
	schema, err := protocolSchema(file)
	if err != nil {
		return nil, err
	}
	err = schema.Validate(instance)
	if err == nil {
		return nil, nil
	}
	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		return nil, err
	}
	return leafIssues(verr, CodeSchema, ""), nil
}

// leafIssues returns one issue per failing leaf of a validation error, with
// paths prefixed by prefix.
func leafIssues(verr *jsonschema.ValidationError, code, prefix string) []Issue {
	var issues []Issue
	seen := map[Issue]bool{}
	var walk func(*jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) > 0 {
			for _, cause := range e.Causes {
				walk(cause)
			}
			return
		}
		path := pointer(e.InstanceLocation)
		message := strings.TrimPrefix(e.Error(), fmt.Sprintf("at '%s': ", path))
		issue := Issue{Code: code, Path: prefix + path, Message: message}
		if !seen[issue] {
			seen[issue] = true
			issues = append(issues, issue)
		}
	}
	walk(verr)
	return issues
}

// ValidateConfiguration validates plugin configuration (a JSON object) against
// the manifest configuration schema, as the engine does before an invocation.
// A manifest without a configuration schema accepts any object. Issue paths
// start with /configuration.
func ValidateConfiguration(m *Manifest, config []byte) []Issue {
	instance, err := decodeInstance(config)
	if err != nil {
		return []Issue{{Code: CodeInvalidConfiguration, Path: "/configuration", Message: "not JSON: " + err.Error()}}
	}
	if _, ok := instance.(map[string]any); !ok {
		return []Issue{{Code: CodeInvalidConfiguration, Path: "/configuration", Message: "configuration must be a JSON object"}}
	}
	if m == nil || m.Configuration == nil {
		return nil
	}
	schemaValue, err := decodeInstance(m.Configuration.Schema)
	if err != nil {
		return []Issue{{Code: CodeInvalidConfigSchema, Path: "/configuration/schema", Message: err.Error()}}
	}
	schema, err := compileUserSchema(schemaValue)
	if err != nil {
		return []Issue{{Code: CodeInvalidConfigSchema, Path: "/configuration/schema", Message: err.Error()}}
	}
	err = schema.Validate(instance)
	var verr *jsonschema.ValidationError
	if errors.As(err, &verr) {
		return leafIssues(verr, CodeInvalidConfiguration, "/configuration")
	}
	if err != nil {
		return []Issue{{Code: CodeInvalidConfiguration, Path: "/configuration", Message: err.Error()}}
	}
	return nil
}

// ValidateDocument validates raw JSON against one Plugin Protocol v0 schema
// file, such as "discovery.schema.json".
func ValidateDocument(file string, raw []byte) []Issue {
	instance, err := decodeInstance(raw)
	if err != nil {
		return []Issue{{Code: CodeSchema, Message: "not JSON: " + err.Error()}}
	}
	issues, err := validateAgainst(file, instance)
	if err != nil {
		return []Issue{{Code: CodeSchema, Message: err.Error()}}
	}
	return issues
}

// ValidateNormalizerResponse validates a normalizer response body: the
// response schema, then the engine's own structural Manifest rules
// (content.CheckManifest), exactly as for a kind "manifest" submission.
// Blob verification, response size, the input-Blob-only rule and namespace
// ownership need invocation context: CheckNormalizerOutput adds them.
func ValidateNormalizerResponse(raw []byte) []Issue {
	if issues := ValidateDocument("normalizer-response.schema.json", raw); len(issues) > 0 {
		return issues
	}
	var response struct {
		Manifest content.Manifest `json:"manifest"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return []Issue{{Code: CodeSchema, Path: "/manifest", Message: err.Error()}}
	}
	if err := content.CheckManifest(&response.Manifest, nil); err != nil {
		message := err.Error()
		var violation *content.ManifestViolation
		if errors.As(err, &violation) {
			message = violation.Detail + " (engine code " + violation.Error() + ")"
		}
		return []Issue{{Code: CodeInvalidManifest, Path: "/manifest", Message: message}}
	}
	return nil
}

type denyLoader struct{}

func (denyLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema references are not allowed (%s)", url)
}

// compileUserSchema checks that a plugin-declared value is a valid JSON Schema
// 2020-12 document without resolving external references.
func compileUserSchema(value any) (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(denyLoader{})
	const url = "https://quivr.invalid/plugin/declared-schema.json"
	if err := compiler.AddResource(url, value); err != nil {
		return nil, err
	}
	return compiler.Compile(url)
}
