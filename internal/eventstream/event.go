package eventstream

import "time"

type Event struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	SessionID string    `json:"session_id"`
	TurnID    *string   `json:"turn_id"`
	Data      any       `json:"data"`
}

type Publish func(Event)

func New(eventType, sessionID string, turnID *string, data any) Event {
	return Event{Type: eventType, Timestamp: time.Now().UTC(), SessionID: sessionID, TurnID: turnID, Data: data}
}
