package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func workspace(ctx context.Context, home string, cfg settings, s store, c *chat, run runProcess) (*chat, error) {
	if err := os.MkdirAll(cfg.Workspace, 0700); err != nil {
		return nil, err
	}
	base, err := filepath.EvalSymlinks(cfg.Workspace)
	if err != nil {
		return nil, err
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return nil, err
	}
	path := c.Workspace
	if path == "" {
		path = filepath.Join(base, fmt.Sprintf("telegram_%d_%d", s.botID, c.Origin))
	}
	if err = os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil, errors.New("chat workspace is outside workspace_dir")
	}
	marker := filepath.Join(path, ".horizon-gateway.json")
	binding := struct{ Bot, Chat int64 }{s.botID, c.Origin}
	data, err := os.ReadFile(marker)
	if errors.Is(err, os.ErrNotExist) {
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}
		if len(entries) != 0 {
			return nil, errors.New("refusing to claim a non-empty unbound chat workspace")
		}
		data, _ = json.Marshal(binding)
		if err = atomicFile(marker, data, 0600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else {
		var got struct{ Bot, Chat int64 }
		if json.Unmarshal(data, &got) != nil || got != binding {
			return nil, errors.New("workspace belongs to a different bot or chat")
		}
	}
	if c.Session == "" {
		output, err := run(ctx, path, []string{"--home", home, "sessions", "create"}, "")
		if err != nil {
			return nil, err
		}
		id := strings.TrimSpace(output)
		if id == "" || strings.ContainsAny(id, "\r\n \t") {
			return nil, errors.New("invalid Horizon session ID")
		}
		if err = s.update(func(v *state) error {
			current, err := resolveChat(v, fmt.Sprint(c.ID))
			if err != nil {
				return err
			}
			current.Workspace = path
			current.Session = id
			return nil
		}); err != nil {
			return nil, err
		}
		c.Session = id
	}
	c.Workspace = path
	return c, nil
}
