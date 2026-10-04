module github.com/The-Vibe-Company/quivr/plugins/core-ingest

go 1.25.0

require github.com/The-Vibe-Company/quivr/sdks/go v0.0.0

require (
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	golang.org/x/text v0.14.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/The-Vibe-Company/quivr/sdks/go => ../../sdks/go
