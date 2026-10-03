package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/radutopala/macuse/internal/buildinfo"
	"github.com/radutopala/macuse/internal/clilink"
	"github.com/radutopala/macuse/internal/launchagent"
	"github.com/radutopala/macuse/internal/proto"
	"github.com/radutopala/macuse/internal/server"
)

// bundled makes the app run from a macuse.app release, with a feed that
// offers the next version and codesign and ditto that accept it.
func (s *AppSuite) bundled() (bundle string, starts *[][]string) {
	dir, err := filepath.EvalSymlinks(s.T().TempDir())
	s.Require().NoError(err)
	bundle = filepath.Join(dir, "macuse.app")
	exe := filepath.Join(bundle, "Contents", "MacOS", "macuse")
	s.Require().NoError(os.MkdirAll(filepath.Dir(exe), 0o755))
	s.Require().NoError(os.WriteFile(exe, []byte("v1"), 0o755))
	s.app.executable = func() (string, error) { return exe, nil }
	s.app.version = "v2026.10.1"
	s.app.cliFallback = filepath.Join(dir, "bin", "macuse")
	s.app.pid = func() int { return 4242 }

	zip := []byte("zip")
	sum := sha256.Sum256(zip)
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/checksums.txt" {
			_, _ = w.Write([]byte(hex.EncodeToString(sum[:]) + "  macuse_2026.10.2_macos.zip\n"))
			return
		}
		_, _ = w.Write(zip)
	}))
	s.T().Cleanup(feed.Close)
	s.app.updateFeed = feed.URL
	s.app.updateEvery = time.Hour
	// The login shell's PATH has ~/.local/bin, which the CLI links in.
	bin := filepath.Join(s.home, ".local", "bin")
	s.Require().NoError(os.MkdirAll(bin, 0o755))
	s.app.getenv = func(string) string { return "" }
	s.app.output = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "/bin/zsh":
			return []byte("__macuse-path__/usr/bin:" + bin + "__macuse-path__"), nil
		case name == "codesign" && args[0] == "-dv":
			return []byte("TeamIdentifier=TEAM\n"), nil
		case name == "ditto":
			next := filepath.Join(args[len(args)-1], "macuse.app", "Contents", "MacOS")
			if err := os.MkdirAll(next, 0o755); err != nil {
				return nil, err
			}
			return nil, os.WriteFile(filepath.Join(next, "macuse"), []byte("v2"), 0o755)
		}
		return nil, nil
	}
	starts = &[][]string{}
	s.app.start = func(name string, args ...string) error {
		*starts = append(*starts, append([]string{name}, args...))
		return s.fail["start"]
	}
	return bundle, starts
}

// popup is a started serve's popup API.
type popup struct {
	s    *AppSuite
	base string
	key  []string
}

func (s *AppSuite) popup(sv *serving) popup {
	page, err := url.Parse(<-sv.page)
	s.Require().NoError(err)
	return popup{s: s, base: "http://" + page.Host, key: []string{server.HeaderUIKey, page.Query().Get("key")}}
}

func (p popup) state() server.UIState {
	var st server.UIState
	p.s.Require().NoError(json.Unmarshal([]byte(readAll(p.s.request(http.MethodGet, p.base+"/ui/api/state", "", p.key...))), &st))
	return st
}

func (p popup) post(path string) int {
	res := p.s.request(http.MethodPost, p.base+path, "", p.key...)
	_ = readAll(res)
	return res.StatusCode
}

func (s *AppSuite) updateAndInstall(bundle string) *serving {
	sv := s.startServe()
	sv.runner.On("Permissions").Return(proto.Permissions{})
	p := s.popup(sv)
	require.Eventually(s.T(), func() bool {
		up := p.state().Update
		return up != nil && up.Available && up.Latest == "2026.10.2"
	}, 5*time.Second, 5*time.Millisecond)

	require.Equal(s.T(), "missing", p.state().CLI)
	require.Equal(s.T(), http.StatusNoContent, p.post("/ui/api/cli"))
	require.Equal(s.T(), "installed", p.state().CLI)
	require.Equal(s.T(), http.StatusOK, p.post("/ui/api/update/check"))

	require.Equal(s.T(), http.StatusNoContent, p.post("/ui/api/update/install"))
	exe, err := os.ReadFile(filepath.Join(bundle, "Contents", "MacOS", "macuse"))
	require.NoError(s.T(), err)
	require.Equal(s.T(), "v2", string(exe), "the new app is in place")
	return sv
}

