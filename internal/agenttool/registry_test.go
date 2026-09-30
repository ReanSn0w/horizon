package agenttool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ReanSn0w/horizon/internal/instructions"
)

func TestDefinitionsAndArgumentValidation(t *testing.T) {
	want := []string{SkillRead, ShellExec}
	definitions := Definitions()
	if len(definitions) != len(want) {
		t.Fatalf("Definitions() count = %d", len(definitions))
	}
	for index, definition := range definitions {
		if definition.Name != want[index] || !definition.Strict || definition.Type != "function" || definition.Parameters["additionalProperties"] != false {
			t.Fatalf("definition %d = %+v", index, definition)
		}
	}
	for _, test := range []struct{ name, args string }{
		{SkillRead, `{"name":""}`},
		{SkillRead, `{"name":"review","extra":true}`},
		{ShellExec, `{"command":"x","timeout_ms":null,"max_output_chars":999}`},
		{ShellExec, `{"command":"","timeout_ms":null,"max_output_chars":null}`},
	} {
		if err := validateArguments(test.name, json.RawMessage(test.args)); err == nil || err.Code != "invalid_arguments" {
			t.Errorf("validateArguments(%s, %s) = %v", test.name, test.args, err)
		}
	}
	if err := validateArguments("file_create", json.RawMessage(`{"path":"x","content":"y"}`)); err == nil || err.Code != "unknown_tool" {
		t.Fatalf("removed tool accepted: %v", err)
	}
}

func TestSkillReadReturnsFullInstructionsAndBaseDirectory(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(home, "skills", "review")
	if err := os.MkdirAll(filepath.Join(base, "references"), 0700); err != nil {
		t.Fatal(err)
	}
	body := "Read every change.\nKeep this final newline.\n"
	contents := "---\nname: review\ndescription: Review code\n---\n" + body
	if err := os.WriteFile(filepath.Join(base, "SKILL.md"), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "references", "guide.md"), []byte("guide"), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := instructions.ScanSkills(home)
	if err != nil {
		t.Fatal(err)
	}
	result := skillReadHandler(context.Background(), json.RawMessage(`{"name":"review"}`), environment{skills: catalog})
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	data := result.Data.(skillReadData)
	if data.Content != body || data.BaseDir != base || data.Description != "Review code" {
		t.Fatalf("skill_read data = %+v", data)
	}
	missing := skillReadHandler(context.Background(), json.RawMessage(`{"name":"missing"}`), environment{skills: catalog})
	if missing.Error == nil || missing.Error.Code != "skill_not_found" {
		t.Fatalf("missing skill result = %+v", missing)
	}
}
