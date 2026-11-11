package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type User struct {
	ID       int64  `json:"id"`
	Bot      bool   `json:"is_bot"`
	Username string `json:"username"`
	First    string `json:"first_name"`
	Last     string `json:"last_name"`
}
type Chat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Username string `json:"username"`
	First    string `json:"first_name"`
	Last     string `json:"last_name"`
	Forum    bool   `json:"is_forum"`
}
type Entity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}
type Message struct {
	ID          int64    `json:"message_id"`
	From        *User    `json:"from"`
	SenderChat  *Chat    `json:"sender_chat"`
	Chat        Chat     `json:"chat"`
	Date        int64    `json:"date"`
	Text        string   `json:"text"`
	Entities    []Entity `json:"entities"`
	Thread      int64    `json:"message_thread_id"`
	Reply       *Message `json:"reply_to_message"`
	MigrateTo   int64    `json:"migrate_to_chat_id"`
	MigrateFrom int64    `json:"migrate_from_chat_id"`
	NewTitle    string   `json:"new_chat_title"`
}
type Membership struct {
	Chat   Chat `json:"chat"`
	Member struct {
		Status string `json:"status"`
	} `json:"new_chat_member"`
}
type Update struct {
	ID         int64       `json:"update_id"`
	Message    *Message    `json:"message"`
	Membership *Membership `json:"my_chat_member"`
}
type Client struct {
	base, token string
	http        *http.Client
}

// A build-time override allows binary integration tests to use a local server.
// Runtime configuration cannot redirect the bot token to another endpoint.
var endpoint = "https://api.telegram.org"

type APIError struct {
	Code        int
	Retry       int
	Description string
	Migrate     int64
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Telegram HTTP/API %d: %s", e.Code, e.Description)
}
func (t *Client) call(ctx context.Context, method string, input, output any) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(t.base, "/")+"/bot"+t.token+"/"+method, bytes.NewReader(payload))
	if err != nil {
		return errors.New("invalid Telegram endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.http.Do(req)
	if err != nil {
		return errors.New("Telegram network error; request outcome may be unknown")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024+1))
	if err != nil || len(data) > 8*1024*1024 {
		return errors.New("Telegram response unreadable; request outcome may be unknown")
	}
	var value struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Code        int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			Retry   int   `json:"retry_after"`
			Migrate int64 `json:"migrate_to_chat_id"`
		} `json:"parameters"`
	}
	if err = json.Unmarshal(data, &value); err != nil {
		return errors.New("invalid Telegram response; request outcome may be unknown")
	}
	if !value.OK {
		if value.Code < 400 || value.Code >= 500 || resp.StatusCode >= 500 {
			return errors.New("Telegram did not confirm request outcome")
		}
		return &APIError{Code: value.Code, Retry: value.Parameters.Retry, Description: strings.ReplaceAll(value.Description, t.token, "[redacted]"), Migrate: value.Parameters.Migrate}
	}
	if resp.StatusCode != 200 {
		return errors.New("unexpected Telegram status; request outcome may be unknown")
	}
	if output != nil {
		if err = json.Unmarshal(value.Result, output); err != nil {
			return errors.New("invalid Telegram result; request outcome may be unknown")
		}
	}
	return nil
}
func (t *Client) Updates(ctx context.Context, offset int64) ([]Update, error) {
	var result []Update
	err := t.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 25, "allowed_updates": []string{"message", "my_chat_member"}}, &result)
	return result, err
}

// New keeps the endpoint independent of runtime configuration. Redirects are
// disabled because the Telegram API embeds the bot credential in its URL.
func New(token string) *Client {
	return &Client{base: endpoint, token: token, http: &http.Client{
		Timeout:       40 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (t *Client) Identity(ctx context.Context) (User, error) {
	var bot User
	err := t.call(ctx, "getMe", struct{}{}, &bot)
	if err == nil && (bot.ID <= 0 || bot.Username == "") {
		err = errors.New("invalid Telegram bot identity")
	}
	return bot, err
}

func (t *Client) CheckPolling(ctx context.Context) error {
	var webhook struct {
		URL string `json:"url"`
	}
	if err := t.call(ctx, "getWebhookInfo", struct{}{}, &webhook); err != nil {
		return err
	}
	if webhook.URL != "" {
		return errors.New("Telegram webhook is configured; remove it explicitly before starting long polling")
	}
	return nil
}

func (t *Client) Send(ctx context.Context, chat int64, text string) (int64, error) {
	var msg Message
	if err := t.call(ctx, "sendMessage", map[string]any{"chat_id": chat, "text": text}, &msg); err != nil {
		return 0, err
	}
	if msg.ID <= 0 || (msg.Chat.ID != 0 && msg.Chat.ID != chat) {
		return 0, errors.New("Telegram did not confirm message identity")
	}
	return msg.ID, nil
}

func (t *Client) Typing(ctx context.Context, chat, thread int64) error {
	data := map[string]any{"chat_id": chat, "action": "typing"}
	if thread > 0 {
		data["message_thread_id"] = thread
	}
	var ok bool
	if err := t.call(ctx, "sendChatAction", data, &ok); err != nil {
		return err
	}
	if !ok {
		return errors.New("Telegram did not confirm typing status")
	}
	return nil
}
