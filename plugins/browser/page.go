package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/ReanSn0w/horizon/internal/plugins"
	"github.com/ReanSn0w/horizon/internal/session"
	"github.com/chromedp/chromedp"
)

type elementRef struct {
	Ref         string `json:"ref"`
	Selector    string `json:"selector"`
	Fingerprint string `json:"fingerprint"`
	Role        string `json:"role"`
	Name        string `json:"name"`
}

type pageData struct {
	URL       string       `json:"url"`
	Title     string       `json:"title"`
	Text      string       `json:"text"`
	Elements  []elementRef `json:"elements"`
	Truncated bool         `json:"truncated"`
}

type pageResult struct {
	URL        string `json:"url"`
	Title      string `json:"title"`
	Text       string `json:"text"`
	SnapshotID string `json:"snapshot_id"`
	Elements   []struct {
		Ref  string `json:"ref"`
		Role string `json:"role"`
		Name string `json:"name"`
	} `json:"elements"`
	Truncated bool `json:"truncated"`
}

const snapshotScript = `(() => {
  const body = document.body;
  const all = body ? Array.from(body.querySelectorAll('a,button,input,textarea,select,[role="button"],[role="link"]')) : [];
  const text = body ? (body.innerText || '') : '';
  const selector = el => {
    const parts = [];
    while (el && el.nodeType === 1 && parts.length < 24) {
      let n = 1;
      for (let prev = el.previousElementSibling; prev; prev = prev.previousElementSibling) if (prev.tagName === el.tagName) n++;
      parts.unshift(el.tagName.toLowerCase() + ':nth-of-type(' + n + ')');
      el = el.parentElement;
    }
    return parts.join(' > ');
  };
  const fingerprint = el => [el.tagName, el.id.slice(0,100), String(el.className).slice(0,100), el.getAttribute('type') || '', (el.textContent || '').trim().slice(0,100)].join('|');
  const chosen = all.slice(0,50).filter(el => !['password','file','hidden'].includes(el.getAttribute('type')));
  const elements = chosen.map((el, i) => ({
    ref: 'e' + (i+1), selector: selector(el), fingerprint: fingerprint(el),
    role: (el.getAttribute('role') || el.tagName.toLowerCase()).slice(0,24),
    name: (el.getAttribute('aria-label') || el.getAttribute('placeholder') || el.innerText || '').trim().slice(0,64)
  })).filter(item => item.selector.length <= 1024);
  return {url: location.href.slice(0,1024), title: document.title.slice(0,256), text: text.slice(0,6000), elements, truncated: text.length > 6000 || all.length > 50 || elements.length < chosen.length};
})()`

