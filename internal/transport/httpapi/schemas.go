package httpapi

import (
	"sync"

	"github.com/The-Vibe-Company/quivr-v2/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// apiContract holds the shared compiled schemas and declared routing aliases.
type apiContract struct {
	schemas map[string]*jsonschema.Schema
	aliases map[string]string
}

// loadContract compiles request and response components once. Invalid schemas
// fail API startup before requests are served.
var loadContract = sync.OnceValues(func() (*apiContract, error) {
	var doc struct {
		Aliases    map[string]string `yaml:"x-quivr-route-aliases"`
		Components struct {
			Schemas map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(contracts.OpenAPI(), &doc); err != nil {
		return nil, err
	}
	registry, err := contracts.HTTP()
	if err != nil {
		return nil, err
	}
	schemas := make(map[string]*jsonschema.Schema, len(doc.Components.Schemas))
	for name := range doc.Components.Schemas {
		schema, err := registry.Schema(contracts.HTTPSchema(name))
		if err != nil {
			return nil, err
		}
		schemas[name] = schema
	}
	return &apiContract{schemas: schemas, aliases: doc.Aliases}, nil
})
