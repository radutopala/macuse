package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/radutopala/macuse/internal/auth"
	"github.com/radutopala/macuse/internal/client"
	"github.com/radutopala/macuse/internal/config"
	"github.com/radutopala/macuse/internal/fsmigrate"
)

// failReader fails every read; okFirst lets the first n bytes through.
type failReader struct{ okFirst int }

func (r *failReader) Read(p []byte) (int, error) {
	if r.okFirst <= 0 {
		return 0, errors.New("no entropy")
	}
	n := min(len(p), r.okFirst)
	r.okFirst -= n
	return n, nil
}

type AppSuite struct {
	suite.Suite
	home     string
	paths    config.Paths
	stdout   *bytes.Buffer
	stderr   *bytes.Buffer
	commands [][]string
	fail     map[string]error
	app      *app
}

func TestAppSuite(t *testing.T) {
	suite.Run(t, new(AppSuite))
}

func (s *AppSuite) SetupTest() {
	s.home = s.T().TempDir()
	s.paths = config.NewPaths(s.home)
	s.stdout = &bytes.Buffer{}
	s.stderr = &bytes.Buffer{}
	s.commands = nil
	s.fail = map[string]error{}
	s.app = newApp()
	s.app.stdout = s.stdout
	s.app.stderr = s.stderr
	s.app.getenv = func(string) string { return "" }
	s.app.exists = func(string) bool { return false }
	s.app.home = func() (string, error) { return s.home, nil }
	s.app.executable = func() (string, error) { return "/opt/macuse/macuse", nil }
	s.app.uid = func() int { return 501 }
	s.app.command = func(name string, args ...string) error {
		s.commands = append(s.commands, append([]string{name}, args...))
		return s.fail[args[0]]
	}
}

func (s *AppSuite) TestPathsMigrationFails() {
	require.NoError(s.T(), os.MkdirAll(s.paths.Dir, 0o700))
	require.NoError(s.T(), os.WriteFile(filepath.Join(s.paths.Dir, "fs_migrations"), []byte("x"), 0o600))
	require.Equal(s.T(), 1, s.run("token"))
	require.Contains(s.T(), s.stderr.String(), "fs_migrations")
}

func (s *AppSuite) run(args ...string) int {
	return s.app.run(context.Background(), args)
}

// writeToken puts tok in the Mac's token file.
func (s *AppSuite) writeToken(tok string) {
	require.NoError(s.T(), os.MkdirAll(s.paths.Dir, 0o700))
	require.NoError(s.T(), os.WriteFile(s.paths.Token, []byte(tok+"\n"), 0o600))
}

func (s *AppSuite) TestNewApp() {
	a := newApp()
	require.True(s.T(), a.exists("/"))
	require.False(s.T(), a.exists(filepath.Join(s.home, "missing")))
}

func (s *AppSuite) TestRunCommand() {
	require.NoError(s.T(), runCommand("true"))
	require.EqualError(s.T(), runCommand("sh", "-c", "echo boom; exit 3"), "exit status 3: boom")
}

func (s *AppSuite) TestVersion() {
	require.Equal(s.T(), 0, s.run("version"))
	require.Equal(s.T(), "dev\n", s.stdout.String())
}

func (s *AppSuite) TestRootHelp() {
	s.app.executable = func() (string, error) { return "", errors.New("no exe") }
	require.Equal(s.T(), 0, s.run())
	require.Contains(s.T(), s.stdout.String(), "Usage:")
}

func (s *AppSuite) TestRootInBundleServes() {
	s.app.executable = func() (string, error) { return "/Applications/macuse.app/Contents/MacOS/macuse", nil }
	s.app.home = func() (string, error) { return "", errors.New("no home") }
	require.Equal(s.T(), 1, s.run())
	require.Equal(s.T(), "macuse: no home\n", s.stderr.String())
}

func (s *AppSuite) TestBadArgs() {
	require.Equal(s.T(), 1, s.run("nope"))
	require.Contains(s.T(), s.stderr.String(), "macuse: unknown command")
}

func (s *AppSuite) TestToken() {
	require.Equal(s.T(), 0, s.run("token"))
	tok, err := auth.Read(s.paths.Token)
	require.NoError(s.T(), err)
	require.Equal(s.T(), tok+"\n", s.stdout.String())
}

