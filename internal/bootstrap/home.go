package bootstrap

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Result describes changes without writing to the CLI's output streams.
type Result struct {
	ConfigCreated bool
}

// Ensure creates missing initialization resources without replacing user files.
func Ensure(home string) (Result, error) {
	var result Result
	for _, dir := range []string{home, filepath.Join(home, "dialogs"), filepath.Join(home, "plugins"), filepath.Join(home, "skills", "skill-creator"), filepath.Join(home, "skills", "filesystem")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return result, fmt.Errorf("initialize directory %q: %w", dir, err)
		}
	}
	created, err := install(filepath.Join(home, "config.yaml"), configTemplate)
	if err != nil {
		return result, err
	}
	result.ConfigCreated = created
	if _, err := install(filepath.Join(home, "AGENTS.md"), nil); err != nil {
		return result, err
	}
	if _, err := install(filepath.Join(home, "skills", "skill-creator", "SKILL.md"), skillTemplate); err != nil {
		return result, err
	}
	_, err = install(filepath.Join(home, "skills", "filesystem", "SKILL.md"), filesystemSkillTemplate)
	return result, err
}

func existingFile(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Follow existing symlinks, but reject broken links and non-regular targets.
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("%q is not a regular file", path)
	}
	return true, nil
}

// install publishes a fully written file using a hard link: unlike Rename,
// Link cannot replace a file installed by another process in the meantime.
func install(path string, data []byte) (created bool, err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("initialize file %q: %w", path, err)
		}
	}()
	exists, err := existingFile(path)
	if err != nil || exists {
		return false, err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".horizon-init-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.Write(data); err != nil {
		return false, err
	}
	if err = file.Sync(); err != nil {
		return false, err
	}
	if err = file.Close(); err != nil {
		return false, err
	}
	if err = os.Link(file.Name(), path); errors.Is(err, os.ErrExist) {
		_, err = existingFile(path)
		return false, err
	} else if err != nil {
		return false, err
	}
	return true, nil
}
