package instructions

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

type SkillProblem struct {
	Code    string
	Field   string
	Line    int
	Message string
}

type SkillValidationError struct{ Problems []SkillProblem }

func (e *SkillValidationError) Error() string {
	lines := make([]string, 0, len(e.Problems))
	for _, p := range e.Problems {
		location := p.Code
		if p.Field != "" {
			location += " (" + p.Field + ")"
		}
		if p.Line > 0 {
			location += fmt.Sprintf(" at line %d", p.Line)
		}
		lines = append(lines, location+": "+p.Message)
	}
	return strings.Join(lines, "\n")
}

var yamlLine = regexp.MustCompile(`line ([0-9]+)`)

func parseSkill(data []byte, base string) (Skill, error) {
	skill := Skill{BaseDir: base}
	var problems []SkillProblem
	add := func(code, field string, line int, message string) {
		problems = append(problems, SkillProblem{code, field, line, message})
	}
	fail := func() (Skill, error) { return skill, &SkillValidationError{Problems: problems} }
	if !utf8.Valid(data) {
		add("invalid_utf8", "", 0, "SKILL.md must contain valid UTF-8")
		return fail()
	}
	reader := bufio.NewReader(bytes.NewReader(data))
	first, _ := reader.ReadString('\n')
	if strings.TrimSpace(first) != "---" {
		add("missing_frontmatter", "", 1, "expected opening ---")
		return fail()
	}
	var header bytes.Buffer
	for {
		line, err := reader.ReadString('\n')
		if strings.TrimSpace(line) == "---" {
			break
		}
		header.WriteString(line)
		if errors.Is(err, io.EOF) {
			add("unterminated_frontmatter", "", 1, "expected closing ---")
			return fail()
		}
		if err != nil {
			return skill, err
		}
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return skill, err
	}
	skill.Content = string(body)
	var document yaml.Node
	decoder := yaml.NewDecoder(&header)
	if err := decoder.Decode(&document); err != nil && !errors.Is(err, io.EOF) {
		line := 0
		if match := yamlLine.FindStringSubmatch(err.Error()); len(match) == 2 {
			n, _ := strconv.Atoi(match[1])
			line = n + 1
		}
		add("invalid_yaml", "", line, err.Error())
		return fail()
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		add("invalid_yaml", "", 0, "frontmatter must contain one YAML document")
		return fail()
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		add("invalid_metadata", "", 2, "frontmatter must be a YAML mapping")
		return fail()
	}
	root := document.Content[0]
	var checkDuplicates func(*yaml.Node)
	checkDuplicates = func(node *yaml.Node) {
		if node.Kind == yaml.MappingNode {
			keys := map[string]bool{}
			for i := 0; i < len(node.Content); i += 2 {
				key := node.Content[i]
				identity := key.Tag + ":" + key.Value
				if keys[identity] {
					add("duplicate_key", key.Value, key.Line+1, "duplicate YAML key")
				}
				keys[identity] = true
			}
		}
		for _, child := range node.Content {
			checkDuplicates(child)
		}
	}
	checkDuplicates(root)
	fields := map[string]*yaml.Node{}
	for i := 0; i < len(root.Content); i += 2 {
		fields[root.Content[i].Value] = root.Content[i+1]
	}
	for _, field := range []string{"name", "description"} {
		node, ok := fields[field]
		if !ok {
			add("missing_field", field, 2, "required non-empty string")
			continue
		}
		if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
			add("invalid_field", field, node.Line+1, "must be a string")
			continue
		}
		if field == "name" {
			skill.Name = node.Value
		} else {
			skill.Description = node.Value
		}
		if strings.TrimSpace(node.Value) == "" {
			add("empty_field", field, node.Line+1, "must not be empty")
		}
	}
	if len(problems) > 0 {
		return fail()
	}
	return skill, nil
}
