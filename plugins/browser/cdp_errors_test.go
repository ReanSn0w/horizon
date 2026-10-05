package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/chromedp"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func TestSafeCDPErrors(t *testing.T) {
	const secret = "PRIVATE-CDP-TOKEN"
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"timeout", fmt.Errorf("%s: %w", secret, context.DeadlineExceeded), "timeout"},
		{"cancel", fmt.Errorf("%s: %w", secret, context.Canceled), "canceled"},
		{"handshake", fmt.Errorf("%s: %w", secret, ws.StatusError(403)), "WebSocket handshake HTTP 403"},
		{"protocol", &cdproto.Error{Code: -32601, Message: secret}, "CDP error code -32601"},
		{"dns", &net.DNSError{Name: secret, Err: secret}, "DNS lookup failed"},
		{"refused", &net.OpError{Op: secret, Err: syscall.ECONNREFUSED}, "connection refused"},
		{"unknown", errors.New("wss://" + secret + "?key=" + secret), "unclassified error"},
		{"navigation", errors.New("net::ERR_TUNNEL_CONNECTION_FAILED"), "net::ERR_TUNNEL_CONNECTION_FAILED"},
		{"untrusted navigation", errors.New("net::ERR_TUNNEL_CONNECTION_FAILED " + secret), "unclassified error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := safeCDPError("navigate", test.err).Error()
			if got != "remote browser navigate failed: "+test.want || strings.Contains(got, secret) {
				t.Fatal(got)
			}
		})
	}
}

func TestCDPActionKeepsStage(t *testing.T) {
	err := cdpAction("snapshot", chromedp.ActionFunc(func(context.Context) error { return errors.New("secret") })).Do(context.Background())
	if got := safeCDPError("action", err).Error(); got != "remote browser snapshot failed: unclassified error" {
		t.Fatal(got)
	}
}

func TestCDPHandshakeFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "secret", http.StatusForbidden) }))
	defer server.Close()
	_, err := runCDP(context.Background(), browserRecord{CDPURL: strings.Replace(server.URL, "http://", "ws://", 1) + "/secret?token=secret"}, time.Second)
	if err == nil || err.Error() != "remote browser connect failed: WebSocket handshake HTTP 403" {
		t.Fatalf("%v", err)
	}
}

func TestCDPTargetFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, _, err := ws.UpgradeHTTP(r, w)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		if _, err := wsutil.ReadClientText(conn); err != nil {
			return
		}
		_ = wsutil.WriteServerText(conn, []byte(`{"id":1,"error":{"code":-32601,"message":"secret"}}`))
		_, _ = wsutil.ReadClientText(conn)
	}))
	defer server.Close()
	_, err := runCDP(context.Background(), browserRecord{CDPURL: strings.Replace(server.URL, "http://", "ws://", 1)}, time.Second)
	if err == nil || err.Error() != "remote browser target failed: CDP error code -32601" {
		t.Fatalf("%v", err)
	}
}

func TestCDPConnectTimeoutAndCancel(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer server.Close()
	defer close(release)
	record := browserRecord{CDPURL: strings.Replace(server.URL, "http://", "ws://", 1)}
	_, err := runCDP(context.Background(), record, 50*time.Millisecond)
	if err == nil || err.Error() != "remote browser connect failed: timeout" {
		t.Fatalf("%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = runCDP(ctx, record, time.Second)
	if err == nil || err.Error() != "remote browser connect failed: canceled" {
		t.Fatalf("%v", err)
	}
}
