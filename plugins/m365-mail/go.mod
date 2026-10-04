module github.com/The-Vibe-Company/quivr/plugins/m365-mail

go 1.27.1

require (
	github.com/The-Vibe-Company/quivr/sdks/go v0.0.0
	github.com/The-Vibe-Company/quivr/tests/fakes v0.0.0
	golang.org/x/net v0.59.0
)

require (
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	golang.org/x/text v0.42.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/The-Vibe-Company/quivr/sdks/go => ../../sdks/go

replace github.com/The-Vibe-Company/quivr/tests/fakes => ../../tests/fakes
