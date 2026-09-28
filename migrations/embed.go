// Package migrations holds the authoritative, ordered PostgreSQL migration set.
//
// New migrations are named <UTC YYYYMMDDTHHMMZ>_<slug>.sql (make migration
// name=<slug>), so parallel branches never pick the same name. The numbered
// 0xx_ files are a closed legacy set that sorts before every stamped file.
// Migrations apply in lexical filename order; readiness requires every
// embedded migration to be applied, so adding one edits no other file.
package migrations

import "embed"

// Files is the authoritative ordered migration set.
//
//go:embed *.sql
var Files embed.FS
