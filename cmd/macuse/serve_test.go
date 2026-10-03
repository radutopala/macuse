package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/radutopala/macuse/internal/auth"
	"github.com/radutopala/macuse/internal/core"
	"github.com/radutopala/macuse/internal/launchagent"
	"github.com/radutopala/macuse/internal/native"
	"github.com/radutopala/macuse/internal/proto"
	"github.com/radutopala/macuse/internal/server"
)

// mockRunner is the native layer. The core.Platform methods a test doesn't
// mock panic on the nil embedded interface.
type mockRunner struct {
	core.Platform
	mock.Mock
}

func (m *mockRunner) Run(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

func (m *mockRunner) StartMenuBar(pageURL string) error {
	return m.Called(pageURL).Error(0)
}

func (m *mockRunner) UpdateMenuBar(st native.MenuState) { m.Called(st) }

func (m *mockRunner) ShowPopover() { m.Called() }

func (m *mockRunner) Permissions() proto.Permissions {
	return m.Called().Get(0).(proto.Permissions)
}

func (m *mockRunner) ListApps() ([]proto.App, error) {
	args := m.Called()
	return args.Get(0).([]proto.App), args.Error(1)
}

// runUntilDone makes Run serve until its ctx ends, like the real one.
func runUntilDone(args mock.Arguments) {
	<-args.Get(0).(context.Context).Done()
}

// failListener fails every Accept.
type failListener struct{ net.Listener }

func (failListener) Accept() (net.Conn, error) { return nil, errors.New("accept failed") }

// serving is a serve run in the background.
type serving struct {
	runner *mockRunner
	page   chan string
	done   chan int
	cancel context.CancelFunc
}

// writeConfig sets the config's listen address.
func (s *AppSuite) writeConfig(listen string) {
	require.NoError(s.T(), os.MkdirAll(s.paths.Dir, 0o700))
	require.NoError(s.T(), os.WriteFile(s.paths.Config, []byte(`{"listen":"`+listen+`"}`), 0o600))
}

// startServe runs serve on a free port with a runner that serves until
// the run ends.
func (s *AppSuite) startServe() *serving {
	s.writeConfig("127.0.0.1:0")
	sv := &serving{runner: &mockRunner{}, page: make(chan string, 1), done: make(chan int, 1)}
	sv.runner.On("Run", mock.Anything).Run(runUntilDone).Return(nil)
	sv.runner.On("StartMenuBar", mock.Anything).Run(func(args mock.Arguments) { sv.page <- args.String(0) }).Return(nil)
	sv.runner.On("UpdateMenuBar", mock.Anything).Maybe()
	s.app.newPlatform = func(*slog.Logger) (native.Runner, error) { return sv.runner, nil }
	s.app.tick = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	sv.cancel = cancel
	go func() { sv.done <- s.app.run(ctx, []string{"serve"}) }()
	return sv
}

func (s *AppSuite) request(method, rawURL, body string, header ...string) *http.Response {
	req, err := http.NewRequest(method, rawURL, strings.NewReader(body))
	require.NoError(s.T(), err)
	for i := 0; i < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(s.T(), err)
	return res
}

func readAll(res *http.Response) string {
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return string(b)
}

func (s *AppSuite) TestServe() {
	sv := s.startServe()
	sv.runner.On("Permissions").Return(proto.Permissions{Accessibility: true})
	page, err := url.Parse(<-sv.page)
	require.NoError(s.T(), err)
	require.Equal(s.T(), "127.0.0.1", page.Hostname())
	require.Equal(s.T(), "/ui", page.Path)
	base := "http://" + page.Host
	key := []string{server.HeaderUIKey, page.Query().Get("key")}

	res := s.request(http.MethodGet, base+"/ui/api/state", "", key...)
	require.Equal(s.T(), http.StatusOK, res.StatusCode)
	require.Contains(s.T(), readAll(res), `"login_item":false`)

	res = s.request(http.MethodPost, base+"/ui/api/login-item", `{"enabled":true}`, key...)
	require.Equal(s.T(), http.StatusNoContent, res.StatusCode)
	require.FileExists(s.T(), s.paths.LaunchAgent)
	res = s.request(http.MethodPost, base+"/ui/api/login-item", `{"enabled":false}`, key...)
	require.Equal(s.T(), http.StatusNoContent, res.StatusCode)
	require.NoFileExists(s.T(), s.paths.LaunchAgent)

	tok, err := auth.Read(s.paths.Token)
	require.NoError(s.T(), err)
	res = s.request(http.MethodGet, base+"/v1/status", "", "Authorization", "Bearer "+tok)
	require.Equal(s.T(), http.StatusOK, res.StatusCode)
	require.Contains(s.T(), readAll(res), `"accessibility":true`)

	// A second serve finds this one and leaves it be.
	second := *s.app
	second.listen = func(string, string) (net.Listener, error) { return nil, errors.New("address in use") }
	other := s.T().TempDir()
	second.home = func() (string, error) { return other, nil }
	s.paths.Dir = filepath.Join(other, "Library", "Application Support", "macuse")
	s.paths.Config = filepath.Join(s.paths.Dir, "config.json")
	s.writeConfig(page.Host)
	s.stdout.Reset()
	require.Equal(s.T(), 0, second.run(context.Background(), []string{"serve"}))
	require.Equal(s.T(), "macuse is already running at "+page.Host+"\n", s.stdout.String())

	res = s.request(http.MethodPost, base+"/ui/api/quit", "", key...)
	require.Equal(s.T(), http.StatusNoContent, res.StatusCode)
	require.Equal(s.T(), 0, <-sv.done)
	sv.runner.AssertExpectations(s.T())
}

func (s *AppSuite) TestServeShutdownGraceRunsOut() {
	s.app.grace = time.Millisecond
	sv := s.startServe()
	release := make(chan struct{})
	defer close(release)
	called := make(chan struct{})
	// The popup's state reads the grants without an agent call to cancel,
	// so pausing doesn't end it.
	sv.runner.On("Permissions").Run(func(mock.Arguments) {
		close(called)
		<-release
	}).Return(proto.Permissions{})
	page, err := url.Parse(<-sv.page)
	require.NoError(s.T(), err)

	answered := make(chan error, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodGet, "http://"+page.Host+"/ui/api/state", nil)
		req.Header.Set(server.HeaderUIKey, page.Query().Get("key"))
		res, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = res.Body.Close()
		}
		answered <- err
	}()
	<-called
	sv.cancel()
	// The grace runs out with the request still open, so the server
	// closes its connection.
	require.Equal(s.T(), 0, <-sv.done)
	require.Error(s.T(), <-answered)
}

