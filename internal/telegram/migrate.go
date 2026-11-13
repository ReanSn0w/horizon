package telegram

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/daemon"
	"github.com/ReanSn0w/horizon/internal/maintenance"
	"github.com/ReanSn0w/horizon/internal/plugins"
	"gopkg.in/yaml.v3"
)

type MigrationReport struct {
	State         string
	Backup        string
	Chats         int
	Queued        int
	Unknown       int
	Archived      int
	LegacyService bool
}

type migrationManifest struct {
	Version int
	State   string
	BotID   int64
	Before  map[string]string
	After   map[string]string
	Report  MigrationReport
}

func migrationDir(home string) string { return filepath.Join(home, "gateway", "telegram-migration-v2") }

// MarkUse seals the rollback window before polling or attempting an outbound
// delivery. It does not contact Telegram and is a no-op for fresh installations.
func MarkUse(home string) error {
	if _, err := os.Lstat(daemon.MigrationBarrier(home)); !errors.Is(err, os.ErrNotExist) {
		return errors.New("finish the offline Telegram migration before polling or sending")
	}
	path := filepath.Join(migrationDir(home), "manifest.json")
	var manifest migrationManifest
	if err := loadJSON(path, &manifest); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil || manifest.State != "complete" {
		return errors.New("Telegram migration has not completed")
	}
	active := filepath.Join(migrationDir(home), "activated")
	if _, err := os.Lstat(active); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return WritePrivate(active, []byte("Telegram polling or delivery was enabled\n"), 0600)
}

