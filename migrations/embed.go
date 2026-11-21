// Package migrations embeds the SQL migration files.
package migrations

import "embed"

// FS holds the *.sql migrations (golang-migrate naming: NNNN_name.up.sql / .down.sql).
//
//go:embed *.sql
var FS embed.FS
