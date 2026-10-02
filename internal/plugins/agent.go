package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ReanSn0w/horizon/internal/session"
)

const AgentProtocolVersion = 1
const OperationTimeout = 60 * time.Second
const PreparationTimeout = 90 * time.Second

// Request is host-selected execution context, never model-selected paths.
type Request struct {
	Home        string          `json:"home"`
	Workspace   string          `json:"workspace"`
	WorkspaceID string          `json:"workspace_id"`
	Access      string          `json:"access"`
	OperationID string          `json:"operation_id"`
	SessionID   string          `json:"session_id,omitempty"`
	TurnID      string          `json:"turn_id,omitempty"`
	CallID      string          `json:"call_id,omitempty"`
	Tool        string          `json:"tool,omitempty"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
}
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (f *Failure) Error() string { return f.Message }

type Reply struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error *Failure        `json:"error,omitempty"`
}
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Effect      string         `json:"effect"`
}
type Description struct {
	Instructions   string `json:"instructions"`
	Tools          []Tool `json:"tools"`
	MaintainEffect string `json:"maintain_effect,omitempty"`
}
type Block struct {
	Source string `json:"source"`
	Text   string `json:"text"`
}
type Extension struct {
	Name        string
	Path        string
	Request     Request
	Description Description
	Context     []Block
}

var functionName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func ToolName(plugin, local string) string { return plugin + "__" + local }
func Allowed(access, effect string) bool {
	switch effect {
	case "read":
		return access == "read" || access == "write" || access == "full"
	case "write_home", "write_workspace":
		return access == "write" || access == "full"
	case "unrestricted":
		return access == "full"
	}
	return false
}
func validEffect(effect string) bool { return Allowed("full", effect) }

// Prepare inspects only explicitly enabled plugins. The returned value is a turn snapshot.
func Prepare(ctx context.Context, home string, workspace session.Workspace, access string, names []string, maintain bool, warn func(string)) ([]Extension, error) {
	if len(names) == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, PreparationTimeout)
	defer cancel()
	home, err := session.NormalizeWorkspace(home)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	result := make([]Extension, 0, len(names))
	contextBytes := 0
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entry := Inspect(home, name, nil)
		if entry.Err != nil {
			return nil, fmt.Errorf("agent plugin %s: %w", name, entry.Err)
		}
		if entry.Metadata.AgentProtocolVersion != AgentProtocolVersion {
			return nil, fmt.Errorf("agent plugin %s: unsupported agent protocol %d", name, entry.Metadata.AgentProtocolVersion)
		}
		id, err := session.NewID()
		if err != nil {
			return nil, err
		}
		extension := Extension{Name: name, Path: entry.Path, Request: Request{Home: home, Workspace: workspace.Dir, WorkspaceID: workspace.ID, Access: access, OperationID: id}}
		reply, _, err := Call(ctx, entry.Path, "describe", extension.Request, MetadataTimeout)
		if err != nil {
			return nil, fmt.Errorf("agent plugin %s describe: %w", name, err)
		}
		if err = Decode(reply.Data, &extension.Description); err != nil {
			return nil, fmt.Errorf("agent plugin %s describe: %w", name, err)
		}
		if extension.Description.MaintainEffect != "" && !validEffect(extension.Description.MaintainEffect) {
			return nil, fmt.Errorf("agent plugin %s: invalid maintain effect", name)
		}
		for _, tool := range extension.Description.Tools {
			full := ToolName(name, tool.Name)
			if tool.Name == "" || len(full) > 64 || !functionName.MatchString(full) || seen[full] || strings.TrimSpace(tool.Description) == "" || !validEffect(tool.Effect) {
				return nil, fmt.Errorf("agent plugin %s: invalid or duplicate tool %q", name, tool.Name)
			}
			if err := CheckSchema(tool.Parameters); err != nil {
				return nil, fmt.Errorf("agent plugin %s tool %s: %w", name, tool.Name, err)
			}
			seen[full] = true
		}
		if maintain && Allowed(access, extension.Description.MaintainEffect) {
			_, diagnostic, err := Call(ctx, entry.Path, "maintain", extension.Request, OperationTimeout)
			if warn != nil {
				if diagnostic != "" {
					warn(fmt.Sprintf("agent plugin %s: %s", name, diagnostic))
				}
				if err != nil {
					warn(fmt.Sprintf("agent plugin %s maintenance: %s", name, err))
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		reply, _, err = Call(ctx, entry.Path, "context", extension.Request, MetadataTimeout)
		if err != nil {
			return nil, fmt.Errorf("agent plugin %s context: %w", name, err)
		}
		if err = Decode(reply.Data, &extension.Context); err != nil {
			return nil, fmt.Errorf("agent plugin %s context: %w", name, err)
		}
		contextBytes += len(extension.Description.Instructions)
		for _, block := range extension.Context {
			if strings.TrimSpace(block.Source) == "" {
				return nil, fmt.Errorf("agent plugin %s: context source is required", name)
			}
			contextBytes += len(block.Source) + len(block.Text)
		}
		if contextBytes > OutputLimit {
			return nil, fmt.Errorf("agent plugin context exceeds %d bytes", OutputLimit)
		}
		result = append(result, extension)
	}
	return result, nil
}
