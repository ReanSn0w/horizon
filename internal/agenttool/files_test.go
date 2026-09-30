package agenttool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ReanSn0w/horizon/internal/instructions"
)

func TestDefinitionsAndArgumentValidation(t *testing.T) {
	want := []string{FileCreate, FileRead, FileUpdate, DirList, DirDelete, SkillRead, ShellExec}
	definitions := Definitions()
	if len(definitions) != len(want) {
		t.Fatalf("Definitions() count = %d", len(definitions))
	}
	for index, definition := range definitions {
		if definition.Name != want[index] || !definition.Strict || definition.Type != "function" {
			t.Fatalf("definition %d = %+v", index, definition)
		}
		if definition.Parameters["additionalProperties"] != false {
			t.Fatalf("definition %s allows extra properties", definition.Name)
		}
	}
	tests := []struct {
		name string
		args string
	}{
		{FileRead, `{"path":"x","start_line":null}`},
		{FileRead, `{"path":"x","start_line":0,"line_count":1}`},
		{DirDelete, `{"path":"x","recursive":null}`},
		{ShellExec, `{"command":"x","timeout_ms":null,"max_output_chars":999}`},
		{FileCreate, `{"path":"x","content":"","extra":true}`},
	}
	for _, test := range tests {
		if err := validateArguments(test.name, json.RawMessage(test.args)); err == nil || err.Code != "invalid_arguments" {
			t.Errorf("validateArguments(%s, %s) = %v", test.name, test.args, err)
		}
	}
}

