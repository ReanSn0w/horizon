package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func splitMessage(text string) []string {
	result := []string{}
	runes := []rune(text)
	// Telegram's entity positions use UTF-16. A 4000-unit budget also fits
	// the 4096-character sendMessage limit when astral characters occur.
	for len(runes) > 0 {
		units, end := 0, 0
		for end < len(runes) {
			cost := 1
			if runes[end] > 0xffff {
				cost = 2
			}
			if units+cost > 4000 {
				break
			}
			units += cost
			end++
		}
		result = append(result, string(runes[:end]))
		runes = runes[end:]
	}
	return result
}
func (g *bridge) delivery(ctx context.Context, c *chat, j *job) error {
	if len(j.Parts) == 0 {
		parts := splitMessage(j.Response)
		if len(parts) == 0 {
			return g.fail(c.ID, j.ID, errors.New("empty saved response"), false)
		}
		if err := g.setJob(c.ID, j.ID, func(_ *chat, j *job) error { j.Parts = parts; return nil }); err != nil {
			return err
		}
		j.Parts = parts
	}
	for len(j.Sent) < len(j.Parts) {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Resolve migrated chat IDs immediately before addressing Telegram.
		value, err := g.store.snapshot()
		if err != nil {
			return err
		}
		c, err = resolveChat(&value, strconv.FormatInt(c.ID, 10))
		if err != nil {
			return err
		}
		if !c.Available {
			return g.fail(c.ID, j.ID, errors.New("chat is unavailable"), false)
		}
		index := len(j.Sent)
		if err = g.setJob(c.ID, j.ID, func(_ *chat, j *job) error { j.Status = "sending"; return nil }); err != nil {
			return err
		}
		messageID, err := g.tg.send(ctx, c, j, j.Parts[index])
		if err != nil {
			var api *apiError
			if errors.As(err, &api) && api.Code == 429 {
				if err = g.setJob(c.ID, j.ID, func(_ *chat, j *job) error { j.Status = "generated"; return nil }); err != nil {
					return err
				}
				if !pause(ctx, time.Duration(max(api.Retry, 1))*time.Second) {
					return ctx.Err()
				}
				continue
			}
			unknown := true
			if errors.As(err, &api) && api.Code >= 400 && api.Code < 500 {
				unknown = false
				if api.Code == 403 {
					if saveErr := g.store.update(func(v *state) error {
						current, e := resolveChat(v, fmt.Sprint(c.ID))
						if e == nil {
							current.Available = false
						}
						return e
					}); saveErr != nil {
						return saveErr
					}
				}
			}
			return g.fail(c.ID, j.ID, err, unknown)
		}
		text := j.Parts[index]
		if err = g.setJob(c.ID, j.ID, func(c *chat, j *job) error {
			j.Sent = append(j.Sent, messageID)
			appendHistory(c, record{ID: messageID, Author: g.bot.ID, Name: g.bot.Username, Text: text, Time: time.Now().Unix(), Thread: j.Thread, Bot: true})
			j.Status = "generated"
			if len(j.Sent) == len(j.Parts) {
				j.Status = "sent"
				c.LastError = ""
			}
			return nil
		}); err != nil {
			return err
		}
		j.Sent = append(j.Sent, messageID)
		if len(j.Sent) < len(j.Parts) && !pause(ctx, time.Second) {
			return ctx.Err()
		}
	}
	return nil
}
func safeLine(value string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 || r == 0x2028 || r == 0x2029 {
			return ' '
		}
		return r
	}, value)
}
