package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/eventstream"
)

type cappedWriter struct {
	bytes.Buffer
	limit int
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if w.Len()+len(p) > w.limit {
		return 0, errors.New("process output exceeds limit")
	}
	return w.Buffer.Write(p)
}
func (w *cappedWriter) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{w}, r)
}

// Process controls the process group even when the immediate child exits first.
func Process(ctx context.Context, binary string, args, env []string, dir, input string, out io.Writer) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(time.Second)
		exited := false
		select {
		case <-done:
			exited = true
		case <-timer.C:
		}
		timer.Stop()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if !exited {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
			}
		}
		return -1, ctx.Err()
	}
	code := 0
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	return code, err
}
func childEnv(home, access string) []string {
	env := []string{}
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "HORIZON_") {
			continue
		}
		env = append(env, e)
	}
	return append(env, "HORIZON_HOME="+home, "HORIZON_EXECUTABLE="+filepath.Join(home, "bin", "horizon"), "HORIZON_INHERITED_ACCESS="+access)
}

var sessionID = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func HorizonRunner(home string) Runner {
	return func(ctx context.Context, t Task, set func(string) error, stage func(string, string)) Outcome {
		cfg, err := config.Load(home)
		if err != nil {
			return Outcome{Err: errors.New("invalid Horizon configuration")}
		}
		if cfg.Limits.MaxTurnDuration > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, cfg.Limits.MaxTurnDuration+2*time.Minute)
			defer cancel()
		}
		binary := filepath.Join(home, "bin", "horizon")
		env := childEnv(home, t.Access)
		if t.Session == "" {
			out := &cappedWriter{limit: 4096}
			code, err := Process(ctx, binary, []string{"--home", home, "sessions", "create"}, env, t.Workspace, "", out)
			if err != nil {
				return Outcome{ExitCode: code, Err: err}
			}
			t.Session = strings.TrimSpace(out.String())
			if !sessionID.MatchString(t.Session) {
				return Outcome{Err: errors.New("invalid session ID")}
			}
			if err = set(t.Session); err != nil {
				return Outcome{Err: err}
			}
		}
		// Parse incrementally; never retain opaque API payloads in daemon state.
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		reader, writer := io.Pipe()
		outcome := make(chan Outcome, 1)
		go func() {
			defer reader.Close()
			var result Outcome
			completed := false
			scan := bufio.NewScanner(reader)
			scan.Buffer(make([]byte, 4096), 4*1024*1024)
			for scan.Scan() {
				var e eventstream.Event
				if json.Unmarshal(scan.Bytes(), &e) != nil || e.Type == "" {
					result.Err = errors.New("invalid Horizon event")
					cancel()
					break
				}
				if e.TurnID != nil {
					result.Turn = *e.TurnID
				}
				switch e.Type {
				case "turn_completed":
					if completed {
						result.Err = errors.New("duplicate completion")
						cancel()
						break
					}
					data, _ := json.Marshal(e.Data)
					var payload struct {
						Text string `json:"text"`
					}
					if json.Unmarshal(data, &payload) != nil || len(payload.Text) > 2*1024*1024 {
						result.Err = errors.New("invalid final response")
						cancel()
						break
					}
					result.Text = payload.Text
					completed = true
				case "turn_started", "model_request_started", "tool_started", "tool_completed", "compaction_started":
					stage(result.Turn, e.Type)
				}
			}
			if result.Err == nil {
				result.Err = scan.Err()
			}
			if result.Err == nil && !completed {
				result.Err = errors.New("missing turn completion")
			}
			outcome <- result
		}()
		code, runErr := Process(ctx, binary, []string{"--home", home, "resume", "--session", t.Session, "--mode", "jsonl", "--access", t.Access}, env, t.Workspace, t.Message, writer)
		writer.Close()
		result := <-outcome
		result.ExitCode = code
		if runErr != nil {
			result.Err = runErr
		}
		return result
	}
}
