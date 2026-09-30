package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

type directoryEntry struct {
	Name      string  `json:"name"`
	Type      string  `json:"type"`
	SizeBytes *int64  `json:"size_bytes"`
	Target    *string `json:"target"`
}

type dirListData struct {
	Path       string           `json:"path"`
	Entries    []directoryEntry `json:"entries"`
	NextOffset *int             `json:"next_offset"`
}

func dirListHandler(ctx context.Context, arguments json.RawMessage, env environment) outcome {
	var args dirListArgs
	if err := decodeStrict(arguments, &args); err != nil {
		return outcome{Error: err}
	}
	offset, limit := 0, 200
	if args.Offset != nil {
		offset = *args.Offset
	}
	if args.Limit != nil {
		limit = *args.Limit
	}
	path, pathError := resolvePath(env.workspace, args.Path)
	if pathError != nil {
		return outcome{Error: pathError}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return outcome{Error: filesystemError(err, path)}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	if offset > len(entries) {
		offset = len(entries)
	}
	end := offset + limit
	if end > len(entries) {
		end = len(entries)
	}
	result := dirListData{Path: path, Entries: make([]directoryEntry, 0, end-offset)}
	for _, entry := range entries[offset:end] {
		if err := ctx.Err(); err != nil {
			return outcome{Error: &ToolError{Code: "cancelled", Message: err.Error()}}
		}
		info, err := entry.Info()
		if err != nil {
			return outcome{Error: filesystemError(err, filepath.Join(path, entry.Name()))}
		}
		item := directoryEntry{Name: entry.Name(), Type: fileType(info.Mode())}
		if info.Mode().IsRegular() {
			size := info.Size()
			item.SizeBytes = &size
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(filepath.Join(path, entry.Name()))
			if err != nil {
				return outcome{Error: filesystemError(err, filepath.Join(path, entry.Name()))}
			}
			item.Target = &target
		}
		result.Entries = append(result.Entries, item)
	}
	if end < len(entries) {
		result.NextOffset = &end
	}
	return outcome{Data: result}
}

type dirDeleteData struct {
	Path    string  `json:"path"`
	Deleted bool    `json:"deleted"`
	Type    *string `json:"type"`
}

func dirDeleteHandler(ctx context.Context, arguments json.RawMessage, env environment) outcome {
	var args dirDeleteArgs
	if err := decodeStrict(arguments, &args); err != nil {
		return outcome{Error: err}
	}
	path, pathError := resolvePath(env.workspace, args.Path)
	if pathError != nil {
		return outcome{Error: pathError}
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return outcome{Data: dirDeleteData{Path: path}}
	}
	if err != nil {
		return outcome{Error: filesystemError(err, path)}
	}
	kind := fileType(info.Mode())
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(path); err != nil {
			return outcome{Error: filesystemError(err, path)}
		}
		return outcome{Data: dirDeleteData{Path: path, Deleted: true, Type: &kind}}
	}
	if !*args.Recursive {
		if err := os.Remove(path); err != nil {
			if errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
				return outcome{Error: errorForPath("directory_not_empty", "directory is not empty", path)}
			}
			return outcome{Error: filesystemError(err, path)}
		}
		return outcome{Data: dirDeleteData{Path: path, Deleted: true, Type: &kind}}
	}
	changed := false
	if failure := deleteTree(ctx, path, &changed); failure != nil {
		code := "io_error"
		if changed {
			code = "partial_delete"
		}
		return outcome{Error: &ToolError{Code: code, Message: failure.err.Error(), Details: map[string]any{"path": path, "failed_path": failure.path, "changed": changed}}}
	}
	return outcome{Data: dirDeleteData{Path: path, Deleted: true, Type: &kind}}
}

type deleteFailure struct {
	path string
	err  error
}

func deleteTree(ctx context.Context, path string, changed *bool) *deleteFailure {
	if err := ctx.Err(); err != nil {
		return &deleteFailure{path: path, err: err}
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return &deleteFailure{path: path, err: err}
	}
	if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		entries, err := os.ReadDir(path)
		if err != nil {
			return &deleteFailure{path: path, err: err}
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			if failure := deleteTree(ctx, filepath.Join(path, entry.Name()), changed); failure != nil {
				return failure
			}
		}
	}
	if err := os.Remove(path); err != nil {
		return &deleteFailure{path: path, err: err}
	}
	*changed = true
	return nil
}

func fileType(mode fs.FileMode) string {
	switch {
	case mode&os.ModeSymlink != 0:
		return "symlink"
	case mode.IsRegular():
		return "file"
	case mode.IsDir():
		return "directory"
	default:
		return "other"
	}
}
