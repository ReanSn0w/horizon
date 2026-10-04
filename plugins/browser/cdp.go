package main

import (
	"context"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// runCDP attaches to the remote browser only. Each plugin invocation is a
// short-lived process; cancelling a chromedp context closes its target tab, so
// successful calls let process exit detach the WebSocket without closing it.
func runCDP(parent context.Context, record browserRecord, timeout time.Duration, actions ...chromedp.Action) (string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	allocator, cancelAllocator := chromedp.NewRemoteAllocator(ctx, record.CDPURL, chromedp.NoModifyURL)
	options := []chromedp.ContextOption{}
	if record.TargetID != "" {
		options = append(options, chromedp.WithTargetID(target.ID(record.TargetID)))
	}
	tab, cancelTab := chromedp.NewContext(allocator, options...)
	if err := chromedp.Run(tab, actions...); err != nil {
		cancelTab()
		cancelAllocator()
		cancel()
		return "", fmt.Errorf("remote browser action failed")
	}
	state := chromedp.FromContext(tab)
	if state == nil || state.Target == nil || state.Target.TargetID == "" {
		cancelTab()
		cancelAllocator()
		cancel()
		return "", fmt.Errorf("remote browser target missing")
	}
	// The process exits immediately after returning its result. Do not invoke
	// chromedp's cancellation hooks on success: they close the retained tab.
	_ = cancelTab
	_ = cancelAllocator
	_ = cancel
	return string(state.Target.TargetID), nil
}
