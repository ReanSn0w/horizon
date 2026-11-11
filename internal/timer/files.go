// Package timer loads timer definitions and calculates their firing times.
package timer

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/ReanSn0w/horizon/internal/session"
	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

const maxFileSize = 128 * 1024

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type Definition struct {
	ID          string
	Revision    [sha256.Size]byte
	Name        string
	Description string
	Workspace   string
	Schedule    ScheduleConfig
	Message     string
	Enabled     bool
}

type ScheduleConfig struct {
	Every    string         `yaml:"every"`
	Cron     string         `yaml:"cron"`
	Timezone string         `yaml:"timezone"`
	Windows  []WindowConfig `yaml:"windows"`
}

type WindowConfig struct {
	Start string `yaml:"start"`
	End   string `yaml:"end"`
}

type frontmatter struct {
	Name        string          `yaml:"name"`
	Description string          `yaml:"description"`
	Workspace   string          `yaml:"workspace"`
	Schedule    *ScheduleConfig `yaml:"schedule"`
	Enabled     bool            `yaml:"enabled"`
}

func ValidID(id string) bool { return idPattern.MatchString(id) }

func openCatalog(home string) (*os.File, error) {
	path := filepath.Join(home, "timers")
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open timer catalog", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}

type FileResult struct {
	ID         string
	Definition Definition
	Err        error
}

// LoadCatalog returns one result per immediate Markdown file. A broken file
// never hides the result for another file in the same directory.
func LoadCatalog(home string) ([]FileResult, error) {
	dir, err := openCatalog(home)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	var results []FileResult
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".md")
		if !ok || entry.IsDir() {
			continue
		}
		def, err := loadFile(dir, id)
		results = append(results, FileResult{ID: id, Definition: def, Err: err})
	}
	return results, nil
}

func LoadFile(home, id string) (Definition, error) {
	if !ValidID(id) {
		return Definition{}, invalidID(id)
	}
	dir, err := openCatalog(home)
	if err != nil {
		return Definition{}, err
	}
	defer dir.Close()
	return loadFile(dir, id)
}

func invalidID(id string) error {
	return fmt.Errorf("invalid timer-id %q: use 1–64 lowercase ASCII letters, digits, hyphens or underscores, starting with a letter or digit", id)
}

func loadFile(dir *os.File, id string) (Definition, error) {
	if !ValidID(id) {
		return Definition{}, invalidID(id)
	}
	name := id + ".md"
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return Definition{}, &os.PathError{Op: "open timer file", Path: name, Err: err}
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Definition{}, err
	}
	if !info.Mode().IsRegular() {
		return Definition{}, errors.New("timer file must be a regular Markdown file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFileSize+1))
	if err != nil {
		return Definition{}, err
	}
	if len(data) > maxFileSize {
		return Definition{}, fmt.Errorf("timer file exceeds %d bytes", maxFileSize)
	}
	return parseFile(id, data)
}

func parseFile(id string, data []byte) (Definition, error) {
	if !utf8.Valid(data) {
		return Definition{}, errors.New("timer file must be UTF-8")
	}
	normalized := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	head, tail, ok := bytes.Cut(normalized, []byte("\n"))
	if !ok || !bytes.Equal(head, []byte("---")) {
		return Definition{}, errors.New("timer file must begin with YAML frontmatter delimited by ---")
	}
	index := bytes.Index(tail, []byte("\n---\n"))
	if index < 0 {
		return Definition{}, errors.New("missing closing --- delimiter or Markdown task")
	}
	header := tail[:index]
	var raw yaml.Node
	if err := yaml.Unmarshal(header, &raw); err != nil {
		return Definition{}, fmt.Errorf("frontmatter: %w", err)
	}
	if len(raw.Content) != 1 || raw.Content[0].Kind != yaml.MappingNode {
		return Definition{}, errors.New("frontmatter must be a YAML mapping")
	}
	if err := checkYAML(raw.Content[0], ""); err != nil {
		return Definition{}, err
	}
	d := yaml.NewDecoder(bytes.NewReader(header))
	d.KnownFields(true)
	fields := frontmatter{Enabled: true}
	if err := d.Decode(&fields); err != nil {
		return Definition{}, fmt.Errorf("frontmatter: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return Definition{}, errors.New("frontmatter must contain one YAML document")
	}
	if strings.TrimSpace(fields.Name) == "" {
		return Definition{}, errors.New("name is required")
	}
	if strings.TrimSpace(fields.Description) == "" {
		return Definition{}, errors.New("description is required")
	}
	if !filepath.IsAbs(fields.Workspace) {
		return Definition{}, errors.New("workspace must be an absolute directory path")
	}
	workspace, err := session.NormalizeWorkspace(fields.Workspace)
	if err != nil {
		return Definition{}, fmt.Errorf("workspace: %w", err)
	}
	if fields.Schedule == nil {
		return Definition{}, errors.New("schedule is required")
	}
	message := strings.Trim(string(tail[index+len("\n---\n"):]), "\n")
	if strings.TrimSpace(message) == "" {
		return Definition{}, errors.New("Markdown task is required")
	}
	return Definition{
		ID: id, Revision: sha256.Sum256(data), Name: fields.Name, Description: fields.Description,
		Workspace: workspace, Schedule: *fields.Schedule, Message: message, Enabled: fields.Enabled,
	}, nil
}

func checkYAML(node *yaml.Node, path string) error {
	if node.Kind == yaml.AliasNode || node.Tag == "!!merge" {
		return fmt.Errorf("line %d: YAML aliases and merge keys are unsupported", node.Line)
	}
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			if key.Tag != "!!str" {
				return fmt.Errorf("line %d: YAML field names must be strings", key.Line)
			}
			field := key.Value
			if path != "" {
				field = path + "." + field
			}
			switch field {
			case "enabled":
				if value.Tag != "!!bool" {
					return fmt.Errorf("line %d: enabled must be a boolean", value.Line)
				}
			case "schedule.every", "schedule.cron":
				if value.Tag != "!!str" || strings.TrimSpace(value.Value) == "" {
					return fmt.Errorf("line %d: %s must be a nonempty string", value.Line, field)
				}
			}
			if err := checkYAML(value, field); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for i, child := range node.Content {
			if err := checkYAML(child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}
