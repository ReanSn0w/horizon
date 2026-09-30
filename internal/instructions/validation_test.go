package instructions

import (
	"errors"
	"testing"
)

func TestSkillValidation(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		count      int
	}{
		{"lf", "---\nname: n\ndescription: d\ncustom: true\n---\n", 0},
		{"crlf", "---\r\nname: n\r\ndescription: d\r\n---\r\nbody\r\n", 0},
		{"missing", "body", 1},
		{"unclosed", "---\nname: n", 1},
		{"utf8", string([]byte{255}), 1},
		{"mapping", "---\n- n\n---", 1},
		{"syntax", "---\nname: [\n---", 1},
		{"empty", "---\n---", 1},
		{"fields", "---\nname: 12\ndescription: ' '\n---", 2},
		{"absent", "---\nother: true\n---", 2},
		{"duplicate", "---\nname: n\nname: x\ndescription: d\n---", 1},
		{"nested-duplicate", "---\nname: n\ndescription: d\nextra: {a: 1, a: 2}\n---", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseSkill([]byte(tc.data), "/skills/n")
			if tc.count == 0 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var validation *SkillValidationError
			if !errors.As(err, &validation) || len(validation.Problems) != tc.count {
				t.Fatalf("err=%v expected %d problems", err, tc.count)
			}
		})
	}
}

func TestValidationKeepsPartialMetadataAndFileLines(t *testing.T) {
	skill, err := parseSkill([]byte("---\nname: good\ndescription: 42\n---\nbody"), "/skills/good")
	var validation *SkillValidationError
	if skill.Name != "good" || !errors.As(err, &validation) || validation.Problems[0].Line != 3 {
		t.Fatalf("skill=%+v err=%v", skill, err)
	}
}
