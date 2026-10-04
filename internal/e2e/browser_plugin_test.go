package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/session"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func TestBuiltBrowserPluginTurnAndCleanup(t *testing.T) {
	binary := buildBinary(t)
	home, workspace := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "plugins"), 0700); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(home, "plugins", "horizon-browser"), "./plugins/browser")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build browser plugin: %v %s", err, output)
	}
	var mu sync.Mutex
	responses, created, stopped, active := 0, 0, 0, false
	var metadata map[string]string
	var endpoint string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cdp" {
			serveBrowserCDP(t, w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v4/browsers") {
			if r.Header.Get("X-Browser-Use-API-Key") != "browser-secret" {
				t.Error("missing Browser Use token")
			}
			mu.Lock()
			defer mu.Unlock()
			switch {
			case r.Method == "GET" && r.URL.Path == "/api/v4/browsers":
				items := []any{}
				if active {
					items = append(items, map[string]any{"id": "browser-1", "status": "active", "cdpUrl": strings.Replace(endpoint, "http://", "ws://", 1) + "/cdp", "metadata": metadata, "startedAt": "2026-01-01T00:00:00Z", "timeoutAt": "2026-01-01T00:10:00Z"})
				}
				json.NewEncoder(w).Encode(map[string]any{"items": items, "totalItems": len(items), "pageNumber": 1, "pageSize": 100})
			case r.Method == "POST" && r.URL.Path == "/api/v4/browsers":
				created++
				var input struct {
					Metadata map[string]string `json:"metadata"`
				}
				_ = json.NewDecoder(r.Body).Decode(&input)
				metadata = input.Metadata
				active = true
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(map[string]any{"id": "browser-1", "status": "active", "cdpUrl": strings.Replace(endpoint, "http://", "ws://", 1) + "/cdp"})
			case r.Method == "PATCH" && r.URL.Path == "/api/v4/browsers/browser-1":
				stopped++
				active = false
				json.NewEncoder(w).Encode(map[string]any{"id": "browser-1", "status": "stopped"})
			case r.Method == "GET" && r.URL.Path == "/api/v4/browsers/browser-1":
				status := "stopped"
				if active {
					status = "active"
				}
				json.NewEncoder(w).Encode(map[string]any{"id": "browser-1", "status": status, "metadata": metadata})
			default:
				http.NotFound(w, r)
			}
			return
		}
		mu.Lock()
		responses++
		call := responses
		mu.Unlock()
		var modelRequest struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&modelRequest); err != nil {
			t.Error(err)
		}
		if call == 5 {
			for _, tool := range modelRequest.Tools {
				if strings.HasPrefix(tool.Name, "browser__") {
					t.Error("disabled browser tool still exposed")
				}
			}
		}
		switch call {
		case 1:
			writeCompleted(w, call, []json.RawMessage{json.RawMessage(`{"type":"function_call","call_id":"nav","name":"browser__navigate","arguments":"{\"url\":\"https://example.com\"}"}`)})
		case 2:
			writeCompleted(w, call, []json.RawMessage{json.RawMessage(`{"type":"function_call","call_id":"snap","name":"browser__snapshot","arguments":"{}"}`)})
		default:
			writeCompleted(w, call, message("done"))
		}
	}))
	defer server.Close()
	endpoint = server.URL
	writeConfig(t, home, server.URL)
	path := filepath.Join(home, "config.yaml")
	base, _ := os.ReadFile(path)
	settings := fmt.Sprintf("\nagent_plugins: [browser]\nplugins:\n  browser:\n    api_key: browser-secret\n    api_url: %s/api/v4\n", server.URL)
	if err := os.WriteFile(path, append(base, []byte(settings)...), 0600); err != nil {
		t.Fatal(err)
	}
	out, diagnostics, err := run(binary, workspace, "", "--home", home, "resume", "--access", "full", "--mode", "plain", "-m", "browse")
	mu.Lock()
	gotCreated, gotStopped, gotResponses := created, stopped, responses
	mu.Unlock()
	if err != nil || out != "done\n" || gotCreated != 1 || gotStopped != 1 || gotResponses != 3 {
		t.Fatalf("run out=%q err=%v created=%d stopped=%d responses=%d stderr=%q", out, err, gotCreated, gotStopped, gotResponses, diagnostics)
	}
	listed, diagnostics, err := run(binary, workspace, "", "--home", home, "browser", "list")
	if err != nil || strings.Contains(listed, "browser-1\t2026") {
		t.Fatalf("list out=%q err=%v stderr=%q", listed, err, diagnostics)
	}
	out, diagnostics, err = run(binary, workspace, "", "--home", home, "resume", "--access", "full", "--mode", "plain", "-m", "no URL")
	mu.Lock()
	gotCreated, gotStopped = created, stopped
	mu.Unlock()
	if err != nil || out != "done\n" || gotCreated != 1 || gotStopped != 1 {
		t.Fatalf("no URL out=%q err=%v created=%d stopped=%d stderr=%q", out, err, gotCreated, gotStopped, diagnostics)
	}
	if err := os.WriteFile(path, append(base, []byte(strings.Replace(settings, "agent_plugins: [browser]", "agent_plugins: []", 1))...), 0600); err != nil {
		t.Fatal(err)
	}
	out, diagnostics, err = run(binary, workspace, "", "--home", home, "resume", "--access", "full", "--mode", "plain", "-m", "disabled")
	mu.Lock()
	gotCreated, gotStopped = created, stopped
	mu.Unlock()
	if err != nil || out != "done\n" || gotCreated != 1 || gotStopped != 1 {
		t.Fatalf("disabled out=%q err=%v created=%d stopped=%d stderr=%q", out, err, gotCreated, gotStopped, diagnostics)
	}
	mu.Lock()
	active = true // simulate an orphan after process termination
	orphanMetadata := map[string]string{}
	for key, value := range metadata {
		orphanMetadata[key] = value
	}
	mu.Unlock()
	orphanRecord, _ := json.Marshal(map[string]string{
		"id": "browser-1", "cdp_url": "ws://private.example/secret", "session_id": orphanMetadata["horizon_session"],
		"turn_id": orphanMetadata["horizon_turn"], "workspace_id": orphanMetadata["horizon_workspace"],
	})
	orphanPath := filepath.Join(home, "browser", "turn-"+orphanMetadata["horizon_turn"]+".json")
	if err := os.WriteFile(orphanPath, orphanRecord, 0600); err != nil {
		t.Fatal(err)
	}
	listed, diagnostics, err = run(binary, workspace, "", "--home", home, "browser", "list")
	if err != nil || !strings.Contains(listed, "оставшийся после сбоя") || strings.Contains(listed, "ws://private") {
		t.Fatalf("orphan list=%q err=%v stderr=%q", listed, err, diagnostics)
	}
	store := session.NewStore(home)
	resolved, err := store.ResolveWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.List(resolved, false)
	if err != nil || len(items) != 1 {
		t.Fatalf("sessions=%d err=%v", len(items), err)
	}
	locked, err := store.LockSession(resolved, items[0].Session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	activeTurnID := "active-turn"
	if err := locked.StartTurn(session.Turn{ID: activeTurnID, Status: session.StatusActive, StartedAt: time.Now(), Model: items[0].Session.Turns[0].Model}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(orphanPath); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	metadata["horizon_turn"] = activeTurnID
	mu.Unlock()
	_, diagnostics, err = run(binary, workspace, "", "--home", home, "browser", "close", "--stale")
	mu.Lock()
	gotStopped = stopped
	mu.Unlock()
	if err != nil || gotStopped != 1 {
		t.Fatalf("active close err=%v stopped=%d stderr=%q", err, gotStopped, diagnostics)
	}
	if err := locked.CompleteTurn(activeTurnID, session.StatusCompleted, nil, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = locked.Close()
	mu.Lock()
	metadata["horizon_turn"] = orphanMetadata["horizon_turn"]
	mu.Unlock()
	if err := os.WriteFile(orphanPath, orphanRecord, 0600); err != nil {
		t.Fatal(err)
	}
	_, diagnostics, err = run(binary, workspace, "", "--home", home, "browser", "close", "--stale")
	mu.Lock()
	gotStopped, isActive := stopped, active
	mu.Unlock()
	if err != nil || gotStopped != 2 || isActive {
		t.Fatalf("stale close err=%v stopped=%d active=%t stderr=%q", err, gotStopped, isActive, diagnostics)
	}
	mu.Lock()
	active = true
	mu.Unlock()
	_, diagnostics, err = run(binary, workspace, "", "--home", home, "browser", "close", "browser-1")
	mu.Lock()
	gotStopped, isActive = stopped, active
	mu.Unlock()
	if err != nil || gotStopped != 3 || isActive {
		t.Fatalf("explicit close err=%v stopped=%d active=%t stderr=%q", err, gotStopped, isActive, diagnostics)
	}
	unavailable := strings.Replace(settings, server.URL+"/api/v4", "http://127.0.0.1:1/api/v4", 1)
	if err := os.WriteFile(path, append(base, []byte(strings.Replace(unavailable, "agent_plugins: [browser]", "agent_plugins: []", 1))...), 0600); err != nil {
		t.Fatal(err)
	}
	listed, diagnostics, err = run(binary, workspace, "", "--home", home, "browser", "list")
	if err == nil || listed != "" || !strings.Contains(diagnostics, "состояние не проверено") {
		t.Fatalf("unavailable list=%q err=%v stderr=%q", listed, err, diagnostics)
	}
}

func serveBrowserCDP(t *testing.T, w http.ResponseWriter, r *http.Request) {
	conn, _, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		t.Error(err)
		return
	}
	defer conn.Close()
	for {
		data, err := wsutil.ReadClientText(conn)
		if err != nil {
			return
		}
		var call struct {
			ID        int             `json:"id"`
			Method    string          `json:"method"`
			SessionID string          `json:"sessionId"`
			Params    json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(data, &call); err != nil {
			t.Error(err)
			return
		}
		result := map[string]any{}
		switch call.Method {
		case "Target.createTarget":
			result["targetId"] = "tab-1"
		case "Target.attachToTarget":
			result["sessionId"] = "cdp-session-1"
		case "Runtime.evaluate":
			var params struct {
				Expression string `json:"expression"`
			}
			_ = json.Unmarshal(call.Params, &params)
			if params.Expression == "self" {
				result["result"] = map[string]any{"type": "object", "className": "Window"}
			} else {
				result["result"] = map[string]any{"type": "object", "value": map[string]any{"url": "https://example.com", "title": "Example", "text": "Hello", "elements": []any{}, "truncated": false}}
			}
		case "Page.navigate":
			result["frameId"] = "frame-1"
			result["loaderId"] = "loader-2"
		case "Page.getFrameTree":
			result["frameTree"] = map[string]any{"frame": map[string]any{"id": "frame-1", "loaderId": "loader-1", "url": "about:blank", "securityOrigin": "://", "mimeType": "text/html"}}
		case "DOM.getDocument":
			result["root"] = map[string]any{"nodeId": 1, "backendNodeId": 1, "nodeType": 9, "nodeName": "#document", "localName": "", "nodeValue": "", "childNodeCount": 0}
		}
		response, _ := json.Marshal(map[string]any{"id": call.ID, "sessionId": call.SessionID, "result": result})
		if err := wsutil.WriteServerText(conn, response); err != nil {
			return
		}
		if call.Method == "Page.navigate" {
			for _, event := range []map[string]any{
				{"method": "Page.frameNavigated", "sessionId": call.SessionID, "params": map[string]any{"frame": map[string]any{"id": "frame-1", "loaderId": "loader-2", "url": "https://example.com", "securityOrigin": "https://example.com", "mimeType": "text/html"}}},
				{"method": "Page.lifecycleEvent", "sessionId": call.SessionID, "params": map[string]any{"frameId": "frame-1", "loaderId": "loader-2", "name": "init", "timestamp": 1}},
				{"method": "Page.loadEventFired", "sessionId": call.SessionID, "params": map[string]any{"timestamp": 2}},
			} {
				encoded, _ := json.Marshal(event)
				if err := wsutil.WriteServerText(conn, encoded); err != nil {
					return
				}
			}
		}
	}
}

func TestBuiltBrowserPluginFinalizesFailedAndCancelledTurns(t *testing.T) {
	binary := buildBinary(t)
	for _, mode := range []string{"failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			home, workspace := t.TempDir(), t.TempDir()
			if err := os.Mkdir(filepath.Join(home, "plugins"), 0700); err != nil {
				t.Fatal(err)
			}
			build := exec.Command("go", "build", "-o", filepath.Join(home, "plugins", "horizon-browser"), "./plugins/browser")
			build.Dir = "../.."
			if output, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build browser plugin: %v %s", err, output)
			}
			cdp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveBrowserCDP(t, w, r) }))
			defer cdp.Close()
			var mu sync.Mutex
			stopped := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case "GET":
					json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "totalItems": 0, "pageNumber": 1, "pageSize": 100})
				case "POST":
					w.WriteHeader(http.StatusCreated)
					json.NewEncoder(w).Encode(map[string]any{"id": "browser-1", "status": "active", "cdpUrl": strings.Replace(cdp.URL, "http://", "ws://", 1)})
				case "PATCH":
					mu.Lock()
					stopped++
					mu.Unlock()
					json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})
				}
			}))
			defer api.Close()
			secondStarted := make(chan struct{})
			releaseSecond := make(chan struct{})
			calls := 0
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				calls++
				call := calls
				mu.Unlock()
				if call == 1 {
					writeCompleted(w, call, []json.RawMessage{json.RawMessage(`{"type":"function_call","call_id":"nav","name":"browser__navigate","arguments":"{\"url\":\"https://example.com\"}"}`)})
					return
				}
				if mode == "cancel" {
					close(secondStarted)
					<-releaseSecond
					return
				}
				http.Error(w, "model failed", http.StatusBadRequest)
			}))
			defer model.Close()
			writeConfig(t, home, model.URL)
			path := filepath.Join(home, "config.yaml")
			base, _ := os.ReadFile(path)
			settings := fmt.Sprintf("\nagent_plugins: [browser]\nplugins:\n  browser:\n    api_key: test\n    api_url: %s\n", api.URL)
			if err := os.WriteFile(path, append(base, []byte(settings)...), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(binary, "--home", home, "resume", "--access", "full", "--mode", "plain", "-m", "browse")
			cmd.Dir = workspace
			if mode == "cancel" {
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				select {
				case <-secondStarted:
				case <-time.After(10 * time.Second):
					_ = cmd.Process.Kill()
					t.Fatal("second model request did not start")
				}
				if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
					t.Fatal(err)
				}
				_ = cmd.Wait()
				close(releaseSecond)
			} else {
				_ = cmd.Run()
			}
			mu.Lock()
			gotStopped := stopped
			mu.Unlock()
			if gotStopped != 1 {
				t.Fatalf("%s: browser stops=%d", mode, gotStopped)
			}
		})
	}
}
