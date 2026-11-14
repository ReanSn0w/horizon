package plugins

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"unicode/utf8"
)

// TurnChannelEnvironment carries host-selected context for a single CLI turn.
// Shells and extension subprocesses do not inherit it.
const TurnChannelEnvironment = "HORIZON_TURN_CHANNEL"

const ChannelProtocolVersion = 2

type TurnChannel struct {
	Extension string `json:"extension"`
	Route     string `json:"route"`
}

var extensionName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func ReadTurnChannel() (*TurnChannel, error) {
	data, exists := os.LookupEnv(TurnChannelEnvironment)
	if !exists {
		return nil, nil
	}
	var channel TurnChannel
	if len(data) > 4096 || Decode([]byte(data), &channel) != nil || !extensionName.MatchString(channel.Extension) ||
		channel.Route == "" || len(channel.Route) > 512 || !utf8.ValidString(channel.Route) {
		return nil, errors.New("invalid host turn channel")
	}
	return &channel, nil
}

func validateTurnChannel(channel *TurnChannel, names []string) error {
	if channel == nil {
		return nil
	}
	for _, name := range names {
		if name == channel.Extension {
			return nil
		}
	}
	return fmt.Errorf("host channel %s requires enabled: true in its extensions entry", channel.Extension)
}
