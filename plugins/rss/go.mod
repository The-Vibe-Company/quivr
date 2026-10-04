module github.com/The-Vibe-Company/quivr/plugins/rss

go 1.25.0

require (
	github.com/The-Vibe-Company/quivr/sdks/go v0.0.0
	github.com/mmcdole/gofeed v1.5.0
	golang.org/x/net v0.58.0
)

require (
	github.com/mmcdole/goxpp/v2 v2.0.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	golang.org/x/text v0.41.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/The-Vibe-Company/quivr/sdks/go => ../../sdks/go
