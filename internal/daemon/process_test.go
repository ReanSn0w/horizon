package daemon

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProcessCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, e := Process(ctx, "/bin/sh", []string{"-c", "sleep 30"}, os.Environ(), "/tmp", "", &bytes.Buffer{})
	if e == nil || time.Since(start) > 5*time.Second {
		t.Fatal(e, time.Since(start))
	}
}
func TestChildEnvironment(t *testing.T) {
	t.Setenv("HORIZON_INHERITED_ACCESS", "full")
	t.Setenv("HORIZON_ADAPTER_CREDENTIAL", "secret")
	env := strings.Join(childEnv("/tmp/home", "read"), "\n")
	if strings.Contains(env, "secret") || strings.Contains(env, "ACCESS=full") || !strings.Contains(env, "ACCESS=read") {
		t.Fatal("invalid inherited environment")
	}
}
func TestStaleSocket(t *testing.T) {
	h := shortHome(t)
	os.MkdirAll(filepath.Join(h, "run"), 0700)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, h, nil) }()
	for i := 0; i < 100; i++ {
		if Hello(ctx, h) == nil {
			break
		}
		time.Sleep(time.Millisecond * 10)
	}
	cancel()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}

func TestInputCredentialIsConsumedByHorizon(t *testing.T) {
	const credential = "01234567890123456789abcdefabcdef"
	if HasTurnInput() {
		channel, err := OpenTurnInput("/tmp/home", "/tmp", "session")
		if err != nil {
			t.Fatal(err)
		}
		client := channel.(*inputClient)
		if client.caller.Credential != credential || client.task != "task" || client.route != "chat" || HasTurnInput() {
			t.Fatal("input context was not consumed correctly")
		}
		if _, present := os.LookupEnv(inputRouteEnv); present {
			t.Fatal("route was left in the process environment")
		}
		return
	}
	if channel, err := OpenTurnInput("/tmp/home", "/tmp", ""); channel != nil || err != nil {
		t.Fatalf("standalone resume got an input channel: %v", err)
	}
	input, err := inputCredentialPipe(credential)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := append(childEnv("/tmp/home", "write"), inputTaskEnv+"=task", inputRouteEnv+"=chat")
	if strings.Contains(strings.Join(env, "\n"), credential) {
		t.Fatal("credential leaked into the child environment")
	}
	var output bytes.Buffer
	if _, err := process(context.Background(), binary, []string{"-test.run=^TestInputCredentialIsConsumedByHorizon$"}, env, "/tmp", "", &output, []*os.File{input}); err != nil {
		t.Fatalf("credential descriptor was not inherited: %v: %s", err, output.String())
	}
}