func Migrate(ctx context.Context, home, action string) (MigrationReport, error) {
	if action != "--check" && action != "--apply" && action != "--rollback" {
		return MigrationReport{}, errors.New("usage: horizon telegram migrate --check|--apply|--rollback")
	}
	home, err := filepath.EvalSymlinks(home)
	if err != nil {
		return MigrationReport{}, err
	}
	unlock, err := lockMigration(home, action == "--check")
	if err != nil {
		return MigrationReport{}, err
	}
	defer unlock()
	backup := migrationDir(home)
	manifestPath := filepath.Join(backup, "manifest.json")
	var manifest migrationManifest
	err = loadJSON(manifestPath, &manifest)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return MigrationReport{}, err
	}
	known := err == nil
	if known && (manifest.Version != 1 || manifest.BotID <= 0 || manifest.Before == nil ||
		manifest.State != "prepared" && manifest.State != "complete" && manifest.State != "rolled_back" && manifest.State != "rolling_back") {
		return MigrationReport{}, errors.New("invalid Telegram migration manifest")
	}
	if action == "--rollback" {
		if !known || manifest.State != "complete" && manifest.State != "rolling_back" {
			return MigrationReport{}, errors.New("there is no completed Telegram migration to roll back")
		}
		return rollbackMigration(home, backup, manifest)
	}
	if known && manifest.State == "rolling_back" {
		if action == "--check" {
			manifest.Report.State = "rollback must be resumed"
			return manifest.Report, nil
		}
		return MigrationReport{}, errors.New("finish Telegram migration rollback with --rollback")
	}
	if err := installedForMigration(ctx, home); err != nil {
		return MigrationReport{}, err
	}
	if known && manifest.State == "complete" {
		if action == "--apply" {
			if err := removeBarrier(home); err != nil {
				return MigrationReport{}, err
			}
		}
		return manifest.Report, nil
	}
	configPath, err := filepath.EvalSymlinks(filepath.Join(home, "config.yaml"))
	if err != nil {
		return MigrationReport{}, err
	}
	oldConfig := configPath
	if known {
		oldConfig = filepath.Join(backup, "config.yaml")
	}
	legacy, key, err := readLegacyConfig(oldConfig, home)
	if err != nil {
		return MigrationReport{}, err
	}
	cfg, err := config.Load(home)
	if err != nil {
		return MigrationReport{}, errors.New("invalid Horizon configuration")
	}
	if _, _, err := cfg.SelectModel(""); err != nil {
		return MigrationReport{}, errors.New("configure the Horizon model before migration")
	}
	if legacy.PrivateAccess != "full" || legacy.GroupAccess != "full" {
		if err := cfg.RequireDecision(); err != nil {
			return MigrationReport{}, err
		}
	}
	settings, err := adapterSection(legacy.adapter(), home)
	if err != nil {
		return MigrationReport{}, err
	}
	if _, exists := cfg.Plugins["agent-telegram"]; exists {
		current, err := ReadAdapter(cfg, home)
		if err != nil || !reflect.DeepEqual(current, DefaultAdapter(home)) && !reflect.DeepEqual(current, settings) {
			return MigrationReport{}, errors.New("existing plugins.agent-telegram settings conflict with legacy configuration")
		}
	}
	if _, err := daemon.LoadSettings(home); err != nil {
		return MigrationReport{}, err
	}
	bot := legacy.Telegram.BotID()
	if err := CheckIdentity(home, bot); err != nil {
		return MigrationReport{}, err
	}
	paths := migrationPaths(home, configPath, bot)
	statePath := paths["legacy-state.yaml"]
	if known {
		if manifest.BotID != bot {
			return MigrationReport{}, errors.New("legacy Telegram bot no longer matches migration manifest")
		}
		if err := verifyBackups(backup, manifest.Before); err != nil {
			return MigrationReport{}, err
		}
		statePath = filepath.Join(backup, "legacy-state.yaml")
	} else {
		for _, name := range []string{"chats.json", "outbox"} {
			path := filepath.Join(NewStore(home, bot).dir, name)
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				return MigrationReport{}, errors.New("Telegram v2 state already exists; migration cannot overwrite it")
			}
		}
	}
	gateway, err := readLegacyGateway(statePath, bot)
	if err != nil {
		return MigrationReport{}, err
	}
	prepared, err := prepareLegacy(home, gateway, legacy.Telegram, settings, false)
	if err != nil {
		return MigrationReport{}, err
	}
	report := prepared.report
	report.Backup = backup
	report.State = "ready"
	if known && manifest.State == "prepared" {
		report.State = "resume required"
	}
	if _, err := os.Stat(filepath.Join(home, "gateway", "service.yaml")); err == nil {
		report.LegacyService = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return MigrationReport{}, err
	}
	if action == "--check" {
		return report, nil
	}

	if err := os.MkdirAll(backup, 0700); err != nil {
		return MigrationReport{}, err
	}
	if info, err := os.Lstat(backup); err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return MigrationReport{}, errors.New("migration backup must be a private directory")
	}
	if !known {
		manifest = migrationManifest{Version: 1, State: "prepared", BotID: bot, Before: map[string]string{}}
		for name, path := range paths {
			digest, err := backupFile(path, filepath.Join(backup, name))
			if err != nil {
				return MigrationReport{}, err
			}
			manifest.Before[name] = digest
		}
	} else if manifest.State == "rolled_back" {
		if err := checkFiles(paths, manifest.Before); err != nil {
			return MigrationReport{}, err
		}
		manifest.State = "prepared"
	}
	if err := WritePrivate(daemon.MigrationBarrier(home), []byte("Telegram offline migration\n"), 0600); err != nil {
		return MigrationReport{}, err
	}
	if err := saveJSON(manifestPath, manifest); err != nil {
		return MigrationReport{}, err
	}
	data, err := prepareLegacy(home, gateway, legacy.Telegram, settings, true)
	if err != nil {
		return MigrationReport{}, err
	}
	if err := NewStore(home, bot).Update(func(registry *Registry) error {
		*registry = data.registry
		return nil
	}); err != nil {
		return MigrationReport{}, err
	}
	if err := convertLegacyConfig(ctx, home, configPath, manifest.Before["config.yaml"], key, legacy.Telegram, settings, legacy.Parallel); err != nil {
		return MigrationReport{}, err
	}
	daemonSettings, err := daemon.LoadSettings(home)
	if err != nil {
		return MigrationReport{}, err
	}
	if err := daemon.ImportDialogs(home, "telegram", daemonSettings, data.dialogs); err != nil {
		return MigrationReport{}, err
	}
	manifest.After = map[string]string{}
	for name, path := range paths {
		digest, err := fileDigest(path)
		if err != nil {
			return MigrationReport{}, err
		}
		manifest.After[name] = digest
	}
	manifest.State = "complete"
	report.State = "complete"
	manifest.Report = report
	if err := saveJSON(manifestPath, manifest); err != nil {
		return MigrationReport{}, err
	}
	if err := removeBarrier(home); err != nil {
		return MigrationReport{}, err
	}
	return report, nil
}

func lockMigration(home string, check bool) (func(), error) {
	run := filepath.Join(home, "run")
	if err := os.MkdirAll(run, 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(run); err != nil || !info.IsDir() {
		return nil, errors.New("Horizon run path must be a real directory")
	}
	daemonUnlock, err := maintenance.Lock(filepath.Join(run, "daemon.lock"), true)
	if err != nil {
		return nil, errors.New("stop horizon agent before Telegram migration")
	}
	pollPath, err := PollLock(home)
	if err != nil {
		daemonUnlock()
		return nil, err
	}
	pollUnlock, err := maintenance.Lock(pollPath, true)
	if err != nil {
		daemonUnlock()
		return nil, errors.New("stop the old Telegram polling service before migration")
	}
	release := func() { pollUnlock(); daemonUnlock() }
	dir := filepath.Join(home, ".manager")
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) && check {
		return release, nil
	}
	if !check {
		if err := os.MkdirAll(dir, 0700); err != nil {
			release()
			return nil, err
		}
	}
	managedUnlock, err := maintenance.Acquire(home, true)
	if err != nil {
		release()
		return nil, err
	}
	return func() { managedUnlock(); release() }, nil
}

