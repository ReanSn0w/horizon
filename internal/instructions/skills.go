package instructions

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var ErrSkillNotFound = errors.New("skill not found")

type Skill struct {
	Name        string
	Description string
	Content     string
	BaseDir     string
}

type SkillSummary struct {
	Name        string
	Description string
}

type Catalog struct {
	byName map[string]Skill
	names  []string
}

func ScanSkills(home string) (*Catalog, error) {
	root := filepath.Join(home, "skills")
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return &Catalog{byName: map[string]Skill{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan skills directory %q: %w", root, err)
	}
	catalog := &Catalog{byName: make(map[string]Skill)}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		base, err := filepath.Abs(filepath.Join(root, entry.Name()))
		if err != nil {
			return nil, err
		}
		path := filepath.Join(base, "SKILL.md")
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read skill %q: %w", path, err)
		}
		skill, err := parseSkill(data, base)
		if err != nil {
			return nil, fmt.Errorf("invalid skill %q: %w", path, err)
		}
		if previous, exists := catalog.byName[skill.Name]; exists {
			return nil, fmt.Errorf("ambiguous skill name %q in %q and %q", skill.Name, previous.BaseDir, skill.BaseDir)
		}
		catalog.byName[skill.Name] = skill
		catalog.names = append(catalog.names, skill.Name)
	}
	sort.Strings(catalog.names)
	return catalog, nil
}

func parseSkill(data []byte, base string) (Skill, error) {
	reader := bufio.NewReader(bytes.NewReader(data))
	first, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return Skill{}, err
	}
	if strings.TrimSpace(first) != "---" {
		return Skill{}, errors.New("missing YAML frontmatter")
	}
	var header bytes.Buffer
	for {
		line, readErr := reader.ReadString('\n')
		if strings.TrimSpace(line) == "---" {
			break
		}
		header.WriteString(line)
		if errors.Is(readErr, io.EOF) {
			return Skill{}, errors.New("unterminated YAML frontmatter")
		}
		if readErr != nil {
			return Skill{}, readErr
		}
	}
	var metadata struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal(header.Bytes(), &metadata); err != nil {
		return Skill{}, err
	}
	if strings.TrimSpace(metadata.Name) == "" || strings.TrimSpace(metadata.Description) == "" {
		return Skill{}, errors.New("name and description are required")
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return Skill{}, err
	}
	return Skill{Name: metadata.Name, Description: metadata.Description, Content: string(body), BaseDir: base}, nil
}

func (catalog *Catalog) Summaries() []SkillSummary {
	result := make([]SkillSummary, 0, len(catalog.names))
	for _, name := range catalog.names {
		skill := catalog.byName[name]
		result = append(result, SkillSummary{Name: skill.Name, Description: skill.Description})
	}
	return result
}

func (catalog *Catalog) Read(name string) (Skill, error) {
	skill, ok := catalog.byName[name]
	if !ok {
		return Skill{}, fmt.Errorf("skill %q: %w", name, ErrSkillNotFound)
	}
	return skill, nil
}
