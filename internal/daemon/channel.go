package daemon

import (
	"encoding/json"
	"strings"

	"github.com/ReanSn0w/horizon/internal/plugins"
)

func channelEnvironment(env []string, task Task) []string {
	source, adapter := strings.CutPrefix(task.Owner, "adapter:")
	if !adapter || !task.Channel || task.Route == "" {
		return env
	}
	payload, _ := json.Marshal(plugins.TurnChannel{Extension: source, Route: task.Route})
	return append(env, plugins.TurnChannelEnvironment+"="+string(payload))
}
