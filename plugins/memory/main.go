package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/plugins"
	"github.com/ReanSn0w/horizon/internal/session"
	flags "github.com/umputun/go-flags"
)

type memoryOptions struct {
	Scope     string `long:"scope" description:"Memory scope"`
	Workspace string `long:"workspace" description:"Workspace directory"`
}

type addOptions struct {
	memoryOptions
	Text string `long:"text" description:"Note"`
	ID   string `long:"id" description:"Deduplication ID"`
}

type watchOptions struct {
	memoryOptions
	Period string `long:"period" default:"1m" description:"Watch interval"`
}

const usage = `Usage: horizon memory <add|show|compact|clear|watch> [options]
  --scope agent|user|workspace   Required except for watch (all existing scopes).
  --text TEXT                    Note for add.
  --id ID                        Optional deduplication ID for add.
  --workspace PATH               CLI workspace override; default: current directory.
  --period DURATION              Watch period, default 1m (minimum 1s).

Examples:
  horizon memory add --scope user --text 'Prefers concise Russian replies'
  horizon memory show --scope workspace
  horizon memory compact --scope user
  horizon memory clear --scope agent
  horizon memory watch --period 1m

Enable agent integration with agent_plugins: [memory] in config.yaml.
Compression uses plugins.memory.model (default_model if omitted) and may incur
provider charges. Without watch, expired memory is compacted at next turn/add.
Watch is an explicit foreground process; no system service is installed.
Read access, including HORIZON_INHERITED_ACCESS=read, forbids writes/compaction.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fail := func(code int, err error) int { fmt.Fprintf(stderr, "memory: %s\n", err); return code }
	if len(args) == 1 && args[0] == "horizon-plugin-metadata" {
		if err := json.NewEncoder(stdout).Encode(plugins.Metadata{ProtocolVersion: 1, AgentProtocolVersion: 1, Version: "1.0.0", Description: "Scoped persistent agent, user and workspace memory"}); err != nil {
			return fail(1, err)
		}
		return 0
	}
	if len(args) == 0 || len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := io.WriteString(stdout, usage)
		if err != nil {
			return fail(1, err)
		}
		return 0
	}
	if len(args) == 1 && strings.HasPrefix(args[0], "horizon-plugin-agent-") {
		return protocol(ctx, strings.TrimPrefix(args[0], "horizon-plugin-agent-"), stdin, stdout, stderr)
	}
	command := args[0]
	switch command {
	case "add", "show", "compact", "clear", "watch":
	default:
		return fail(2, fmt.Errorf("unknown command %q", command))
	}
	var common memoryOptions
	var add addOptions
	var watchArgs watchOptions
	var options any = &common
	if command == "add" {
		options = &add
	}
	if command == "watch" {
		options = &watchArgs
	}
	parser := flags.NewParser(options, flags.HelpFlag|flags.PassDoubleDash)
	parser.Name = "horizon memory " + command
	remaining, err := parser.ParseArgs(args[1:])
	if err != nil {
		var flagErr *flags.Error
		if errors.As(err, &flagErr) && flagErr.Type == flags.ErrHelp {
			if _, err := io.WriteString(stdout, usage); err != nil {
				return fail(1, err)
			}
			return 0
		}
		return fail(2, err)
	}
	if len(remaining) != 0 {
		return fail(2, fmt.Errorf("unexpected arguments"))
	}
	if command == "add" {
		common = add.memoryOptions
	}
	if command == "watch" {
		common = watchArgs.memoryOptions
	}
	if command != "watch" && common.Scope == "" {
		return fail(2, fmt.Errorf("--scope is required"))
	}
	if command == "watch" && common.Scope != "" {
		return fail(2, fmt.Errorf("watch operates on all existing scopes; omit --scope"))
	}
	access, err := effectiveAccess("write")
	if err != nil {
		return fail(2, err)
	}
	if command != "show" && !plugins.Allowed(access, "write_home") {
		return fail(2, fmt.Errorf("memory writes forbidden in access %s", access))
	}
	home, err := config.ResolveHome("")
	if err != nil {
		return fail(2, err)
	}
	cfg, s, err := configuration(home)
	if err != nil {
		return fail(2, err)
	}
	dir := common.Workspace
	if dir == "" {
		dir, err = os.Getwd()
		if err != nil {
			return fail(2, err)
		}
	}
	dir, err = session.NormalizeWorkspace(dir)
	if err != nil {
		return fail(2, err)
	}
	m := newStore(home, dir, s)
	summarize := apiSummarizer(cfg, s)
	var result any
	switch command {
	case "show":
		result, err = m.read(common.Scope)
	case "clear":
		err = m.clear(ctx, common.Scope)
		result = map[string]any{"cleared": err == nil, "scope": common.Scope}
	case "compact":
		result, err = m.compact(ctx, common.Scope, true, summarize)
	case "add":
		if add.ID == "" {
			add.ID, err = session.NewID()
		}
		if err == nil {
			result, err = addAndCompact(ctx, m, common.Scope, add.Text, add.ID, summarize)
		}
	case "watch":
		interval, e := time.ParseDuration(watchArgs.Period)
		if e != nil || interval < time.Second || interval > 24*time.Hour {
			return fail(2, fmt.Errorf("--period must be between 1s and 24h"))
		}
		err = watch(ctx, m, interval, summarize, stderr)
		if err != nil {
			if ctx.Err() != nil {
				return 130
			}
			return fail(1, err)
		}
		return 0
	}
	if err != nil {
		if ctx.Err() != nil {
			return fail(130, fmt.Errorf("cancelled"))
		}
		return fail(1, err)
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		return fail(1, err)
	}
	return 0
}
func configuration(home string) (config.Config, settings, error) {
	cfg, err := config.Load(home)
	if err != nil {
		return cfg, settings{}, fmt.Errorf("cannot load config.yaml; check configuration format and values")
	}
	s, err := loadSettings(cfg)
	return cfg, s, err
}
func effectiveAccess(requested string) (string, error) {
	if inherited := os.Getenv("HORIZON_INHERITED_ACCESS"); inherited != "" {
		requested = inherited
	}
	if requested != "read" && requested != "write" && requested != "full" {
		return "", fmt.Errorf("invalid access mode")
	}
	return requested, nil
}
func addAndCompact(ctx context.Context, m *memoryStore, scope, text, id string, summarize summarizer) (any, error) {
	n, duplicate, err := m.add(ctx, scope, text, id)
	if err != nil {
		return nil, err
	}
	status, compactErr := m.compact(ctx, scope, false, summarize)
	if compactErr != nil {
		status = compaction{Status: "error", Error: compactErr.Error()}
	}
	return map[string]any{"saved": true, "id": n.ID, "duplicate": duplicate, "compaction": status}, nil
}
func protocol(ctx context.Context, op string, stdin io.Reader, stdout, stderr io.Writer) int {
	var request plugins.Request
	data, err := io.ReadAll(io.LimitReader(stdin, plugins.OutputLimit+1))
	if err == nil && len(data) > plugins.OutputLimit {
		err = fmt.Errorf("request too large")
	}
	if err == nil {
		err = plugins.Decode(data, &request)
	}
	reply := plugins.Reply{}
	var result any
	if err == nil {
		var home, workspace string
		home, err = session.NormalizeWorkspace(request.Home)
		if err == nil {
			workspace, err = session.NormalizeWorkspace(request.Workspace)
		}
		if err == nil && (home != request.Home || workspace != request.Workspace || session.WorkspaceID(workspace) != request.WorkspaceID || strings.TrimSpace(request.OperationID) == "" || len(request.OperationID) > 512) {
			err = fmt.Errorf("invalid host execution context")
		}
		var access string
		if err == nil {
			access, err = effectiveAccess(request.Access)
		}
		if err == nil {
			var cfg config.Config
			var s settings
			cfg, s, err = configuration(home)
			if err == nil {
				m := newStore(home, workspace, s)
				summarize := apiSummarizer(cfg, s)
				switch op {
				case "describe":
					result = describe()
				case "context":
					result, err = m.context()
				case "maintain":
					if !plugins.Allowed(access, "write_home") {
						err = fmt.Errorf("maintenance forbidden in access %s", access)
					} else {
						statuses := map[string]compaction{}
						for _, scope := range []string{"agent", "user", "workspace"} {
							status, e := m.compact(ctx, scope, false, summarize)
							if e != nil {
								status = compaction{Status: "error", Error: e.Error()}
								fmt.Fprintf(stderr, "%s maintenance: %s\n", scope, e)
							}
							statuses[scope] = status
						}
						result = statuses
					}
				case "tool":
					var args struct {
						Scope string `json:"scope"`
						Text  string `json:"text,omitempty"`
					}
					err = validateTool(request.Tool, request.Arguments)
					if err == nil {
						err = plugins.Decode(request.Arguments, &args)
					}
					if err == nil {
						switch request.Tool {
						case "read":
							if args.Text != "" {
								err = fmt.Errorf("read accepts only scope")
							} else {
								result, err = m.contextScopes([]string{args.Scope})
							}
						case "add":
							if !plugins.Allowed(access, "write_home") {
								err = fmt.Errorf("add forbidden in access %s", access)
							} else {
								result, err = addAndCompact(ctx, m, args.Scope, args.Text, request.OperationID, summarize)
							}
						default:
							err = fmt.Errorf("unknown memory tool")
						}
					}
				default:
					err = fmt.Errorf("unknown agent operation")
				}
			}
		}
	}
	if err != nil {
		reply.Error = &plugins.Failure{Code: "memory_error", Message: err.Error()}
	} else {
		reply.OK = true
		reply.Data, err = json.Marshal(result)
		if err != nil {
			reply.OK = false
			reply.Error = &plugins.Failure{Code: "memory_error", Message: "cannot encode result"}
		}
	}
	if err := json.NewEncoder(stdout).Encode(reply); err != nil {
		fmt.Fprintln(stderr, "memory: cannot write protocol response")
		return 1
	}
	return 0
}
func (m *memoryStore) existing() ([]*memoryStore, error) {
	result := []*memoryStore{m}
	var failures []error
	paths, err := filepath.Glob(filepath.Join(m.home, "memory", "workspaces", "*.json"))
	if err != nil {
		return result, err
	}
	for _, path := range paths {
		// Resolve identity from the state's recorded normalized path, never from CLI/model input.
		f, err := os.Open(path)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, maxStateBytes+1))
		f.Close()
		if err != nil {
			failures = append(failures, err)
			continue
		}
		var s state
		if len(data) > maxStateBytes {
			failures = append(failures, fmt.Errorf("memory state too large"))
			continue
		}
		if err := plugins.Decode(data, &s); err != nil {
			failures = append(failures, err)
			continue
		}
		if s.Workspace == "" || filepath.Base(path) != session.WorkspaceID(s.Workspace)+".json" {
			failures = append(failures, fmt.Errorf("invalid stored workspace identity"))
			continue
		}
		if s.Workspace != m.workspace {
			other := newStore(m.home, s.Workspace, m.settings)
			other.now = m.now
			result = append(result, other)
		}
	}
	return result, errors.Join(failures...)
}
func watch(ctx context.Context, m *memoryStore, interval time.Duration, summarize summarizer, stderr io.Writer) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	nextWarning := time.Time{}
	for {
		warn := func(err error) {
			if !m.now().Before(nextWarning) {
				fmt.Fprintf(stderr, "memory watch: %s\n", err)
				nextWarning = m.now().Add(m.settings.retryInterval)
			}
		}
		stores, err := m.existing()
		if err != nil {
			warn(err)
		}
		{
			for index, store := range stores {
				scopes := []string{"workspace"}
				if index == 0 {
					scopes = []string{"agent", "user", "workspace"}
				}
				for _, scope := range scopes {
					if _, err := store.compact(ctx, scope, false, summarize); err != nil {
						warn(err)
					}
					if ctx.Err() != nil {
						return ctx.Err()
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func validateTool(name string, args json.RawMessage) error {
	for _, tool := range describe().Tools {
		if tool.Name == name {
			raw, _ := json.Marshal(tool.Parameters)
			var schema map[string]any
			if err := plugins.Decode(raw, &schema); err != nil {
				return err
			}
			return plugins.Validate(schema, args)
		}
	}
	return fmt.Errorf("unknown memory tool")
}
