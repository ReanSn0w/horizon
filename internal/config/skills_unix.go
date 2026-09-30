//go:build unix

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"gopkg.in/yaml.v3"
)

// UpdateDisabledSkills serializes Horizon writers on the resolved config target.
// beforeEnable runs under that lock with the proposed disabled IDs, before a write.
func UpdateDisabledSkills(home, id string, disable bool, beforeEnable func([]string) error) (bool, error) {
	if !validSkillID(id) {
		return false, fmt.Errorf("invalid skill ID %q", id)
	}
	path, err := filepath.EvalSymlinks(filepath.Join(home, "config.yaml"))
	if err != nil {
		return false, err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("configuration is not a regular file")
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return false, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return false, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	raw, document, err := readConfig(path)
	if err != nil {
		return false, err
	}
	ids := []string(raw.DisabledSkills)
	present := slices.Contains(ids, id)
	if present == disable {
		return false, nil
	}
	if disable {
		ids = append(ids, id)
	} else {
		ids = slices.DeleteFunc(ids, func(value string) bool { return value == id })
	}
	if !disable && beforeEnable != nil {
		if err := beforeEnable(append([]string(nil), ids...)); err != nil {
			return false, err
		}
	}
	root := document.Content[0]
	var list *yaml.Node
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "disabled_skills" {
			list = root.Content[i+1]
			break
		}
	}
	if list == nil {
		list = &yaml.Node{}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "disabled_skills"}, list)
	}
	// Retain this node's comments and anchor while replacing only its value.
	list.Kind = yaml.SequenceNode
	list.Tag = "!!seq"
	list.Value = ""
	list.Alias = nil
	list.Content = nil
	for _, id := range ids {
		list.Content = append(list.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: id})
	}
	data, err := yaml.Marshal(document)
	if err != nil {
		return false, err
	}
	// Re-read mode under the lock: a prior Horizon writer may have replaced the file.
	info, err = os.Stat(path)
	if err != nil {
		return false, err
	}
	if err := writeConfigAtomic(path, data, info.Mode().Perm()); err != nil {
		return false, err
	}
	return true, nil
}

func writeConfigAtomic(path string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".horizon-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
		return err
	}
	return nil
}