func (s *AppSuite) TestTokenFailures() {
	tests := []struct {
		name  string
		setup func()
		want  string
	}{
		{"home", func() { s.app.home = func() (string, error) { return "", errors.New("no home") } }, "no home"},
		{"entropy", func() { s.app.tokens = auth.Tokens{Rand: &failReader{}} }, "token: no entropy"},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			tc.setup()
			require.Equal(s.T(), 1, s.run("token"))
			require.Contains(s.T(), s.stderr.String(), tc.want)
		})
	}
}

func (s *AppSuite) TestServiceInstallUninstall() {
	require.Equal(s.T(), 0, s.run("service", "install"))
	require.FileExists(s.T(), s.paths.LaunchAgent)
	plist, err := os.ReadFile(s.paths.LaunchAgent)
	require.NoError(s.T(), err)
	require.Contains(s.T(), string(plist), "<string>/opt/macuse/macuse</string>")
	require.Contains(s.T(), string(plist), "<string>"+s.paths.Log+"</string>")
	require.Equal(s.T(), [][]string{
		{"launchctl", "bootout", "gui/501/io.github.radutopala.macuse"},
		{"launchctl", "bootstrap", "gui/501", s.paths.LaunchAgent},
	}, s.commands)

	require.Equal(s.T(), 0, s.run("service", "uninstall"))
	require.NoFileExists(s.T(), s.paths.LaunchAgent)
	require.Equal(s.T(), "macuse runs now and at every login.\nmacuse stopped and won't start at login.\n", s.stdout.String())
}

func (s *AppSuite) TestServiceInstallResolvesSymlink() {
	real := filepath.Join(s.home, "macuse.app", "Contents", "MacOS", "macuse")
	require.NoError(s.T(), os.MkdirAll(filepath.Dir(real), 0o755))
	require.NoError(s.T(), os.WriteFile(real, nil, 0o755))
	link := filepath.Join(s.home, "macuse")
	require.NoError(s.T(), os.Symlink(real, link))
	s.app.executable = func() (string, error) { return link, nil }
	require.Equal(s.T(), 0, s.run("service", "install"))
	plist, err := os.ReadFile(s.paths.LaunchAgent)
	require.NoError(s.T(), err)
	resolved, err := filepath.EvalSymlinks(real)
	require.NoError(s.T(), err)
	require.Contains(s.T(), string(plist), "<string>"+resolved+"</string>")
}

func (s *AppSuite) TestServiceFailures() {
	tests := []struct {
		name  string
		args  []string
		setup func()
		want  string
	}{
		{"home", []string{"install"}, func() { s.app.home = func() (string, error) { return "", errors.New("no home") } }, "no home"},
		{"executable", []string{"uninstall"}, func() { s.app.executable = func() (string, error) { return "", errors.New("no exe") } }, "no exe"},
		{"bootstrap", []string{"install"}, func() { s.fail["bootstrap"] = errors.New("denied") }, "launchctl bootstrap: denied"},
		{"remove", []string{"uninstall"}, func() {
			// Migrated already, which would read the plist.
			require.NoError(s.T(), fsmigrate.Run(fsmigrate.Ctx{Home: s.home, Paths: s.paths}))
			require.NoError(s.T(), os.MkdirAll(filepath.Join(s.paths.LaunchAgent, "x"), 0o755))
		}, "directory not empty"},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			tc.setup()
			require.Equal(s.T(), 1, s.run(append([]string{"service"}, tc.args...)...))
			require.Contains(s.T(), s.stderr.String(), tc.want)
		})
	}
}