func (s *AppSuite) TestServeMenuBarFails() {
	s.writeConfig("127.0.0.1:0")
	runner := &mockRunner{}
	started := make(chan struct{})
	runner.On("Run", mock.Anything).Run(runUntilDone).Return(nil)
	runner.On("StartMenuBar", mock.Anything).Run(func(mock.Arguments) { close(started) }).Return(errors.New("no WebKit"))
	runner.On("UpdateMenuBar", mock.Anything).Maybe()
	s.app.newPlatform = func(*slog.Logger) (native.Runner, error) { return runner, nil }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- s.app.run(ctx, []string{"serve"}) }()
	<-started
	cancel()
	require.Equal(s.T(), 0, <-done)
	require.Contains(s.T(), s.stderr.String(), "no WebKit")
}

func (s *AppSuite) TestServeAcceptFails() {
	s.writeConfig("127.0.0.1:0")
	runner := &mockRunner{}
	runner.On("Run", mock.Anything).Run(runUntilDone).Return(nil)
	runner.On("StartMenuBar", mock.Anything).Return(nil).Maybe()
	runner.On("UpdateMenuBar", mock.Anything).Maybe()
	s.app.newPlatform = func(*slog.Logger) (native.Runner, error) { return runner, nil }
	s.app.listen = func(network, addr string) (net.Listener, error) {
		ln, err := net.Listen(network, addr)
		return failListener{ln}, err
	}
	require.Equal(s.T(), 1, s.run("serve"))
	require.Contains(s.T(), s.stderr.String(), "accept failed")
}

func (s *AppSuite) TestServeRunFails() {
	s.writeConfig("127.0.0.1:0")
	runner := &mockRunner{}
	runner.On("Run", mock.Anything).Return(errors.New("no main thread"))
	runner.On("StartMenuBar", mock.Anything).Return(nil).Maybe()
	runner.On("UpdateMenuBar", mock.Anything).Maybe()
	s.app.newPlatform = func(*slog.Logger) (native.Runner, error) { return runner, nil }
	require.Equal(s.T(), 1, s.run("serve"))
	require.Contains(s.T(), s.stderr.String(), "no main thread")
}

