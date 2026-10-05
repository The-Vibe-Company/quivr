module github.com/The-Vibe-Company/quivr/tests/fakes/load-plugin

go 1.24

require github.com/The-Vibe-Company/quivr/sdks/go v0.0.0

require (
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	golang.org/x/text v0.14.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/The-Vibe-Company/quivr/sdks/go => ../../../sdks/go
