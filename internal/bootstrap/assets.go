// Package bootstrap installs missing first-run resources into Horizon home.
package bootstrap

import _ "embed"

//go:embed assets/config.yaml
var configTemplate []byte

//go:embed assets/skills/skill-creator/SKILL.md
var skillTemplate []byte
