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

func Build(home, workspace, introduction string) (*Snapshot, error) {
	global, err := readOptional(filepath.Join(home, "AGENTS.md"))
	if err != nil {
		return nil, err
	}
	catalog, err := ScanSkills(home)
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
		fmt.Fprintf(&builder, "\n- %s: %s", skill.Name, skill.Description)
	}
	return builder.String()
}
