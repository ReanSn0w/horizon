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
	Executable     string
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
	if opts.Executable != "" {
		blocks = append(blocks, skillManagementInstructions(opts.Executable, home))
	}
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

func skillManagementInstructions(executable, home string) string {
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	prefix := quote(executable) + " --home " + quote(home) + " skills "
	return "## Skill management\n" +
		"Use shell_exec to run these local commands; they do not contact the provider. Use the exact executable and home paths below.\n" +
		prefix + "list\n" +
		prefix + "validate --id 'SKILL_ID'\n" +
		prefix + "enable --id 'SKILL_ID'\n" +
		prefix + "disable --id 'SKILL_ID'\n" +
		"Replace SKILL_ID with the skill directory ID shown by skills list. skill_read takes the YAML name, not the directory ID.\n" +
		"After creating or editing a skill, validate its current file with this command, fix reported formatting problems, and validate again.\n" +
		"Enable or disable skills when the user's task calls for it. Changes apply to the next turn; the current skill snapshot stays unchanged. Disabling does not delete files."
}
