package main

import (
	"context"
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
	targetID, err := runCDP(ctx, record, s.actionTimeout, cdpAction("navigate", chromedp.Navigate("https://example.com")), cdpAction("snapshot", chromedp.Evaluate(snapshotScript, &page)))
	if err != nil {
		t.Fatal(err)
	}
	if page.URL != "https://example.com/" || !strings.Contains(page.Text, "Example Domain") {
		t.Fatal("navigation did not return expected page content")
	}
	record.TargetID = targetID
	var second pageData
	secondID, err := runCDP(ctx, record, s.actionTimeout, cdpAction("snapshot", chromedp.Evaluate(snapshotScript, &second)))
	if err != nil {
		t.Fatal(err)
	}
	if secondID != targetID || second.URL != page.URL || !strings.Contains(second.Text, "Example Domain") {
		t.Fatal("reattachment did not retain expected page content")
	}
}
