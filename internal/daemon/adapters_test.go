package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAdapterSupervision(t *testing.T) {
	h := shortHome(t)
	os.MkdirAll(filepath.Join(h, "bin"), 0700)
	script := `#!/bin/sh
if [ "$1" = agent-plugin-metadata ]; then
 echo '{"protocol_version":1,"version":"test","description":"fixture","capabilities":["tasks"]}'
 exit
fi
echo started > "$HORIZON_HOME/started"
sleep 30
`
	os.WriteFile(filepath.Join(h, "bin", "agent-test"), []byte(script), 0700)
	s := Defaults()
	s.Adapters = []string{"test"}
	q, _ := OpenQueue(h, s, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, e := q.SuperviseAdapters(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer stop()
	for i := 0; i < 100; i++ {
		if _, e := os.Stat(filepath.Join(h, "started")); e == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, e := os.Stat(filepath.Join(h, "started")); e != nil {
		t.Fatal(e)
	}
	r := request("submit", "event")
	r.Caller.Kind = "adapter"
	r.Caller.Source = "test"
	r.Caller.Session = ""
	if _, e := q.Handle(ctx, r); e == nil {
		t.Fatal("unregistered credential accepted")
	}
	q.mu.Lock()
	r.Caller.Credential = q.adapters["test"]
	q.mu.Unlock()
	a := taskResult(t, q, r)
	r.ID = "retry"
	b := taskResult(t, q, r)
	if a.ID != b.ID {
		t.Fatal("event duplicated")
	}
	r.Caller.Source = "other"
	if _, e := q.Handle(ctx, r); e == nil {
		t.Fatal("cross source")
	}
}
func TestDisabledAdapterNotStarted(t *testing.T) {
	h := shortHome(t)
	os.MkdirAll(filepath.Join(h, "bin"), 0700)
	os.WriteFile(filepath.Join(h, "bin", "agent-bad"), []byte("#!/bin/sh\nexit 9\n"), 0700)
	q, _ := OpenQueue(h, Defaults(), nil)
	stop, e := q.SuperviseAdapters(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	stop()
}
