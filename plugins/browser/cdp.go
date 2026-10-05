package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/gobwas/ws"
)

// runCDP attaches to the remote browser only. Each plugin invocation is a
// short-lived process; cancelling a chromedp context closes its target tab, so
// successful calls let process exit detach the WebSocket without closing it.
func runCDP(parent context.Context, record browserRecord, timeout time.Duration, actions ...chromedp.Action) (string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	allocator, cancelAllocator := chromedp.NewRemoteAllocator(ctx, record.CDPURL, chromedp.NoModifyURL)
	// Library logs can contain full protocol messages and private CDP URLs.
	options := []chromedp.ContextOption{chromedp.WithLogf(func(string, ...any) {}), chromedp.WithErrorf(func(string, ...any) {})}
	if record.TargetID != "" {
		options = append(options, chromedp.WithTargetID(target.ID(record.TargetID)))
	}
	tab, cancelTab := chromedp.NewContext(allocator, options...)
	if err := chromedp.Run(tab); err != nil {
		phase := "connect"
		if state := chromedp.FromContext(tab); state != nil && state.Browser != nil {
			phase = "target"
		}
		failure := contextualCDPError(ctx, phase, err)
		cancelTab()
		cancelAllocator()
		cancel()
		return "", failure
	}
	if err := chromedp.Run(tab, actions...); err != nil {
		failure := contextualCDPError(ctx, "action", err)
		cancelTab()
		cancelAllocator()
		cancel()
		return "", failure
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

// cdpAction marks the operation before discarding untrusted error text.
func cdpAction(phase string, action chromedp.Action) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		if err := action.Do(ctx); err != nil {
			return safeCDPError(phase, err)
		}
		return nil
	})
}

type cdpFailure struct{ phase, reason string }

func (e *cdpFailure) Error() string { return "remote browser " + e.phase + " failed: " + e.reason }

// Never expose arbitrary transport/protocol errors. They may embed credentials,
// request URLs or server-controlled text. Only emit fixed labels and numbers.
func safeCDPError(phase string, err error) error {
	var failure *cdpFailure
	if errors.As(err, &failure) {
		return failure
	}
	reason := "unclassified error"
	var protocolError *cdproto.Error
	var status ws.StatusError
	var dns *net.DNSError
	var network *net.OpError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		reason = "timeout"
	case errors.Is(err, context.Canceled):
		reason = "canceled"
	case errors.As(err, &status):
		reason = fmt.Sprintf("WebSocket handshake HTTP %d", int(status))
	case errors.As(err, &protocolError):
		reason = fmt.Sprintf("CDP error code %d", protocolError.Code)
	case errors.As(err, &dns):
		reason = "DNS lookup failed"
	case errors.Is(err, syscall.ECONNREFUSED):
		reason = "connection refused"
	case errors.Is(err, io.EOF), errors.Is(err, net.ErrClosed):
		reason = "connection closed"
	case errors.As(err, &network):
		if network.Timeout() {
			reason = "network timeout"
		} else {
			reason = "network error"
		}
	default:
		// Navigate returns Chromium's errorText as an ordinary error. Allow only
		// complete, known codes; never copy a substring from arbitrary error text.
		switch err.Error() {
		case "net::ERR_NAME_NOT_RESOLVED", "net::ERR_CONNECTION_REFUSED", "net::ERR_CONNECTION_TIMED_OUT", "net::ERR_TUNNEL_CONNECTION_FAILED", "net::ERR_PROXY_CONNECTION_FAILED", "net::ERR_CERT_AUTHORITY_INVALID", "net::ERR_ABORTED", "net::ERR_INTERNET_DISCONNECTED":
			reason = err.Error()
		}
	}
	return &cdpFailure{phase: phase, reason: reason}
}

// chromedp cancels child contexts when the allocator stops, which can turn a
// parent deadline into context.Canceled. Inspect our deadline before cleanup.
func contextualCDPError(ctx context.Context, phase string, err error) error {
	var failure *cdpFailure
	if errors.As(err, &failure) {
		phase = failure.phase
	}
	if ctx.Err() != nil {
		return safeCDPError(phase, ctx.Err())
	}
	return safeCDPError(phase, err)
}
