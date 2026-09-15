package contract

import _ "embed"

// OpenAPI is the authoritative public contract embedded in the binary.
//
//go:embed openapi.yaml
var OpenAPI []byte
