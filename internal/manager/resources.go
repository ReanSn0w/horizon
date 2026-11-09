package manager

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/ReanSn0w/horizon/internal/bootstrap"
	"github.com/ReanSn0w/horizon/internal/config"
)

// Initialize installs missing resources and merges installed plugin templates.
func Initialize(home string, out, diag io.Writer) error {
	result, err := bootstrap.Ensure(home)
	if err != nil {
		return err
	}
	if _, err := config.MigrateMemory(home); err != nil {
		return fmt.Errorf("migrate memory configuration: %w", err)
	}
	statuses, skipped, err := initializePluginConfig(home, nil)
	if err != nil {
		return fmt.Errorf("initialize plugin configuration: %w", err)
	}
	fmt.Fprintf(out, "Horizon home: %s\n", home)
	configPath := filepath.Join(home, "config.yaml")
	if result.ConfigCreated {
		fmt.Fprintf(out, "Отредактируйте %s: задайте providers.<имя>.url и key, models.<профиль>.provider и model; проверьте default_model, decision_model и compact_threshold.\n", configPath)
	} else {
		fmt.Fprintf(out, "Существующая конфигурация сохранена: %s.\n", configPath)
	}
	fmt.Fprintf(out, "При необходимости добавьте инструкции агента в %s.\n", filepath.Join(home, "AGENTS.md"))
	for _, name := range sortedKeys(statuses) {
		fmt.Fprintf(out, "Плагин %s: %s.\n", name, statuses[name])
	}
	for _, name := range skipped {
		fmt.Fprintf(diag, "horizon: настройки плагина %s пропущены (шаблон недоступен).\n", name)
	}
	return nil
}
