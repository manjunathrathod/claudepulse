// Package webassets embeds the UI templates and static files so the binary is
// self-contained. internal/web renders them.
package webassets

import "embed"

// FS holds templates/** and static/**.
//
//go:embed all:templates all:static
var FS embed.FS