func TestFileCreateReadAndUpdate(t *testing.T) {
	workspace := t.TempDir()
	env := environment{workspace: workspace}
	created := fileCreateHandler(context.Background(), json.RawMessage(`{"path":"nested/example.txt","content":"one\r\ntwo\r\nthree"}`), env)
	if created.Error != nil {
		t.Fatal(created.Error)
	}
	createData := created.Data.(fileCreateData)
	if createData.BytesWritten != len("one\r\ntwo\r\nthree") || len(createData.CreatedDirectories) != 1 {
		t.Fatalf("file_create data = %+v", createData)
	}
	if repeated := fileCreateHandler(context.Background(), json.RawMessage(`{"path":"nested/example.txt","content":"overwrite"}`), env); repeated.Error == nil || repeated.Error.Code != "already_exists" {
		t.Fatalf("existing file result = %+v", repeated)
	}

	broken := filepath.Join(workspace, "broken")
	if err := os.Symlink(filepath.Join(workspace, "missing"), broken); err != nil {
		t.Fatal(err)
	}
	if result := fileCreateHandler(context.Background(), json.RawMessage(`{"path":"broken","content":"x"}`), env); result.Error == nil || result.Error.Code != "already_exists" {
		t.Fatalf("broken symlink result = %+v", result)
	}

	first := fileReadHandler(context.Background(), json.RawMessage(`{"path":"nested/example.txt","start_line":null,"line_count":1}`), env)
	if first.Error != nil {
		t.Fatal(first.Error)
	}
	firstData := first.Data.(fileReadData)
	if firstData.Content != "one\r\n" || firstData.EndLine == nil || *firstData.EndLine != 1 || firstData.NextLine == nil || *firstData.NextLine != 2 || firstData.EOF {
		t.Fatalf("first read = %+v", firstData)
	}
	second := fileReadHandler(context.Background(), json.RawMessage(`{"path":"nested/example.txt","start_line":2,"line_count":2}`), env)
	secondData := second.Data.(fileReadData)
	if secondData.Content != "two\r\nthree" || !secondData.EOF || secondData.NextLine != nil {
		t.Fatalf("second read = %+v", secondData)
	}
	beyond := fileReadHandler(context.Background(), json.RawMessage(`{"path":"nested/example.txt","start_line":20,"line_count":1}`), env).Data.(fileReadData)
	if beyond.Content != "" || beyond.EndLine != nil || beyond.NextLine != nil || !beyond.EOF {
		t.Fatalf("read beyond EOF = %+v", beyond)
	}

	path := filepath.Join(workspace, "nested", "example.txt")
	if err := os.Chmod(path, 0o751); err != nil {
		t.Fatal(err)
	}
	updated := fileUpdateHandler(context.Background(), json.RawMessage(`{"path":"nested/example.txt","old_text":"two","new_text":"TWO"}`), env)
	if updated.Error != nil || !updated.Data.(fileUpdateData).Changed {
		t.Fatalf("file_update result = %+v", updated)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o751 {
		t.Fatalf("updated permissions = %o", info.Mode().Perm())
	}
	same := fileUpdateHandler(context.Background(), json.RawMessage(`{"path":"nested/example.txt","old_text":"TWO","new_text":"TWO"}`), env)
	if same.Error != nil || same.Data.(fileUpdateData).Changed {
		t.Fatalf("same-fragment update = %+v", same)
	}
	missing := fileUpdateHandler(context.Background(), json.RawMessage(`{"path":"nested/example.txt","old_text":"absent","new_text":"x"}`), env)
	if missing.Error == nil || missing.Error.Code != "text_not_found" {
		t.Fatalf("missing update = %+v", missing)
	}
	if err := os.WriteFile(path, []byte("x x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ambiguous := fileUpdateHandler(context.Background(), json.RawMessage(`{"path":"nested/example.txt","old_text":"x","new_text":"y"}`), env)
	if ambiguous.Error == nil || ambiguous.Error.Code != "ambiguous_match" {
		t.Fatalf("ambiguous update = %+v", ambiguous)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "x x" {
		t.Fatalf("ambiguous update changed file to %q, %v", contents, err)
	}
	if err := os.WriteFile(path, []byte("anchor\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inserted := fileUpdateHandler(context.Background(), json.RawMessage(`{"path":"nested/example.txt","old_text":"anchor\n","new_text":"anchor\nadded\n"}`), env)
	if inserted.Error != nil {
		t.Fatal(inserted.Error)
	}
	removed := fileUpdateHandler(context.Background(), json.RawMessage(`{"path":"nested/example.txt","old_text":"added\n","new_text":""}`), env)
	if removed.Error != nil {
		t.Fatal(removed.Error)
	}
	contents, err = os.ReadFile(path)
	if err != nil || string(contents) != "anchor\n" {
		t.Fatalf("insert/delete result = %q, %v", contents, err)
	}
}

func TestFileReadLimitsEncodingAndLiteralPaths(t *testing.T) {
	workspace := t.TempDir()
	env := environment{workspace: workspace}
	longPath := filepath.Join(workspace, "long.txt")
	if err := os.WriteFile(longPath, []byte(strings.Repeat("a", maxReadBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	long := fileReadHandler(context.Background(), json.RawMessage(`{"path":"long.txt","start_line":1,"line_count":1}`), env)
	if long.Error == nil || long.Error.Code != "line_too_long" {
		t.Fatalf("long line result = %+v", long)
	}
	if err := os.WriteFile(filepath.Join(workspace, "binary"), []byte{0xff, 0xfe}, 0o600); err != nil {
		t.Fatal(err)
	}
	binary := fileReadHandler(context.Background(), json.RawMessage(`{"path":"binary","start_line":null,"line_count":null}`), env)
	if binary.Error == nil || binary.Error.Code != "unsupported_encoding" {
		t.Fatalf("binary result = %+v", binary)
	}
	literal := fileCreateHandler(context.Background(), json.RawMessage(`{"path":"~/literal.txt","content":"ok"}`), env)
	if literal.Error != nil {
		t.Fatal(literal.Error)
	}
	if _, err := os.Stat(filepath.Join(workspace, "~", "literal.txt")); err != nil {
		t.Fatalf("tilde path was expanded: %v", err)
	}
}

func TestDirectoryListAndDeleteDoNotFollowSymlinks(t *testing.T) {
	workspace := t.TempDir()
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "keep.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(workspace, "tree")
	if err := os.MkdirAll(filepath.Join(tree, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"z.txt", ".hidden", "a.txt"} {
		if err := os.WriteFile(filepath.Join(tree, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(external, filepath.Join(tree, "external-link")); err != nil {
		t.Fatal(err)
	}
	env := environment{workspace: workspace}
	listed := dirListHandler(context.Background(), json.RawMessage(`{"path":"tree","offset":0,"limit":2}`), env)
	if listed.Error != nil {
		t.Fatal(listed.Error)
	}
	page := listed.Data.(dirListData)
	if got := []string{page.Entries[0].Name, page.Entries[1].Name}; !reflect.DeepEqual(got, []string{".hidden", "a.txt"}) || page.NextOffset == nil || *page.NextOffset != 2 {
		t.Fatalf("first directory page = %+v", page)
	}
	all := dirListHandler(context.Background(), json.RawMessage(`{"path":"tree","offset":0,"limit":100}`), env).Data.(dirListData)
	foundLink := false
	for _, entry := range all.Entries {
		if entry.Name == "external-link" {
			foundLink = entry.Type == "symlink" && entry.Target != nil && *entry.Target == external
		}
	}
	if !foundLink {
		t.Fatalf("symlink metadata missing: %+v", all.Entries)
	}
	nonrecursive := dirDeleteHandler(context.Background(), json.RawMessage(`{"path":"tree","recursive":false}`), env)
	if nonrecursive.Error == nil || nonrecursive.Error.Code != "directory_not_empty" {
		t.Fatalf("nonrecursive delete = %+v", nonrecursive)
	}
	deleted := dirDeleteHandler(context.Background(), json.RawMessage(`{"path":"tree","recursive":true}`), env)
	if deleted.Error != nil || !deleted.Data.(dirDeleteData).Deleted {
		t.Fatalf("recursive delete = %+v", deleted)
	}
	if data, err := os.ReadFile(filepath.Join(external, "keep.txt")); err != nil || string(data) != "keep" {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
	absent := dirDeleteHandler(context.Background(), json.RawMessage(`{"path":"tree","recursive":true}`), env)
	if absent.Error != nil || absent.Data.(dirDeleteData).Deleted || absent.Data.(dirDeleteData).Type != nil {
		t.Fatalf("absent delete = %+v", absent)
	}
}

func TestSkillReadReturnsFullInstructionsAndBaseDirectory(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(home, "skills", "review")
	if err := os.MkdirAll(filepath.Join(base, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "Read every change.\nKeep this final newline.\n"
	contents := "---\nname: review\ndescription: Review code\n---\n" + body
	if err := os.WriteFile(filepath.Join(base, "SKILL.md"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "references", "guide.md"), []byte("guide"), 0o600); err != nil {
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
	if _, err := os.Stat(filepath.Join(data.BaseDir, "references", "guide.md")); err != nil {
		t.Fatalf("base_dir resource lookup: %v", err)
	}
	missing := skillReadHandler(context.Background(), json.RawMessage(`{"name":"missing"}`), environment{skills: catalog})
	if missing.Error == nil || missing.Error.Code != "skill_not_found" {
		t.Fatalf("missing skill result = %+v", missing)
	}
}
