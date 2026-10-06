module github.com/The-Vibe-Company/quivr/plugins/m365-mail

go 1.27.1

require (
	github.com/The-Vibe-Company/quivr/sdks/go v0.0.0
	github.com/The-Vibe-Company/quivr/tests/fakes v0.0.0
	golang.org/x/net v0.59.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/The-Vibe-Company/quivr/sdks/go => ../../sdks/go

replace github.com/The-Vibe-Company/quivr/tests/fakes => ../../tests/fakes
