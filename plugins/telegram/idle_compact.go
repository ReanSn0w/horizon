package main

import "time"

const minIdleCheckpointBytes = 128 << 10

func idleChatEligible(c *chat, now time.Time, after time.Duration) bool {
	if c == nil || !c.Available || c.Workspace == "" || c.Session == "" || after <= 0 {
		return false
	}
	last := c.LastReceivedAt
	if last.IsZero() {
		last = c.LastAt
	}
	if last.IsZero() || now.Before(last) || now.Sub(last) < after {
		return false
	}
	for _, j := range c.Jobs {
		if pending(j.Status) {
			return false
		}
	}
	return true
}