func (s *AppSuite) TestServeFailures() {
	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer refused.Close()
	busy := strings.TrimPrefix(refused.URL, "http://")
	inUse := func(string, string) (net.Listener, error) { return nil, errors.New("address in use") }

	tests := []struct {
		name  string
		setup func()
		want  string
	}{
		{"home", func() { s.app.home = func() (string, error) { return "", errors.New("no home") } }, "no home"},
		{"config", func() {
			require.NoError(s.T(), os.MkdirAll(s.paths.Dir, 0o700))
			require.NoError(s.T(), os.WriteFile(s.paths.Config, []byte("{"), 0o600))
		}, "unexpected end of JSON input"},
		{"token", func() { s.app.tokens = auth.Tokens{Rand: &failReader{}} }, "token: no entropy"},
		{"ui key", func() {
			s.writeToken("tok")
			s.app.tokens = auth.Tokens{Rand: &failReader{}}
		}, "token: no entropy"},
		{"executable", func() { s.app.executable = func() (string, error) { return "", errors.New("no exe") } }, "no exe"},
		{"approvals", func() {
			require.NoError(s.T(), os.MkdirAll(s.paths.Approvals, 0o700))
		}, "approvals"},
		{"audit dir", func() {
			require.NoError(s.T(), os.MkdirAll(filepath.Dir(filepath.Dir(s.paths.Audit)), 0o700))
			require.NoError(s.T(), os.WriteFile(filepath.Dir(s.paths.Audit), nil, 0o600))
		}, "not a directory"},
		{"audit file", func() {
			require.NoError(s.T(), os.MkdirAll(s.paths.Audit, 0o700))
		}, "is a directory"},
		{"listen", func() {
			s.writeConfig(busy)
			s.app.listen = inUse
		}, "listen on " + busy + ": address in use"},
		{"platform", func() {
			s.writeConfig("127.0.0.1:0")
			s.app.newPlatform = func(*slog.Logger) (native.Runner, error) { return nil, native.ErrUnsupported }
		}, native.ErrUnsupported.Error()},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			tc.setup()
			require.Equal(s.T(), 1, s.run("serve"))
			require.Contains(s.T(), s.stderr.String(), tc.want)
		})
	}
}

func (s *AppSuite) TestRunning() {
	handler := func(status int, body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		}))
	}
	closed := handler(http.StatusOK, "")
	closed.Close()
	tests := []struct {
		name string
		addr string
		want bool
	}{
		{"macuse", s.serverAddr(handler(http.StatusUnauthorized, `{"error":{"code":"unauthorized","message":"x"}}`)), true},
		{"other 401", s.serverAddr(handler(http.StatusUnauthorized, `{"error":{"code":"nope"}}`)), false},
		{"no error", s.serverAddr(handler(http.StatusUnauthorized, `{}`)), false},
		{"not json", s.serverAddr(handler(http.StatusUnauthorized, `nope`)), false},
		{"200", s.serverAddr(handler(http.StatusOK, `{}`)), false},
		{"nothing there", strings.TrimPrefix(closed.URL, "http://"), false},
		{"bad address", "bad\x7fhost", false},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			require.Equal(s.T(), tc.want, s.app.running(context.Background(), tc.addr))
		})
	}
}

func (s *AppSuite) serverAddr(srv *httptest.Server) string {
	s.T().Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// fakeAddr is an address that isn't host:port.
type fakeAddr string

func (a fakeAddr) Network() string { return "fake" }
func (a fakeAddr) String() string  { return string(a) }

func (s *AppSuite) TestUIURL() {
	tests := []struct {
		addr net.Addr
		want string
	}{
		{&net.TCPAddr{IP: net.IPv4zero, Port: 7710}, "http://127.0.0.1:7710/ui?key=a+b"},
		{&net.TCPAddr{IP: net.IPv6unspecified, Port: 7710}, "http://127.0.0.1:7710/ui?key=a+b"},
		{&net.TCPAddr{IP: net.IPv4(192, 168, 1, 2), Port: 80}, "http://192.168.1.2:80/ui?key=a+b"},
		{&net.TCPAddr{IP: net.IPv6loopback, Port: 80}, "http://[::1]:80/ui?key=a+b"},
		{fakeAddr("nonsense"), "http://127.0.0.1:/ui?key=a+b"},
	}
	for _, tc := range tests {
		require.Equal(s.T(), tc.want, uiURL(tc.addr, "a b"))
	}
}

func (s *AppSuite) TestMenu() {
	runner := &mockRunner{}
	m := &menu{runner: runner}
	steps := []struct {
		sum  server.Summary
		show bool
	}{
		{server.Summary{Pending: 1}, true},
		{server.Summary{Pending: 1, Active: true}, false},
		{server.Summary{Paused: true}, false},
		{server.Summary{Pending: 2}, true},
	}
	for _, st := range steps {
		runner.On("UpdateMenuBar", native.MenuState{Pending: st.sum.Pending, Active: st.sum.Active, Paused: st.sum.Paused}).Once()
		if st.show {
			runner.On("ShowPopover").Once()
		}
		m.update(st.sum)
	}
	runner.AssertExpectations(s.T())
}

func (s *AppSuite) TestHost() {
	quit := false
	h := host{
		agent: launchagent.Agent{Path: s.paths.LaunchAgent, Log: s.paths.Log},
		quit:  func() { quit = true },
	}
	require.False(s.T(), h.LoginItem())
	require.NoError(s.T(), h.SetLoginItem(true))
	require.True(s.T(), h.LoginItem())
	require.NoError(s.T(), h.SetLoginItem(false))
	require.False(s.T(), h.LoginItem())
	h.Quit()
	require.True(s.T(), quit)
}
