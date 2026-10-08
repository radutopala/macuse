package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/radutopala/macuse/internal/approval"
	"github.com/radutopala/macuse/internal/clilink"
	"github.com/radutopala/macuse/internal/config"
	"github.com/radutopala/macuse/internal/core"
	"github.com/radutopala/macuse/internal/launchagent"
	"github.com/radutopala/macuse/internal/native"
	"github.com/radutopala/macuse/internal/policy"
	"github.com/radutopala/macuse/internal/proto"
	"github.com/radutopala/macuse/internal/server"
)

func (a *app) serve(ctx context.Context) error {
	p, err := a.migratedPaths()
	if err != nil {
		return err
	}
	logger, closeLog, err := a.serveLogger(p.Log)
	if err != nil {
		return err
	}
	defer closeLog()
	cfg, err := config.Load(p.Config)
	if err != nil {
		return err
	}
	token, err := a.tokens.Ensure(p.Token)
	if err != nil {
		return err
	}
	uiKey, err := a.tokens.New()
	if err != nil {
		return err
	}
	agent, err := a.agent(p)
	if err != nil {
		return err
	}
	store, err := approval.OpenStore(p.Approvals, a.now)
	if err != nil {
		return err
	}
	auditFile, err := openAppend(p.Audit)
	if err != nil {
		return err
	}
	defer func() { _ = auditFile.Close() }()

	ln, err := a.listen("tcp", cfg.Listen)
	if err != nil {
		if a.running(ctx, cfg.Listen) {
			fmt.Fprintln(a.stdout, "macuse is already running at", cfg.Listen)
			return nil
		}
		return fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}
	runner, err := a.newPlatform(logger)
	if err != nil {
		_ = ln.Close()
		return err
	}

	ctx, quit := context.WithCancel(ctx)
	defer quit()
	bundle := bundlePath(agent.Exe)
	rs := &restarter{a: a, bundle: bundle, quit: quit, logger: logger}
	var updater server.Updater
	if up := a.updater(bundle, rs.restart); up != nil {
		updater = up
		go up.Run(ctx)
	}
	m := &menu{runner: runner}
	var srv *server.Server
	notify := func() { m.update(srv.Summary()) }
	broker := approval.NewBroker(cfg.ApprovalTimeout(), a.now, notify)
	srv = server.New(server.Deps{
		Engine:  core.NewService(runner, time.Sleep),
		Gate:    approval.NewGate(store, broker),
		Broker:  broker,
		Store:   store,
		Policy:  policy.NewPolicy(cfg.DenyApps),
		Audit:   server.NewAudit(auditFile, a.now),
		Host:    host{agent: agent, link: a.cliLink(ctx, bundle, agent.Exe), quit: quit},
		Updater: updater,
		Logger:  logger,
		Now:     a.now,
		Token:   token,
		UIKey:   uiKey,
		Version: a.version,
		Notify:  notify,
	})
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	// The native calls need Run on this, the main, goroutine until the HTTP
	// server is down, so Run outlives ctx.
	runCtx, stopRun := context.WithCancel(context.Background())
	var serveErr error
	served := make(chan struct{})
	go func() {
		defer close(served)
		if err := hs.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
			quit()
		}
	}()
	go func() {
		defer stopRun()
		a.menuLoop(ctx, runner, m, uiURL(ln.Addr(), uiKey), notify, logger)
		// Pausing cancels the calls waiting on the user, so they answer
		// before the grace runs out.
		srv.SetPaused(true)
		sctx, cancel := context.WithTimeout(context.Background(), a.grace)
		defer cancel()
		if err := hs.Shutdown(sctx); err != nil {
			_ = hs.Close()
		}
		<-served
	}()
	logger.Info("macuse serving", "addr", ln.Addr().String(), "version", a.version)
	if err := runner.Run(runCtx); err != nil {
		return err
	}
	if serveErr == nil && rs.failed {
		return errRestart
	}
	return serveErr
}

// menuLoop shows the menu bar and keeps it current until ctx is done.
func (a *app) menuLoop(ctx context.Context, runner native.Runner, m *menu, pageURL string, notify func(), logger *slog.Logger) {
	if err := runner.StartMenuBar(pageURL); err != nil {
		// The API works without the menu bar, but nobody can answer.
		logger.Error("menu bar", "error", err)
	}
	notify()
	// Activity fades on its own, so redraw on a beat too.
	t := time.NewTicker(a.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			notify()
		}
	}
}

// uiURL is the popup page on addr. The popup runs on this Mac, so a
// wildcard listen address is reached over loopback.
func uiURL(addr net.Addr, key string) string {
	host, port, _ := net.SplitHostPort(addr.String())
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/ui?key=" + url.QueryEscape(key)
}

// running reports whether addr is already a macuse API: it refuses an
// unauthenticated status with macuse's own error.
func (a *app) running(ctx context.Context, addr string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/v1/status", nil)
	if err != nil {
		return false
	}
	res, err := a.http.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = res.Body.Close() }()
	var body proto.ErrorBody
	return res.StatusCode == http.StatusUnauthorized &&
		json.NewDecoder(res.Body).Decode(&body) == nil &&
		body.Error != nil && body.Error.Code == proto.CodeUnauthorized
}

// serveLogger logs to stderr and the log file, unless stderr is the log
// file already, as the LaunchAgent makes it.
func (a *app) serveLogger(path string) (*slog.Logger, func(), error) {
	f, err := openAppend(path)
	if err != nil {
		return nil, nil, err
	}
	if sameFile(a.stderr, f) {
		_ = f.Close()
		return slog.New(slog.NewTextHandler(a.stderr, nil)), func() {}, nil
	}
	return slog.New(slog.NewTextHandler(io.MultiWriter(a.stderr, f), nil)), func() { _ = f.Close() }, nil
}

func sameFile(w io.Writer, f *os.File) bool {
	wf, ok := w.(*os.File)
	if !ok {
		return false
	}
	a, errA := wf.Stat()
	b, errB := f.Stat()
	return errA == nil && errB == nil && os.SameFile(a, b)
}

func openAppend(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// menu draws server summaries on the menu bar, opening the popover when a
// new request arrives.
type menu struct {
	runner  native.Runner
	mu      sync.Mutex
	pending int
}

func (m *menu) update(s server.Summary) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runner.UpdateMenuBar(native.MenuState{Pending: s.Pending, Active: s.Active, Paused: s.Paused})
	if s.Pending > m.pending {
		m.runner.ShowPopover()
	}
	m.pending = s.Pending
}

// host is what the popup controls: the login item, the CLI link and
// quitting.
type host struct {
	agent launchagent.Agent
	// link is nil outside the app.
	link *clilink.Link
	quit func()
}

func (h host) LoginItem() bool { return h.agent.Installed() }

func (h host) SetLoginItem(on bool) error {
	if on {
		return h.agent.Write()
	}
	return h.agent.Remove()
}

func (h host) CLI() string {
	switch {
	case h.link == nil:
		return ""
	case h.link.Installed():
		return "installed"
	}
	return "missing"
}

func (h host) InstallCLI() error { return h.link.Install() }

func (h host) Quit() { h.quit() }
