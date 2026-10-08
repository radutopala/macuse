package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/radutopala/macuse/internal/auth"
	"github.com/radutopala/macuse/internal/buildinfo"
	"github.com/radutopala/macuse/internal/clilink"
	"github.com/radutopala/macuse/internal/config"
	"github.com/radutopala/macuse/internal/fsmigrate"
	"github.com/radutopala/macuse/internal/launchagent"
	"github.com/radutopala/macuse/internal/native"
	"github.com/radutopala/macuse/internal/update"
)

// app holds the CLI's dependencies, swapped per test.
type app struct {
	stdout, stderr io.Writer
	getenv         func(string) string
	exists         func(string) bool
	home           func() (string, error)
	executable     func() (string, error)
	uid            func() int
	now            func() time.Time
	tokens         auth.Tokens
	http           *http.Client
	listen         func(network, addr string) (net.Listener, error)
	newPlatform    func(*slog.Logger) (native.Runner, error)
	command        func(name string, args ...string) error
	// output runs a command, returning its combined output.
	output func(ctx context.Context, name string, args ...string) ([]byte, error)
	// start starts a command that outlives this process.
	start func(name string, args ...string) error
	pid   func() int
	// version is this build's, which updates compare against.
	version     string
	updateFeed  string
	updateEvery time.Duration
	// cliFallback is the CLI link an administrator creates when no
	// directory on the user's PATH is writable.
	cliFallback string
	// stdio is the MCP transport "macuse mcp" serves.
	stdio mcp.Transport
	// tick paces the menu bar's refresh.
	tick time.Duration
	// grace is how long in-flight requests get to finish on quit.
	grace time.Duration
}

func newApp() *app {
	return &app{
		stdout:      os.Stdout,
		stderr:      os.Stderr,
		getenv:      os.Getenv,
		exists:      func(p string) bool { _, err := os.Stat(p); return err == nil },
		home:        os.UserHomeDir,
		executable:  os.Executable,
		uid:         os.Getuid,
		now:         time.Now,
		tokens:      auth.DefaultTokens,
		http:        &http.Client{},
		listen:      net.Listen,
		newPlatform: native.New,
		command:     runCommand,
		output:      runOutput,
		start:       startDetached,
		pid:         os.Getpid,
		version:     buildinfo.Version,
		updateFeed:  update.DefaultFeed,
		updateEvery: 30 * time.Minute,
		cliFallback: clilink.Fallback,
		stdio:       &mcp.StdioTransport{},
		tick:        time.Second,
		grace:       5 * time.Second,
	}
}

func runCommand(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// run executes the command line, returning the exit code.
func (a *app) run(ctx context.Context, args []string) int {
	root := a.root()
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(a.stderr, "macuse:", err)
		return 1
	}
	return 0
}

// bundled reports whether this binary runs from inside an app bundle,
// where opening the app (no arguments) means serve.
func (a *app) bundled() bool {
	exe, err := a.executable()
	return err == nil && strings.Contains(exe, ".app/Contents/MacOS/")
}

func (a *app) root() *cobra.Command {
	root := &cobra.Command{
		Use:           "macuse",
		Short:         "Let AI agents drive macOS apps, with your approval",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.bundled() {
				return a.serve(cmd.Context())
			}
			return cmd.Help()
		},
	}
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	root.AddCommand(
		&cobra.Command{
			Use:   "serve",
			Short: "Run the menu bar app and its API",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, _ []string) error { return a.serve(cmd.Context()) },
		},
		a.mcpCommand(),
		&cobra.Command{
			Use:   "token",
			Short: "Print the API token, creating it the first time",
			Args:  cobra.NoArgs,
			RunE:  func(*cobra.Command, []string) error { return a.printToken() },
		},
		a.statusCommand(),
		a.serviceCommand(),
		&cobra.Command{
			Use:   "version",
			Short: "Print the version",
			Args:  cobra.NoArgs,
			Run:   func(*cobra.Command, []string) { fmt.Fprintln(a.stdout, buildinfo.Version) },
		},
	)
	return root
}

// paths returns the files macuse keeps, without touching them. A client
// that only reads the token may run where ~/.macuse isn't writable, such
// as a container with the token mounted in.
func (a *app) paths() (config.Paths, error) {
	home, err := a.home()
	if err != nil {
		return config.Paths{}, err
	}
	return config.NewPaths(home), nil
}

// migratedPaths is paths, after the pending fs migrations, for the commands
// that own the files on the Mac.
func (a *app) migratedPaths() (config.Paths, error) {
	home, err := a.home()
	if err != nil {
		return config.Paths{}, err
	}
	p := config.NewPaths(home)
	if err := fsmigrate.Run(fsmigrate.Ctx{Home: home, Paths: p}); err != nil {
		return config.Paths{}, err
	}
	return p, nil
}

func (a *app) printToken() error {
	p, err := a.migratedPaths()
	if err != nil {
		return err
	}
	tok, err := a.tokens.Ensure(p.Token)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.stdout, tok)
	return nil
}

func (a *app) agent(p config.Paths) (launchagent.Agent, error) {
	exe, err := a.executable()
	if err != nil {
		return launchagent.Agent{}, err
	}
	// Run through a symlink on the PATH, launchd should still start the
	// binary in the app bundle, which holds the privacy grants.
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return launchagent.Agent{
		Path:  p.LaunchAgent,
		Label: buildinfo.BundleID,
		Exe:   exe,
		Log:   p.Log,
		UID:   a.uid(),
		Run:   a.command,
		Sleep: time.Sleep,
	}, nil
}

func (a *app) serviceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Start macuse at login, as a LaunchAgent",
	}
	withAgent := func(f func(launchagent.Agent) error) func(*cobra.Command, []string) error {
		return func(*cobra.Command, []string) error {
			p, err := a.migratedPaths()
			if err != nil {
				return err
			}
			ag, err := a.agent(p)
			if err != nil {
				return err
			}
			return f(ag)
		}
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "install",
			Short: "Start macuse now and at every login",
			Args:  cobra.NoArgs,
			RunE: withAgent(func(ag launchagent.Agent) error {
				if err := ag.Install(); err != nil {
					return err
				}
				fmt.Fprintln(a.stdout, "macuse runs now and at every login.")
				return nil
			}),
		},
		&cobra.Command{
			Use:   "uninstall",
			Short: "Stop macuse and don't start it at login",
			Args:  cobra.NoArgs,
			RunE: withAgent(func(ag launchagent.Agent) error {
				if err := ag.Uninstall(); err != nil {
					return err
				}
				fmt.Fprintln(a.stdout, "macuse stopped and won't start at login.")
				return nil
			}),
		},
	)
	return cmd
}