func (s *AppSuite) TestClientResolution() {
	tokenFile := filepath.Join(s.home, "tok")
	require.NoError(s.T(), os.WriteFile(tokenFile, []byte("from-file\n"), 0o600))
	tests := []struct {
		name      string
		flags     clientFlags
		env       map[string]string
		container bool
		macToken  bool
		wantURL   string
		wantToken string
		wantErr   string
	}{
		{name: "flags win", flags: clientFlags{url: "http://a", token: "t1"}, env: map[string]string{envURL: "http://b", envToken: "t2"},
			wantURL: "http://a", wantToken: "t1"},
		{name: "env", env: map[string]string{envURL: "http://b", envToken: "t2"}, wantURL: "http://b", wantToken: "t2"},
		{name: "token file flag", flags: clientFlags{tokenFile: tokenFile}, env: map[string]string{envTokenFile: "/nope"},
			wantURL: client.LocalURL, wantToken: "from-file"},
		{name: "token file env", env: map[string]string{envTokenFile: tokenFile}, container: true,
			wantURL: client.ContainerURL, wantToken: "from-file"},
		{name: "the Mac's token", macToken: true, wantURL: client.LocalURL, wantToken: "mac-token"},
		{name: "no token", wantErr: "no API token: set MACUSE_TOKEN (run `macuse token` on the Mac) or MACUSE_TOKEN_FILE"},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			s.app.getenv = func(k string) string { return tc.env[k] }
			s.app.exists = func(p string) bool { return tc.container && p == "/.dockerenv" }
			if tc.macToken {
				s.writeToken("mac-token")
			}
			c, err := s.app.client(tc.flags, "x")
			if tc.wantErr != "" {
				require.ErrorContains(s.T(), err, tc.wantErr)
				return
			}
			require.NoError(s.T(), err)
			require.Equal(s.T(), tc.wantURL, c.BaseURL)
			require.Equal(s.T(), tc.wantToken, c.Token)
			require.Equal(s.T(), "x", c.Name)
			require.Len(s.T(), c.Session, 12)
		})
	}
}

func (s *AppSuite) TestClientFailures() {
	s.app.home = func() (string, error) { return "", errors.New("no home") }
	_, err := s.app.client(clientFlags{}, "x")
	require.EqualError(s.T(), err, "no home")

	s.app.tokens = auth.Tokens{Rand: &failReader{}}
	_, err = s.app.client(clientFlags{token: "t"}, "x")
	require.EqualError(s.T(), err, "token: no entropy")
}

func (s *AppSuite) TestStatus() {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"granted", `{"version":"v1","permissions":{"accessibility":true,"screen_recording":true}}`,
			"accessibility: granted\nscreen recording: granted\n"},
		{"paused and missing", `{"version":"v1","paused":true,"permissions":{"accessibility":false,"screen_recording":true}}`,
			"paused: agents can't use the Mac until you resume\naccessibility: missing; grant it from the macuse menu bar\nscreen recording: granted\n"},
		{"no permissions", `{"version":"v1"}`, ""},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(s.T(), "Bearer tok", r.Header.Get("Authorization"))
				require.Equal(s.T(), "macuse status", r.Header.Get("X-Macuse-Client"))
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			require.Equal(s.T(), 0, s.run("status", "--url", srv.URL, "--token", "tok"))
			require.Equal(s.T(), "macuse v1 at "+srv.URL+"\n"+tc.want, s.stdout.String())
		})
	}
}

func (s *AppSuite) TestStatusFailures() {
	require.Equal(s.T(), 1, s.run("status"))
	require.Contains(s.T(), s.stderr.String(), "no API token")

	s.SetupTest()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":"unauthorized","message":"wrong token"}}`)
	}))
	defer srv.Close()
	require.Equal(s.T(), 1, s.run("status", "--url", srv.URL, "--token", "bad"))
	require.Contains(s.T(), s.stderr.String(), "wrong token")
}

func (s *AppSuite) TestMCP() {
	serverT, clientT := mcp.NewInMemoryTransports()
	s.app.stdio = serverT
	done := make(chan int, 1)
	go func() { done <- s.run("mcp", "--token", "tok", "--url", "http://127.0.0.1:1") }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientT, nil)
	require.NoError(s.T(), err)
	tools, err := session.ListTools(ctx, nil)
	require.NoError(s.T(), err)
	var names []string
	for _, t := range tools.Tools {
		names = append(names, t.Name)
	}
	require.Contains(s.T(), names, "get_state")
	require.Equal(s.T(), "macuse", session.InitializeResult().ServerInfo.Name)
	require.NoError(s.T(), session.Close())
	require.Equal(s.T(), 0, <-done)
}

func (s *AppSuite) TestMCPNoToken() {
	require.Equal(s.T(), 1, s.run("mcp"))
	require.Contains(s.T(), s.stderr.String(), "no API token")
}

func (s *AppSuite) TestGranted() {
	require.Equal(s.T(), "granted", granted(true))
	require.True(s.T(), strings.HasPrefix(granted(false), "missing"))
}

func (s *AppSuite) TestFirst() {
	require.Equal(s.T(), "", first())
	require.Equal(s.T(), "b", first("", "b", "c"))
}
