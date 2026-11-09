package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ReanSn0w/horizon/internal/config"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const repositoryURL = "https://github.com/ReanSn0w/horizon.git"
const latestReleaseURL = "https://api.github.com/repos/ReanSn0w/horizon/releases/latest"

type Engine struct {
	repository, releaseURL string
	http                   *http.Client
	build                  func(context.Context, string, string, string) error
	afterReplace           func(int) error
}

func engine() *Engine {
	return &Engine{repository: repositoryURL, releaseURL: latestReleaseURL, http: &http.Client{Timeout: 20 * time.Second}}
}

// commands have a bounded lifetime and cancellation terminates their process group.
func command(ctx context.Context, dir, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	c := exec.CommandContext(ctx, name, args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GIT_TERMINAL_PROMPT=0")
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	c.WaitDelay = 2 * time.Second
	data, err := c.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("%s %s failed (check tool version, network and repository access)", name, args[0])
	}
	return strings.TrimSpace(string(data)), nil
}
func checkTools(ctx context.Context) error {
	for _, name := range []string{"git", "go"} {
		if _, err := exec.LookPath(name); err != nil {
			return fmt.Errorf("%s must be installed and available in PATH", name)
		}
	}
	_, err := command(ctx, "", "go", "version")
	return err
}
func (e *Engine) resolveRef(ctx context.Context, source config.Source) (string, error) {
	if err := source.Validate(); err != nil {
		return "", err
	}
	if source.Type == "branch" {
		ref := "refs/heads/" + source.Branch
		if _, err := command(ctx, "", "git", "check-ref-format", ref); err != nil {
			return "", fmt.Errorf("invalid branch name")
		}
		return ref, nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", e.releaseURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := e.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot resolve latest release")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("latest stable release unavailable (HTTP %d); choose an explicit branch if appropriate", resp.StatusCode)
	}
	var r struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil || r.Draft || r.Prerelease || r.Tag == "" {
		return "", fmt.Errorf("invalid stable release response")
	}
	ref := "refs/tags/" + r.Tag
	if _, err := command(ctx, "", "git", "check-ref-format", ref); err != nil {
		return "", fmt.Errorf("invalid release tag")
	}
	return ref, nil
}
func (e *Engine) checkCheckout(ctx context.Context, path, expectedCommit string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if expectedCommit != "" {
			return fmt.Errorf("installed source is missing; restore it before updating")
		}
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("source must be a managed directory, not a symlink")
	}
	origin, err := command(ctx, path, "git", "remote", "get-url", "origin")
	if err != nil || origin != e.repository {
		return fmt.Errorf("source has an unexpected origin; it will not be overwritten")
	}
	top, err := command(ctx, path, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	resolved, _ := filepath.EvalSymlinks(path)
	if top != resolved {
		return fmt.Errorf("source is not a standalone checkout")
	}
	status, err := command(ctx, path, "git", "status", "--porcelain", "--untracked-files=all", "--ignored")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("source has local or ignored files; save them outside source before updating")
	}
	if expectedCommit != "" {
		head, err := command(ctx, path, "git", "rev-parse", "HEAD")
		if err != nil || head != expectedCommit {
			return fmt.Errorf("source revision differs from manager state")
		}
	}
	return nil
}
func (e *Engine) prepare(ctx context.Context, staging string, source config.Source) (string, string, map[string]Plugin, error) {
	ref, err := e.resolveRef(ctx, source)
	if err != nil {
		return "", "", nil, err
	}
	dest := filepath.Join(staging, "source")
	if err = os.Mkdir(dest, 0700); err != nil {
		return "", "", nil, err
	}
	if _, err = command(ctx, "", "git", "init", "--quiet", dest); err != nil {
		return "", "", nil, err
	}
	if _, err = command(ctx, dest, "git", "remote", "add", "origin", e.repository); err != nil {
		return "", "", nil, err
	}
	if _, err = command(ctx, dest, "git", "fetch", "--depth=1", "origin", ref); err != nil {
		return "", "", nil, err
	}
	commit, err := command(ctx, dest, "git", "rev-parse", "FETCH_HEAD^{commit}")
	if err != nil || !commitPattern.MatchString(commit) {
		return "", "", nil, fmt.Errorf("cannot resolve source commit")
	}
	if _, err = command(ctx, dest, "git", "checkout", "--quiet", "--detach", commit); err != nil {
		return "", "", nil, err
	}
	catalog, err := Catalog(dest)
	if err != nil {
		return "", "", nil, err
	}
	if err = checkGoVersion(ctx, dest); err != nil {
		return "", "", nil, err
	}
	return commit, ref, catalog, nil
}
func versionParts(s string) ([3]int, error) {
	var v [3]int
	s = strings.TrimPrefix(s, "go")
	p := strings.Split(s, ".")
	if len(p) < 2 || len(p) > 3 {
		return v, fmt.Errorf("invalid Go version")
	}
	for i, part := range p {
		n, err := strconv.Atoi(part)
		if err != nil {
			return v, fmt.Errorf("stable Go version required")
		}
		v[i] = n
	}
	return v, nil
}
func checkGoVersion(ctx context.Context, source string) error {
	data, err := os.ReadFile(filepath.Join(source, "go.mod"))
	if err != nil {
		return err
	}
	required := ""
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "go" {
			required = f[1]
			break
		}
	}
	want, err := versionParts(required)
	if err != nil {
		return fmt.Errorf("source go.mod has no supported Go requirement")
	}
	installed, err := command(ctx, "", "go", "env", "GOVERSION")
	if err != nil {
		return err
	}
	got, err := versionParts(installed)
	if err != nil {
		return err
	}
	for i := range want {
		if got[i] > want[i] {
			return nil
		}
		if got[i] < want[i] {
			return fmt.Errorf("source requires Go %s; installed %s (automatic toolchain download is disabled)", required, installed)
		}
	}
	return nil
}
func (e *Engine) buildBinary(ctx context.Context, source, pkg, out string) error {
	if e.build != nil {
		return e.build(ctx, source, pkg, out)
	}
	_, err := command(ctx, source, "go", "build", "-o", out, pkg)
	return err
}
