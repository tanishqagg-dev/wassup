// Package docs embeds reference docs printed by the CLI.
package docs

import _ "embed"

//go:embed configuration.md
var Configuration string
