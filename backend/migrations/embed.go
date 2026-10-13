// Package migrations embeds the forward-only SQL migration files applied by
// internal/storage/mysql. Files are named NNNN_slug.sql and applied in
// filename order (ADR-0002).
package migrations

import "embed"

// FS holds the numbered .sql migration files.
//
//go:embed *.sql
var FS embed.FS
