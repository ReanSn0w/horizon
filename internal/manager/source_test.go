package manager

import (
	"context"
	"fmt"
	"github.com/ReanSn0w/horizon/internal/config"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func fixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{{"init", "--quiet", dir}, {"-C", dir, "config", "user.email", "test@example.test"}, {"-C", dir, "config", "user.name", "Test"}} {
		if _, err := command(ctx, "", "git", args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "plugins", "manager"), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile("../../plugins/manager/plugin.json")
	_ = os.WriteFile(filepath.Join(dir, "plugins", "manager", "plugin.json"), data, 0600)
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test/fixture\ngo 1.20\n"), 0600)
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "fixture"}, {"branch", "-M", "feature/install"}, {"tag", "v1"}} {
		if _, err := command(ctx, dir, "git", args...); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
func TestPrepareSourcesAndProtectCheckout(t *testing.T) {
	repo := fixtureRepo(t)
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v1","draft":false,"prerelease":false}`)
	}))
	defer server.Close()
	e := engine()
	e.repository = repo
	e.releaseURL = server.URL
	for _, source := range []config.Source{{Type: "branch", Branch: "feature/install"}, {Type: "release"}} {
		stage := t.TempDir()
		commit, _, _, err := e.prepare(ctx, stage, source)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(stage, "source")
		if err := e.checkCheckout(ctx, path, commit); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(path, "local.txt"), []byte("keep"), 0600)
		if e.checkCheckout(ctx, path, commit) == nil {
			t.Fatal("dirty checkout accepted")
		}
	}
	if _, _, _, err := e.prepare(ctx, t.TempDir(), config.Source{Type: "branch", Branch: "absent"}); err == nil {
		t.Fatal("missing branch accepted")
	}
}
func TestReleaseFailureDoesNotFallback(t *testing.T) {
	for _, status := range []int{404, 403, 500} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		e := engine()
		e.releaseURL = server.URL
		if _, err := e.resolveRef(context.Background(), config.Source{Type: "release"}); err == nil {
			t.Fatal(status)
		}
		server.Close()
	}
}
