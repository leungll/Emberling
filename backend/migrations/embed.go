// Package migrations embeds the goose SQL migrations so that a Backend binary can run
// them before readiness without shipping the repository tree.
package migrations

import "embed"

// FS holds every migration file in this directory, in goose naming order.
//
//go:embed *.sql
var FS embed.FS
