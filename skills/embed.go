// Package skills embeds the agent skill shipped with wassup.
package skills

import _ "embed"

//go:embed wassup/SKILL.md
var WhatsApp string
