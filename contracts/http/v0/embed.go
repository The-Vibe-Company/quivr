package contract

import _ "embed"

// OpenAPI is the authoritative public contract embedded in the binary.
//
//go:embed openapi.yaml
var OpenAPI []byte

// WebhookVector is the public Standard Webhooks interoperability vector.
//
//go:embed webhook-vector.json
var WebhookVector []byte