func installedForMigration(ctx context.Context, home string) error {
	for _, name := range []string{"horizon", "horizon-agent", "horizon-telegram", "agent-telegram"} {
		path := filepath.Join(home, "bin", name)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
			return fmt.Errorf("install %s before Telegram migration", name)
		}
	}
	extension := plugins.Inspect(home, "telegram", nil)
	daemonCLI := plugins.Inspect(home, "agent", nil)
	adapter, err := daemon.AdapterInspect(ctx, home, "telegram")
	if extension.Err != nil || extension.Metadata.AgentProtocolVersion != plugins.ChannelProtocolVersion ||
		daemonCLI.Err != nil || err != nil || !slices.Contains(adapter.Capabilities, daemon.ActiveInputCapability) {
		return errors.New("install compatible agent, telegram and agent:telegram before migration")
	}
	return nil
}

func migrationPaths(home, configPath string, bot int64) map[string]string {
	return map[string]string{
		"config.yaml":         configPath,
		"legacy-state.yaml":   legacyStatePath(home, bot),
		"daemon-state.json":   filepath.Join(home, "daemon", "state.json"),
		"registry.json":       filepath.Join(NewStore(home, bot).dir, "chats.json"),
		"legacy-identity":     filepath.Join(home, "gateway", "identity"),
		"legacy-service.yaml": filepath.Join(home, "gateway", "service.yaml"),
	}
}

func backupFile(source, destination string) (string, error) {
	data, err := os.ReadFile(source)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("migration backup source must be a regular file")
	}
	mode := info.Mode().Perm() & 0600
	if mode == 0 {
		mode = 0600
	}
	if err := WritePrivate(destination, data, mode); err != nil {
		return "", err
	}
	return digest(data), nil
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func fileDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return digest(data), nil
}

func checkFiles(paths, expected map[string]string) error {
	for name, path := range paths {
		current, err := fileDigest(path)
		if err != nil {
			return err
		}
		if current != expected[name] {
			return fmt.Errorf("%s changed since migration; inspect the state and backups manually", name)
		}
	}
	return nil
}

func verifyBackups(backup string, expected map[string]string) error {
	paths := map[string]string{}
	for name := range expected {
		if filepath.Base(name) != name {
			return errors.New("invalid Telegram backup entry")
		}
		paths[name] = filepath.Join(backup, name)
	}
	return checkFiles(paths, expected)
}