func (s *AppSuite) TestServeUpdatesUnderLaunchd() {
	bundle, starts := s.bundled()
	s.app.getenv = func(k string) string {
		if k == "XPC_SERVICE_NAME" {
			return buildinfo.BundleID
		}
		return ""
	}
	// launchd points stderr at the log.
	stderr, err := openAppend(s.paths.Log)
	require.NoError(s.T(), err)
	defer func() { _ = stderr.Close() }()
	s.app.stderr = stderr
	sv := s.updateAndInstall(bundle)
	require.Equal(s.T(), 1, <-sv.done, "launchd restarts an agent that fails")
	require.Empty(s.T(), *starts)
	log, err := os.ReadFile(s.paths.Log)
	require.NoError(s.T(), err)
	require.Contains(s.T(), string(log), "macuse: restarting into the update")
	require.Equal(s.T(), 1, strings.Count(string(log), "macuse serving"), "logged once, not to stderr and the log")
}

func (s *AppSuite) TestServeUpdatesAndReopens() {
	bundle, starts := s.bundled()
	sv := s.updateAndInstall(bundle)
	require.Equal(s.T(), 0, <-sv.done)
	require.Equal(s.T(), [][]string{{"/bin/sh", "-c", relaunch, "sh", "4242", bundle}}, *starts)
	log, err := os.ReadFile(s.paths.Log)
	require.NoError(s.T(), err)
	require.Contains(s.T(), string(log), "macuse serving", "an app opened directly logs to the file too")
	require.Contains(s.T(), s.stderr.String(), "macuse serving")
}

func (s *AppSuite) TestServeReopenFails() {
	bundle, _ := s.bundled()
	s.fail["start"] = errors.New("fork failed")
	sv := s.updateAndInstall(bundle)
	require.Equal(s.T(), 0, <-sv.done)
	require.Contains(s.T(), s.stderr.String(), "fork failed")
}

func (s *AppSuite) TestServeDevBuildDoesntUpdate() {
	s.bundled()
	s.app.version = "v2026.10.1-3-gabc"
	sv := s.startServe()
	sv.runner.On("Permissions").Return(proto.Permissions{})
	p := s.popup(sv)
	require.Nil(s.T(), p.state().Update)
	require.Equal(s.T(), http.StatusNotImplemented, p.post("/ui/api/update/install"))
	sv.cancel()
	require.Equal(s.T(), 0, <-sv.done)
}

func (s *AppSuite) TestBundlePath() {
	require.Equal(s.T(), "/Applications/macuse.app", bundlePath("/Applications/macuse.app/Contents/MacOS/macuse"))
	require.Empty(s.T(), bundlePath("/usr/local/bin/macuse"))
}

func (s *AppSuite) TestRunHelpers() {
	require.NoError(s.T(), startDetached("true"))
	require.Error(s.T(), startDetached(filepath.Join(s.home, "missing")))
	out, err := runOutput(context.Background(), "sh", "-c", "echo hi; echo err >&2")
	require.NoError(s.T(), err)
	require.Equal(s.T(), "hi\nerr\n", string(out))
}

func (s *AppSuite) TestHostCLI() {
	require.Empty(s.T(), host{}.CLI())

	target := filepath.Join(s.home, "macuse")
	require.NoError(s.T(), os.WriteFile(target, nil, 0o755))
	bin := filepath.Join(s.home, "bin")
	s.Require().NoError(os.MkdirAll(bin, 0o755))
	h := host{link: &clilink.Link{Target: target, Dirs: func() []string { return []string{bin} }, Writable: []string{bin}}, agent: launchagent.Agent{}}
	require.Equal(s.T(), "missing", h.CLI())
	require.NoError(s.T(), h.InstallCLI())
	require.Equal(s.T(), "installed", h.CLI())
}
