// Package agentradar holds the files that ship inside the radar binary.
package agentradar

import _ "embed"

// SkillMD is the agent skill that `radar skill install` writes.
//
//go:embed skills/radar/SKILL.md
var SkillMD []byte