func (b browserLifecycle) tool(ctx context.Context, request plugins.Request) (any, error) {
	if request.Access != "full" {
		return nil, fmt.Errorf("browser tools require full access")
	}
	var args struct {
		URL        string `json:"url"`
		SnapshotID string `json:"snapshot_id"`
		Ref        string `json:"ref"`
		Text       string `json:"text"`
	}
	if err := plugins.Decode(request.Arguments, &args); err != nil {
		return nil, fmt.Errorf("invalid browser tool arguments")
	}
	var record browserRecord
	var err error
	if request.Tool == "navigate" {
		parsed, parseErr := url.Parse(args.URL)
		if parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || len(args.URL) > 2048 {
			return nil, fmt.Errorf("browser URL must be HTTP(S) and at most 2048 characters")
		}
		record, err = b.open(ctx, request)
	} else {
		record, err = b.state.recordFor(request)
		if err != nil {
			return nil, fmt.Errorf("open a URL with browser__navigate first")
		}
	}
	if err != nil {
		return nil, err
	}
	var page pageData
	actions := []chromedp.Action{}
	switch request.Tool {
	case "navigate":
		actions = append(actions, cdpAction("navigate", navigateDocument(args.URL)))
	case "snapshot":
	case "click", "type":
		var selected *elementRef
		if args.SnapshotID == "" || args.SnapshotID != record.SnapshotID {
			return nil, fmt.Errorf("stale browser snapshot; call browser__snapshot")
		}
		for i := range record.Elements {
			if record.Elements[i].Ref == args.Ref {
				selected = &record.Elements[i]
				break
			}
		}
		if selected == nil {
			return nil, fmt.Errorf("unknown browser element reference")
		}
		if request.Tool == "type" && len(args.Text) > 4096 {
			return nil, fmt.Errorf("browser text exceeds 4096 characters")
		}
		var current string
		check := `(() => { const el = document.querySelector(` + strconv.Quote(selected.Selector) + `); return el ? [el.tagName, el.id.slice(0,100), String(el.className).slice(0,100), el.getAttribute('type') || '', (el.textContent || '').trim().slice(0,100)].join('|') : ''; })()`
		actions = append(actions, chromedp.Evaluate(check, &current), chromedp.ActionFunc(func(context.Context) error {
			if current != selected.Fingerprint {
				return fmt.Errorf("stale browser element; call browser__snapshot")
			}
			return nil
		}))
		if request.Tool == "click" {
			actions = append(actions, cdpAction("click", chromedp.Click(selected.Selector)))
		} else {
			actions = append(actions, cdpAction("type", chromedp.Clear(selected.Selector)), cdpAction("type", chromedp.SendKeys(selected.Selector, args.Text)))
		}
	default:
		return nil, fmt.Errorf("unsupported browser tool")
	}
	actions = append(actions, cdpAction("snapshot", chromedp.Evaluate(snapshotScript, &page)))
	targetID, err := runCDP(ctx, record, b.s.actionTimeout, actions...)
	if err != nil {
		return nil, err
	}
	id, err := session.NewID()
	if err != nil {
		return nil, err
	}
	record.TargetID = targetID
	record.SnapshotID = id
	record.Elements = page.Elements
	if err := b.state.withLock(func() error { return b.state.save(record) }); err != nil {
		return nil, err
	}
	return renderPage(page, id), nil
}

func renderPage(page pageData, snapshotID string) pageResult {
	result := pageResult{URL: page.URL, Title: page.Title, Text: page.Text, SnapshotID: snapshotID, Truncated: page.Truncated}
	for _, element := range page.Elements {
		result.Elements = append(result.Elements, struct {
			Ref  string `json:"ref"`
			Role string `json:"role"`
			Name string `json:"name"`
		}{element.Ref, element.Role, element.Name})
	}
	for {
		encoded, err := json.Marshal(result)
		if err == nil && len(encoded) <= 48<<10 {
			break
		}
		result.Truncated = true
		if len(result.Text) > 0 {
			runes := []rune(result.Text)
			result.Text = string(runes[:len(runes)/2])
		} else if len(result.Elements) > 0 {
			result.Elements = result.Elements[:len(result.Elements)-1]
		} else {
			break
		}
	}
	return result
}

func browserTools() plugins.Description {
	stringField := func() map[string]any { return map[string]any{"type": "string"} }
	schema := func(properties map[string]any, required ...string) map[string]any {
		if required == nil {
			required = []string{}
		}
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	return plugins.Description{
		Instructions:   "Use browser__navigate to open a public HTTP(S) page. Read the returned text and element refs. Call browser__snapshot to refresh refs; click and type require the latest snapshot_id. Do not type passwords, tokens, or other secrets. Browser sessions stop automatically at the end of this turn.",
		FinalizeEffect: "unrestricted",
		Tools: []plugins.Tool{
			{Name: "navigate", Description: "Open a public HTTP(S) URL and return a text snapshot", Effect: "unrestricted", Parameters: schema(map[string]any{"url": stringField()}, "url")},
			{Name: "snapshot", Description: "Read the current page as text and element references", Effect: "unrestricted", Parameters: schema(map[string]any{})},
			{Name: "click", Description: "Click an element from the latest snapshot", Effect: "unrestricted", Parameters: schema(map[string]any{"snapshot_id": stringField(), "ref": stringField()}, "snapshot_id", "ref")},
			{Name: "type", Description: "Replace the contents of an input with ordinary text", Effect: "unrestricted", Parameters: schema(map[string]any{"snapshot_id": stringField(), "ref": stringField(), "text": stringField()}, "snapshot_id", "ref", "text")},
		},
	}
}
