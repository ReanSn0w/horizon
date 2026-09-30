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
	ID          string
	Name        string
	Description string
	Content     string
	BaseDir     string
}

type SkillSummary struct {
	ID          string
	Name        string
	Description string
}

type Catalog struct {
	byName map[string]Skill
	names  []string
}

// SkillEntry keeps discovery usable even when an individual file is broken.
type SkillEntry struct {
	ID    string
	Path  string
	Skill Skill
	Err   error
}

func ValidSkillID(id string) bool {
	return id != "" && id != "." && id != ".." && !filepath.IsAbs(id) && !strings.ContainsAny(id, "/\\\x00")
}

func SkillDirectory(home, id string) (string, error) {
	if !ValidSkillID(id) {
		return "", fmt.Errorf("invalid skill ID %q", id)
	}
	base, err := filepath.Abs(filepath.Join(home, "skills", id))
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(base)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("skill %q: %w", id, ErrSkillNotFound)
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("skill %q is not a directory", id)
	}
	return base, nil
}

func skillIDs(home string) ([]string, error) {
	root := filepath.Join(home, "skills")
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan skills directory %q: %w", root, err)
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() {
			ids = append(ids, entry.Name())
		}
	}
	return ids, nil
}

func InspectSkill(home, id string) (Skill, error) {
	base, err := SkillDirectory(home, id)
	if err != nil {
		return Skill{}, err
	}
	path := filepath.Join(base, "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, fmt.Errorf("read skill %q at %q: %w", id, path, err)
	}
	skill, err := parseSkill(data, base)
	skill.ID = id
	if err != nil {
		return skill, fmt.Errorf("invalid skill %q at %q: %w", id, path, err)
	}
	return skill, nil
}

func ListSkills(home string) ([]SkillEntry, error) {
	ids, err := skillIDs(home)
	if err != nil {
		return nil, err
	}
	entries := make([]SkillEntry, 0, len(ids))
	for _, id := range ids {
		skill, err := InspectSkill(home, id)
		entries = append(entries, SkillEntry{ID: id, Path: filepath.Join(home, "skills", id, "SKILL.md"), Skill: skill, Err: err})
	}
	return entries, nil
}

func ScanSkills(home string, disabled ...string) (*Catalog, error) {
	ids, err := skillIDs(home)
	if err != nil {
		return nil, err
	}
	excluded := make(map[string]bool, len(disabled))
	for _, id := range disabled {
		excluded[id] = true
	}
	catalog := &Catalog{byName: make(map[string]Skill)}
	for _, id := range ids {
		if excluded[id] {
			continue
		}
		skill, err := InspectSkill(home, id)
		if err != nil {
			return nil, err
		}
		if previous, exists := catalog.byName[skill.Name]; exists {
			return nil, fmt.Errorf("ambiguous skill name %q in %q (ID %s) and %q (ID %s)", skill.Name, previous.BaseDir, previous.ID, skill.BaseDir, skill.ID)
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
		result = append(result, SkillSummary{ID: skill.ID, Name: skill.Name, Description: skill.Description})
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
