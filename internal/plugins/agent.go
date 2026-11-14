package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ReanSn0w/horizon/internal/session"
)

const AgentProtocolVersion = 1
const OperationTimeout = 60 * time.Second
const PreparationTimeout = 90 * time.Second

// Request is host-selected execution context, never model-selected paths.
type Request struct {
	Home          string          `json:"home"`
	Workspace     string          `json:"workspace"`
	WorkspaceID   string          `json:"workspace_id"`
	Access        string          `json:"access"`
	OperationID   string          `json:"operation_id"`
	SessionID     string          `json:"session_id,omitempty"`
	TurnID        string          `json:"turn_id,omitempty"`
	CallID        string          `json:"call_id,omitempty"`
	Tool          string          `json:"tool,omitempty"`
	Arguments     json.RawMessage `json:"arguments,omitempty"`
	Route         string          `json:"route,omitempty"`
	Communication bool            `json:"communication,omitempty"`
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
	FinalizeEffect string `json:"finalize_effect,omitempty"`
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
	return PrepareForTurn(ctx, home, workspace, access, names, maintain, warn, TurnPolicy{})
}

type TurnPolicy struct {
	Channel       *TurnChannel
	Communication []string
	SessionID     string
}

const CommunicationEffect = "communicate"

func ToolAllowed(request Request, effect string) bool {
	if effect == CommunicationEffect {
		return request.Communication && Allowed(request.Access, "read")
	}
	return Allowed(request.Access, effect)
}

func PrepareForTurn(ctx context.Context, home string, workspace session.Workspace, access string, names []string, maintain bool, warn func(string), policy TurnPolicy) ([]Extension, error) {
	channel := policy.Channel
	if err := validateTurnChannel(channel, names); err != nil {
		return nil, err
	}
	if channel != nil && !slices.Contains(policy.Communication, channel.Extension) {
		return nil, fmt.Errorf("host channel %s requires communication: true in its extensions entry", channel.Extension)
	}
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
		metadataTimeout := MetadataTimeout
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < metadataTimeout {
			metadataTimeout = time.Until(deadline)
		}
		entry := InspectTimeout(home, name, nil, metadataTimeout)
		if entry.Err != nil {
			return nil, fmt.Errorf("agent plugin %s: %w", name, entry.Err)
		}
		version := entry.Metadata.AgentProtocolVersion
		if version != AgentProtocolVersion && version != ChannelProtocolVersion {
			return nil, fmt.Errorf("agent plugin %s: unsupported agent protocol %d", name, entry.Metadata.AgentProtocolVersion)
		}
		id, err := session.NewID()
		if err != nil {
			return nil, err
		}
		extension := Extension{Name: name, Path: entry.Path, Request: Request{Home: home, Workspace: workspace.Dir, WorkspaceID: workspace.ID, Access: access, OperationID: id}}
		grant := (channel == nil || channel.Extension == name) && slices.Contains(policy.Communication, name)
		if grant && version != ChannelProtocolVersion {
			return nil, fmt.Errorf("extensions.%s.communication requires agent protocol %d", name, ChannelProtocolVersion)
		}
		if version == ChannelProtocolVersion {
			extension.Request.Communication = grant
			extension.Request.SessionID = policy.SessionID
		}
		if channel != nil && channel.Extension == name {
			if version != ChannelProtocolVersion {
				return nil, fmt.Errorf("host channel %s requires agent protocol %d", name, ChannelProtocolVersion)
			}
			extension.Request.Route = channel.Route
		}
		reply, _, err := Call(ctx, entry.Path, "describe", extension.Request, MetadataTimeout)
		if err != nil {
			return nil, fmt.Errorf("agent plugin %s describe: %w", name, err)
		}
		if err = Decode(reply.Data, &extension.Description); err != nil {
			return nil, fmt.Errorf("agent plugin %s describe: %w", name, err)
		}
		if extension.Description.Tools == nil {
			return nil, fmt.Errorf("agent plugin %s: describe requires a tools array", name)
		}
		if extension.Description.MaintainEffect != "" && !validEffect(extension.Description.MaintainEffect) {
			return nil, fmt.Errorf("agent plugin %s: invalid maintain effect", name)
		}
		if extension.Description.FinalizeEffect != "" && !validEffect(extension.Description.FinalizeEffect) {
			return nil, fmt.Errorf("agent plugin %s: invalid finalize effect", name)
		}
		for _, tool := range extension.Description.Tools {
			full := ToolName(name, tool.Name)
			if tool.Name == "" || len(full) > 64 || !functionName.MatchString(full) || seen[full] || strings.TrimSpace(tool.Description) == "" || (!validEffect(tool.Effect) && !(version == ChannelProtocolVersion && tool.Effect == CommunicationEffect)) {
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
		if !strings.HasPrefix(strings.TrimSpace(string(reply.Data)), "[") {
			return nil, fmt.Errorf("agent plugin %s: context must be an array", name)
		}
		if err = Decode(reply.Data, &extension.Context); err != nil {
			return nil, fmt.Errorf("agent plugin %s context: %w", name, err)
		}
		encodedContext, err := json.Marshal(extension.Context)
		if err != nil {
			return nil, err
		}
		contextBytes += len(extension.Description.Instructions) + len(encodedContext)
		for _, block := range extension.Context {
			if strings.TrimSpace(block.Source) == "" {
				return nil, fmt.Errorf("agent plugin %s: context source is required", name)
			}
		}
		if contextBytes > OutputLimit {
			return nil, fmt.Errorf("agent plugin context exceeds %d bytes", OutputLimit)
		}
		result = append(result, extension)
	}
	return result, nil
}
