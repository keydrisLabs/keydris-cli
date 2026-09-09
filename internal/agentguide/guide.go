// Package agentguide embeds the versioned, secret-free agent guidance in every
// distribution of the CLI. It requires neither a network fetch nor a checkout.
package agentguide

import _ "embed"

//go:embed SKILL.md
var Skill string

//go:embed briefing.txt
var Briefing string
