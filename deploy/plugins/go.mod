module github.com/The-Vibe-Company/quivr/deploy/plugins

go 1.27.4

require (
	github.com/The-Vibe-Company/quivr/plugins/core-ingest v0.0.0
	github.com/The-Vibe-Company/quivr/plugins/core-retrieve v0.0.0
	github.com/The-Vibe-Company/quivr/plugins/m365-mail v0.0.0
	github.com/The-Vibe-Company/quivr/plugins/newsroom-retrieve v0.0.0
	github.com/The-Vibe-Company/quivr/plugins/object-storage-archive v0.0.0
	github.com/The-Vibe-Company/quivr/plugins/rss v0.0.0
	github.com/The-Vibe-Company/quivr/plugins/text-embed v0.0.0
	github.com/The-Vibe-Company/quivr/plugins/x-list v0.0.0
)

require (
	github.com/The-Vibe-Company/quivr/sdks/go v0.0.0 // indirect
	github.com/aws/aws-sdk-go-v2 v1.47.4 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.24 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.20.10 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.7 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.7 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.22 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.11.8 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.20.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/s3 v1.114.5 // indirect
	github.com/aws/smithy-go v1.28.6 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/mmcdole/gofeed v1.5.0 // indirect
	github.com/mmcdole/goxpp/v2 v2.0.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/The-Vibe-Company/quivr/plugins/core-ingest => ../../plugins/core-ingest
	github.com/The-Vibe-Company/quivr/plugins/core-retrieve => ../../plugins/core-retrieve
	github.com/The-Vibe-Company/quivr/plugins/m365-mail => ../../plugins/m365-mail
	github.com/The-Vibe-Company/quivr/plugins/newsroom-retrieve => ../../plugins/newsroom-retrieve
	github.com/The-Vibe-Company/quivr/plugins/object-storage-archive => ../../plugins/object-storage-archive
	github.com/The-Vibe-Company/quivr/plugins/rss => ../../plugins/rss
	github.com/The-Vibe-Company/quivr/plugins/text-embed => ../../plugins/text-embed
	github.com/The-Vibe-Company/quivr/plugins/x-list => ../../plugins/x-list
	github.com/The-Vibe-Company/quivr/sdks/go => ../../sdks/go
	github.com/The-Vibe-Company/quivr/tests/fakes => ../../tests/fakes
)
