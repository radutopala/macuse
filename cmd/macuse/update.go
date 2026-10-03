package main

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/radutopala/macuse/internal/buildinfo"
	"github.com/radutopala/macuse/internal/clilink"
	"github.com/radutopala/macuse/internal/update"
)

// errRestart ends a launchd-run serve with an error, so launchd starts the
// updated app again.
var errRestart = errors.New("restarting into the update")

// relaunch waits for the process $1 to exit, then opens the app $2.
const relaunch = `while kill -0 "$1" 2>/dev/null; do sleep 0.2; done; exec open "$2"`

// bundlePath is the .app holding exe, or "" when exe isn't in one.
func bundlePath(exe string) string {
	i := strings.Index(exe, ".app/Contents/MacOS/")
	if i < 0 {
		return ""
	}
	return exe[:i+len(".app")]
}

// startDetached starts a command that outlives this process.
func startDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Start()
}

func runOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// restarter starts the app again after an update. launchd restarts its
// agent when it fails; an app the user opened is reopened once this
// process is gone, since a second copy can't take the port.
type restarter struct {
	a      *app
	bundle string
	quit   func()
	logger *slog.Logger
	failed bool
}

func (r *restarter) restart() {
	if r.a.getenv("XPC_SERVICE_NAME") == buildinfo.BundleID {
		r.failed = true
	} else if err := r.a.start("/bin/sh", "-c", relaunch, "sh", strconv.Itoa(r.a.pid()), r.bundle); err != nil {
		r.logger.Error("reopening the app after the update", "error", err)
	}
	r.quit()
}

// updater keeps the app at bundle current, or is nil where it can't:
// outside an app, or in a build between releases.
func (a *app) updater(bundle string, restart func()) *update.Manager {
	if bundle == "" || !update.Valid(a.version) {
		return nil
	}
	inst := update.Installer{App: bundle, HTTP: a.http, Run: a.output}
	return &update.Manager{
		Current: a.version,
		Check: func(ctx context.Context) (update.Release, error) {
			return update.Latest(ctx, a.http, a.updateFeed)
		},
		Install:  inst.Install,
		Restart:  restart,
		Interval: a.updateEvery,
	}
}

// cliLink is the CLI's link to the binary in bundle, or nil outside one. It
// asks the login shell for PATH once, when the popup first needs it.
func (a *app) cliLink(ctx context.Context, bundle, exe string) *clilink.Link {
	if bundle == "" {
		return nil
	}
	home, _ := a.home()
	shell := a.getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	return &clilink.Link{
		Target:   exe,
		Dirs:     sync.OnceValue(func() []string { return clilink.ShellPath(ctx, a.output, shell) }),
		Writable: clilink.Writable(home),
		Fallback: a.cliFallback,
		Run:      a.command,
	}
}
