package contract

import _ "embed"

// The OpenAPI document references the shared Manifest schema; load it through
// the contracts package, which compiles every contract resource together.

// WebhookVector is the public Standard Webhooks interoperability vector.
//
//go:embed webhook-vector.json
var WebhookVector []byte
