package main

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/session"
	"github.com/chromedp/chromedp"
)

// This test creates a billable Browser Use session. It never runs by default.
func TestConfiguredBrowserUseAPI(t *testing.T) {
	if os.Getenv("HORIZON_BROWSER_API_CHECK") != "1" {
		t.Skip("set HORIZON_BROWSER_API_CHECK=1 to use a real Browser Use account")
	}
	home, err := config.ResolveHome("")
	if err != nil {
		t.Fatal(err)
	}
	s, err := configuration(home)
	if err != nil {
		t.Fatal(err)
	}
	id, err := session.NewID()
	if err != nil {
		t.Fatal(err)
	}
	api := newBrowserAPI(s)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	created, err := api.create(ctx, "horizon-api-check", id, id, id, 1)
	if created.ID != "" {
		defer func() {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer stopCancel()
			if err := api.stop(stopCtx, created.ID); err != nil {
				t.Errorf("stop test browser %s: %v", created.ID, err)
			}
		}()
	}
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.CDPURL == "" {
		t.Fatal("Browser Use did not return browser ID and CDP URL")
	}
	record := browserRecord{ID: created.ID, CDPURL: created.CDPURL}
	var page pageData
	targetID, err := runCDP(ctx, record, s.actionTimeout, cdpAction("navigate", navigateDocument("https://example.com")), cdpAction("snapshot", chromedp.Evaluate(snapshotScript, &page)))
	if err != nil {
		t.Fatal(err)
	}
	if page.URL != "https://example.com/" || !strings.Contains(strings.ToLower(page.Text), "documentation") {
		u, _ := url.Parse(page.URL)
		t.Fatalf("navigation content mismatch: scheme=%s host=%s title_expected=%t text_chars=%d text_expected=%t", u.Scheme, u.Hostname(), page.Title == "Example Domain", len(page.Text), strings.Contains(strings.ToLower(page.Text), "documentation"))
	}
	record.TargetID = targetID
	var second pageData
	secondID, err := runCDP(ctx, record, s.actionTimeout, cdpAction("snapshot", chromedp.Evaluate(snapshotScript, &second)))
	if err != nil {
		t.Fatal(err)
	}
	if secondID != targetID || second.URL != page.URL || !strings.Contains(strings.ToLower(second.Text), "documentation") {
		t.Fatal("reattachment did not retain expected page content")
	}
	for _, address := range []string{"https://habr.com", "https://news.ycombinator.com"} {
		var visited pageData
		id, err := runCDP(ctx, record, s.actionTimeout, cdpAction("navigate", navigateDocument(address)), cdpAction("snapshot", chromedp.Evaluate(snapshotScript, &visited)))
		if err != nil {
			t.Fatalf("public site check: %v", err)
		}
		u, err := url.Parse(visited.URL)
		expected, _ := url.Parse(address)
		if err != nil || u.Hostname() != expected.Hostname() || visited.Title == "" || len(visited.Text) < 50 {
			t.Fatal("public site did not return expected host and page content")
		}
		if id != targetID {
			t.Fatal("navigation did not reuse target")
		}
		t.Logf("verified host=%s text_chars=%d", u.Hostname(), len(visited.Text))
	}
}
