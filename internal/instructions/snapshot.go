package instructions

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type Snapshot struct {
	Prompt string
	Skills *Catalog
	Global string
	Local  string
	Intro  string
}

type Options struct {
	DisabledSkills []string
}

func Build(home, workspace, introduction string, options ...Options) (*Snapshot, error) {
	var opts Options
	if len(options) > 0 {
		opts = options[0]
	}

	global, err := readOptional(filepath.Join(home, "AGENTS.md"))
	if err != nil {
		return nil, err
	}
	catalog, err := ScanSkills(home, opts.DisabledSkills...)
	if err != nil {
		return nil, err
	}
	local, err := readOptional(filepath.Join(workspace, "AGENTS.md"))
	if err != nil {
		return nil, err
	}
	snapshot := &Snapshot{Skills: catalog, Global: global, Local: local, Intro: introduction}
	var blocks []string
	if introduction != "" {
		blocks = append(blocks, introduction)
	}
	if global != "" {
		blocks = append(blocks, global)
	}
	blocks = append(blocks, renderCatalog(catalog.Summaries()))
	blocks = append(blocks, skillManagementInstructions())
	if local != "" {
		blocks = append(blocks, local)
	}
	snapshot.Prompt = strings.Join(blocks, "\n\n")
	return snapshot, nil
}

func readOptional(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read instructions %q: %w", path, err)
	}
	return string(data), nil
}

func renderCatalog(skills []SkillSummary) string {
	var builder strings.Builder
	builder.WriteString("## Available skills")
	for _, skill := range skills {
		fmt.Fprintf(&builder, "\n- %s: %s [ID: %s]", skill.Name, skill.Description, skill.ID)
	}
	return builder.String()
}

func skillManagementInstructions() string {
	return "## Skill management\n" +
		"Run `horizon skills list` through shell_exec to see skill IDs and status. " +
		"After creating or editing a skill, run `horizon skills validate --id ID` and fix reported errors. " +
		"Use `horizon skills enable --id ID` or `horizon skills disable --id ID` when requested. " +
		"ID is the directory name; skill_read uses the YAML name. Changes apply from the next turn."
}