func removeBarrier(home string) error {
	if err := os.Remove(daemon.MigrationBarrier(home)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func rollbackMigration(home, backup string, manifest migrationManifest) (MigrationReport, error) {
	if _, err := os.Lstat(filepath.Join(backup, "activated")); !errors.Is(err, os.ErrNotExist) {
		return MigrationReport{}, errors.New("automatic rollback is disabled after Telegram polling or delivery has started")
	}
	configPath, err := filepath.EvalSymlinks(filepath.Join(home, "config.yaml"))
	if err != nil {
		return MigrationReport{}, err
	}
	paths := migrationPaths(home, configPath, manifest.BotID)
	if manifest.State == "complete" {
		if err := checkFiles(paths, manifest.After); err != nil {
			return MigrationReport{}, err
		}
	} else {
		for name, path := range paths {
			current, err := fileDigest(path)
			if err != nil {
				return MigrationReport{}, err
			}
			if current != manifest.After[name] && current != manifest.Before[name] {
				return MigrationReport{}, fmt.Errorf("%s changed during rollback", name)
			}
		}
	}
	if err := verifyBackups(backup, manifest.Before); err != nil {
		return MigrationReport{}, err
	}
	if err := WritePrivate(daemon.MigrationBarrier(home), []byte("Telegram offline rollback\n"), 0600); err != nil {
		return MigrationReport{}, err
	}
	manifest.State = "rolling_back"
	if err := saveJSON(filepath.Join(backup, "manifest.json"), manifest); err != nil {
		return MigrationReport{}, err
	}
	for _, name := range []string{"daemon-state.json", "registry.json", "config.yaml"} {
		if manifest.Before[name] == "" {
			if err := os.Remove(paths[name]); err != nil && !errors.Is(err, os.ErrNotExist) {
				return MigrationReport{}, err
			}
			continue
		}
		if _, err := backupFile(filepath.Join(backup, name), paths[name]); err != nil {
			return MigrationReport{}, err
		}
	}
	manifest.State = "rolled_back"
	manifest.Report.State = "rolled back"
	if err := saveJSON(filepath.Join(backup, "manifest.json"), manifest); err != nil {
		return MigrationReport{}, err
	}
	if err := removeBarrier(home); err != nil {
		return MigrationReport{}, err
	}
	return manifest.Report, nil
}

func convertLegacyConfig(ctx context.Context, home, path, oldHash, key string, bot BotSettings, settings AdapterSettings, parallel int) error {
	_, err := config.UpdateDocumentContext(ctx, home, func(doc *yaml.Node) (bool, error) {
		root := doc.Content[0]
		p := yamlKey(root, "plugins")
		if yamlKey(yamlKey(p, key), "telegram") == nil {
			existing := yamlKey(p, "telegram")
			var current BotSettings
			adapter := yamlKey(p, "agent-telegram")
			if existing != nil && adapter != nil && decodeSection(*existing, &current) == nil && current == bot {
				var currentSettings AdapterSettings
				if decodeSection(*adapter, &currentSettings) == nil && reflect.DeepEqual(currentSettings, settings) &&
					hasYAML(root, "agent_plugins", "telegram") && hasYAML(root, "agent_communication", "telegram") &&
					hasYAML(yamlKey(p, "agent"), "adapters", "telegram") {
					return false, nil
				}
			}
			return false, errors.New("legacy Telegram settings changed during migration")
		}
		found, err := fileDigest(path)
		if err != nil || found != oldHash {
			return false, errors.New("legacy configuration changed after backup")
		}
		botNode, adapterNode := &yaml.Node{}, &yaml.Node{}
		if err := botNode.Encode(bot); err != nil {
			return false, err
		}
		if err := adapterNode.Encode(settings); err != nil {
			return false, err
		}
		if key == "gateway" {
			removeYAML(p, key)
		}
		setYAML(p, "telegram", botNode)
		setYAML(p, "agent-telegram", adapterNode)
		if err := appendYAML(root, "agent_plugins", "telegram"); err != nil {
			return false, err
		}
		if err := appendYAML(root, "agent_communication", "telegram"); err != nil {
			return false, err
		}
		d := yamlKey(p, "agent")
		if legacy := yamlKey(p, "daemon"); legacy != nil {
			if d != nil {
				return false, errors.New("both plugins.agent and plugins.daemon exist")
			}
			d = legacy
			removeYAML(p, "daemon")
			setYAML(p, "agent", d)
		}
		if d == nil {
			d = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			setYAML(p, "agent", d)
		}
		if err := appendYAML(d, "adapters", "telegram"); err != nil {
			return false, err
		}
		if yamlKey(d, "max_active_adapter_dialogs") == nil {
			value := &yaml.Node{}
			if err := value.Encode(parallel); err != nil {
				return false, err
			}
			setYAML(d, "max_active_adapter_dialogs", value)
		}
		return true, nil
	})
	return err
}

func removeYAML(mapping *yaml.Node, key string) {
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content = slices.Delete(mapping.Content, i, i+2)
			return
		}
	}
}

func setYAML(mapping *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1] = value
			return
		}
	}
	if len(mapping.Content) == 0 {
		mapping.Style &^= yaml.FlowStyle
	}
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

func hasYAML(mapping *yaml.Node, key, value string) bool {
	sequence := yamlKey(mapping, key)
	return sequence != nil && sequence.Kind == yaml.SequenceNode &&
		slices.ContainsFunc(sequence.Content, func(item *yaml.Node) bool { return item.Value == value })
}

func appendYAML(mapping *yaml.Node, key, value string) error {
	if mapping.Kind != yaml.MappingNode {
		return errors.New("expected mapping in migration configuration")
	}
	sequence := yamlKey(mapping, key)
	if sequence == nil {
		sequence = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		setYAML(mapping, key, sequence)
	}
	if sequence.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s must be a sequence", key)
	}
	for _, item := range sequence.Content {
		if item.Value == value {
			return nil
		}
	}
	sequence.Content = append(sequence.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
	return nil
}

func encodeLegacyInput(chatID int64, message Entry, recent []Entry) (string, error) {
	value := struct {
		ChatID  int64   `json:"chat_id"`
		Message Entry   `json:"message"`
		Recent  []Entry `json:"recent,omitempty"`
	}{chatID, message, recent}
	for {
		raw, err := json.Marshal(value)
		if err != nil || len(raw) <= daemon.MaxInputBytes {
			return string(raw), err
		}
		if len(value.Recent) == 0 {
			return "", errors.New("legacy Telegram input exceeds daemon size limit")
		}
		value.Recent = value.Recent[1:]
	}
}
