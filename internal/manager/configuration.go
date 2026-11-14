package manager

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ReanSn0w/horizon/internal/config"
	"gopkg.in/yaml.v3"
)

// The selected checkout, not the running bootstrap manager, owns core defaults.
func coreTemplate(source string) ([]byte, error) {
	path := filepath.Join(source, "config.example.yaml")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("selected source has no config.example.yaml: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("selected source config.example.yaml must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func prepareCoreConfig(home, stage, source string, fresh, agentSupervisor bool) error {
	template, err := coreTemplate(source)
	if err != nil {
		return err
	}
	if fresh {
		if err = atomicWrite(filepath.Join(stage, "config.yaml"), template, 0600); err != nil {
			return err
		}
	} else if err = copyPrivate(filepath.Join(home, "config.yaml"), filepath.Join(stage, "config.yaml")); err != nil {
		return err
	}
	// Explicit semantic migrations run before adding newly introduced defaults.
	if err = prepareSupervisorConfig(stage, agentSupervisor); err != nil {
		return err
	}
	if _, err = config.MigrateMemory(stage); err != nil {
		return fmt.Errorf("migrate memory configuration: %w", err)
	}
	modern, err := extensionTemplate(template)
	if err != nil {
		return err
	}
	if modern {
		if _, err = config.MigrateExtensions(stage); err != nil {
			return fmt.Errorf("migrate extensions: %w", err)
		}
	}
	if _, err = config.MergeCoreTemplate(stage, template); err != nil {
		return fmt.Errorf("update core configuration: %w", err)
	}
	return nil
}

func configurationChanged(home, stage string) (bool, error) {
	before, err := os.ReadFile(filepath.Join(home, "config.yaml"))
	if err != nil {
		return false, err
	}
	after, err := os.ReadFile(filepath.Join(stage, "config.yaml"))
	if err != nil {
		return false, err
	}
	return !bytes.Equal(before, after), nil
}

func extensionTemplate(data []byte) (bool, error) {
	var doc map[string]yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return false, err
	}
	_, ok := doc["extensions"]
	return ok, nil
}
