// Package turninput defines the messages carried by a daemon-owned Horizon turn.
package turninput

import "context"

type Message struct {
	ID       string `json:"id"`
	Sequence uint64 `json:"sequence"`
	Source   string `json:"source"`
	Route    string `json:"route"`
	Author   string `json:"author"`
	Text     string `json:"text"`
}

// Channel delivers one message at a time. If closing is true and Next returns
// no message, the daemon has closed admission to this turn.
type Channel interface {
	Next(ctx context.Context, turnID string, closing bool) (*Message, error)
	Acknowledge(ctx context.Context, messageID, includedTurnID string) error
}
