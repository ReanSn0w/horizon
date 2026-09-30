package agenttool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"unicode/utf8"
)

const maxReadBytes = 64 * 1024

type fileCreateData struct {
	Path               string   `json:"path"`
	BytesWritten       int      `json:"bytes_written"`
	CreatedDirectories []string `json:"created_directories"`
}

func fileCreateHandler(ctx context.Context, arguments json.RawMessage, env environment) outcome {
	var args fileCreateArgs
	if err := decodeStrict(arguments, &args); err != nil {
		return outcome{Error: err}
	}
	path, pathError := resolvePath(env.workspace, args.Path)
	if pathError != nil {
		return outcome{Error: pathError}
	}
	if _, err := os.Lstat(path); err == nil {
		return outcome{Error: errorForPath("already_exists", "path already exists", path)}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return outcome{Error: filesystemError(err, path)}
	}
	created, err := createParents(ctx, filepath.Dir(path))
	if err != nil {
		toolError := filesystemError(err, path)
		toolError.Details["created_directories"] = created
		return outcome{Error: toolError}
	}
	if err := ctx.Err(); err != nil {
		return outcome{Error: &ToolError{Code: "cancelled", Message: err.Error(), Details: map[string]any{"created_directories": created}}}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o666)
	if err != nil {
		toolError := filesystemError(err, path)
		toolError.Details["created_directories"] = created
		return outcome{Error: toolError}
	}
	written, writeErr := io.WriteString(file, args.Content)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		remaining := true
		if removeErr := os.Remove(path); removeErr == nil || errors.Is(removeErr, fs.ErrNotExist) {
			remaining = false
		}
		cause := writeErr
		if cause == nil {
			cause = closeErr
		}
		return outcome{Error: &ToolError{Code: "io_error", Message: fmt.Sprintf("write file %q: %v", path, cause), Details: map[string]any{"path": path, "created_directories": created, "partial_file_remaining": remaining}}}
	}
	return outcome{Data: fileCreateData{Path: path, BytesWritten: written, CreatedDirectories: created}}
}

func createParents(ctx context.Context, dir string) ([]string, error) {
	var missing []string
	current := dir
	for {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return nil, fmt.Errorf("parent path %q is not a directory", current)
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	created := make([]string, 0, len(missing))
	for index := len(missing) - 1; index >= 0; index-- {
		if err := ctx.Err(); err != nil {
			return created, err
		}
		if err := os.Mkdir(missing[index], 0o777); err != nil {
			return created, err
		}
		created = append(created, missing[index])
	}
	return created, nil
}

type fileReadData struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	StartLine int    `json:"start_line"`
	EndLine   *int   `json:"end_line"`
	NextLine  *int   `json:"next_line"`
	EOF       bool   `json:"eof"`
}

