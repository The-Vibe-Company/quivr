package migrations

import "embed"

// Files is the authoritative ordered migration set.
//
//go:embed *.sql
var Files embed.FS
