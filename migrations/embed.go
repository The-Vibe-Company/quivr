// Package migrations holds the authoritative, ordered PostgreSQL migration set.
//
// New migrations are named <UTC YYYYMMDDTHHMMZ>_<slug>.sql (make migration
// name=<slug>), so parallel branches never pick the same name. The numbered
// 0xx_ files are a closed legacy set that sorts before every stamped file.
// Migrations apply in lexical filename order; readiness requires every expand
// migration. Tagged contracts run only by explicit operator request.
package migrations

import "embed"

// Files is the authoritative ordered migration set.
//
//go:embed *.sql
var Files embed.FS