func fileReadHandler(ctx context.Context, arguments json.RawMessage, env environment) outcome {
	var args fileReadArgs
	if err := decodeStrict(arguments, &args); err != nil {
		return outcome{Error: err}
	}
	start, count := 1, 200
	if args.StartLine != nil {
		start = *args.StartLine
	}
	if args.LineCount != nil {
		count = *args.LineCount
	}
	path, pathError := resolvePath(env.workspace, args.Path)
	if pathError != nil {
		return outcome{Error: pathError}
	}
	file, err := os.Open(path)
	if err != nil {
		return outcome{Error: filesystemError(err, path)}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return outcome{Error: filesystemError(err, path)}
	}
	if !info.Mode().IsRegular() {
		return outcome{Error: errorForPath("wrong_type", "path is not a regular file", path)}
	}

	reader := bufio.NewReader(file)
	lineNumber := 0
	selected := 0
	var content bytes.Buffer
	result := fileReadData{Path: path, StartLine: start}
	for {
		if err := ctx.Err(); err != nil {
			return outcome{Error: &ToolError{Code: "cancelled", Message: err.Error()}}
		}
		line, readErr := reader.ReadBytes('\n')
		if len(line) == 0 && errors.Is(readErr, io.EOF) {
			result.EOF = true
			break
		}
		lineNumber++
		if !utf8.Valid(line) {
			return outcome{Error: &ToolError{Code: "unsupported_encoding", Message: fmt.Sprintf("file %q is not valid UTF-8 at line %d", path, lineNumber), Details: map[string]any{"path": path, "line": lineNumber}}}
		}
		if lineNumber >= start {
			if len(line) > maxReadBytes && content.Len() == 0 {
				return outcome{Error: &ToolError{Code: "line_too_long", Message: fmt.Sprintf("line %d exceeds the 64 KiB read limit", lineNumber), Details: map[string]any{"path": path, "line": lineNumber, "size_bytes": len(line)}}}
			}
			if content.Len()+len(line) > maxReadBytes {
				next := lineNumber
				result.NextLine = &next
				result.EOF = false
				break
			}
			_, _ = content.Write(line)
			end := lineNumber
			result.EndLine = &end
			selected++
		}
		if errors.Is(readErr, io.EOF) {
			result.EOF = true
			break
		}
		if readErr != nil {
			return outcome{Error: filesystemError(readErr, path)}
		}
		if selected == count {
			if _, err := reader.Peek(1); errors.Is(err, io.EOF) {
				result.EOF = true
			} else if err != nil {
				return outcome{Error: filesystemError(err, path)}
			} else {
				next := lineNumber + 1
				result.NextLine = &next
			}
			break
		}
	}
	result.Content = content.String()
	return outcome{Data: result}
}

type fileUpdateData struct {
	Path         string `json:"path"`
	Replacements int    `json:"replacements"`
	Changed      bool   `json:"changed"`
	BytesBefore  int    `json:"bytes_before"`
	BytesAfter   int    `json:"bytes_after"`
}

func fileUpdateHandler(ctx context.Context, arguments json.RawMessage, env environment) outcome {
	var args fileUpdateArgs
	if err := decodeStrict(arguments, &args); err != nil {
		return outcome{Error: err}
	}
	path, pathError := resolvePath(env.workspace, args.Path)
	if pathError != nil {
		return outcome{Error: pathError}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return outcome{Error: filesystemError(err, path)}
	}
	if !info.Mode().IsRegular() {
		return outcome{Error: errorForPath("wrong_type", "path is not a regular file", path)}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return outcome{Error: filesystemError(err, path)}
	}
	if !utf8.Valid(data) {
		return outcome{Error: errorForPath("unsupported_encoding", "file is not valid UTF-8", path)}
	}
	matches := bytes.Count(data, []byte(args.OldText))
	if matches == 0 {
		return outcome{Error: errorForPath("text_not_found", "old_text was not found", path)}
	}
	if matches > 1 {
		toolError := errorForPath("ambiguous_match", "old_text occurs more than once", path)
		toolError.Details["matches"] = matches
		return outcome{Error: toolError}
	}
	result := fileUpdateData{Path: path, Replacements: 1, BytesBefore: len(data)}
	if args.OldText == args.NewText {
		result.BytesAfter = len(data)
		return outcome{Data: result}
	}
	updated := bytes.Replace(data, []byte(args.OldText), []byte(args.NewText), 1)
	if err := ctx.Err(); err != nil {
		return outcome{Error: &ToolError{Code: "cancelled", Message: err.Error()}}
	}
	if err := replaceFile(path, updated, info.Mode().Perm()); err != nil {
		return outcome{Error: filesystemError(err, path)}
	}
	result.Changed = true
	result.BytesAfter = len(updated)
	return outcome{Data: result}
}

func replaceFile(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, "."+filepath.Base(path)+".update-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	keep = true
	return syncParent(dir)
}

func syncParent(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func filesystemError(err error, path string) *ToolError {
	code := "io_error"
	switch {
	case errors.Is(err, fs.ErrNotExist):
		code = "not_found"
	case errors.Is(err, fs.ErrPermission):
		code = "permission_denied"
	case errors.Is(err, fs.ErrExist):
		code = "already_exists"
	}
	return errorForPath(code, err.Error(), path)
}
