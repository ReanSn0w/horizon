package telegram

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Workspace preserves the original per-chat path across supergroup migrations.
func Workspace(base string, botID int64, chat Conversation) (string, error) {
	if chat.Workspace == "" {
		if err := os.MkdirAll(base, 0700); err != nil {
			return "", err
		}
	}
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	path := chat.Workspace
	if path == "" {
		path = filepath.Join(base, fmt.Sprintf("telegram_%d_%d", botID, chat.Origin))
	}
	if !inside(base, path) {
		return "", errors.New("Telegram chat workspace is outside workspace_dir")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if errors.Is(err, os.ErrNotExist) && chat.Workspace == "" {
		if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		resolved, err = filepath.EvalSymlinks(path)
	}
	if err != nil || !inside(base, resolved) {
		return "", errors.New("Telegram chat workspace is unavailable or outside workspace_dir")
	}
	marker := filepath.Join(resolved, ".horizon-gateway.json")
	want := struct{ Bot, Chat int64 }{botID, chat.Origin}
	data, err := os.ReadFile(marker)
	if errors.Is(err, os.ErrNotExist) && chat.Workspace == "" {
		entries, err := os.ReadDir(resolved)
		if err != nil {
			return "", err
		}
		if len(entries) != 0 {
			return "", errors.New("refusing to claim a nonempty unbound Telegram workspace")
		}
		data, _ := json.Marshal(want)
		return resolved, WritePrivate(marker, data, 0600)
	}
	var got struct{ Bot, Chat int64 }
	if err != nil || json.Unmarshal(data, &got) != nil || got != want {
		return "", errors.New("Telegram workspace is not bound to this bot and chat")
	}
	return resolved, nil
}

func inside(base, path string) bool {
	rel, err := filepath.Rel(base, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func PollLock(home string) (string, error) {
	dir := filepath.Join(home, "gateway")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "process.lock"), nil
}

func BindIdentity(home string, botID int64) error {
	path := filepath.Join(home, "gateway", "identity")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return WritePrivate(path, []byte(fmt.Sprint(botID)), 0600)
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(data)) != fmt.Sprint(botID) {
		return errors.New("this Horizon home belongs to another Telegram bot")
	}
	return nil
}
