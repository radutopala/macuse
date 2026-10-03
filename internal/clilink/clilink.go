// Package clilink puts the macuse binary inside the app on the PATH, as a
// symlink, so the CLI and the app are one signed binary.
package clilink

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Fallback is where an administrator links the CLI when no directory on the
// user's PATH is writable. It is on every PATH.
const Fallback = "/usr/local/bin/macuse"

const marker = "__macuse-path__"

// Writable are the directories Install may link in, when they are on PATH.
// Other writable directories on PATH tend to belong to one tool, or sit
// inside an app, whose signature a new file would break.
func Writable(home string) []string {
	return []string{"/opt/homebrew/bin", "/usr/local/bin", filepath.Join(home, ".local", "bin"), filepath.Join(home, "bin")}
}

// ShellPath is the PATH of the user's login shell, which an app opened from
// Finder or launchd doesn't inherit. It is nil when the shell fails.
func ShellPath(ctx context.Context, run func(ctx context.Context, name string, args ...string) ([]byte, error), shell string) []string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Interactive, so zsh reads .zshrc too. The markers set PATH apart from
	// whatever the startup files print.
	out, err := run(ctx, shell, "-ilc", `printf '`+marker+`%s`+marker+`' "$PATH"`)
	if err != nil {
		return nil
	}
	_, rest, _ := strings.Cut(string(out), marker)
	path, _, ok := strings.Cut(rest, marker)
	if !ok {
		return nil
	}
	return filepath.SplitList(path)
}

// Link is the CLI's symlink.
type Link struct {
	// Target is the binary inside the app.
	Target string
	// Dirs lists the directories on the user's PATH, in order.
	Dirs func() []string
	// Writable are the directories Install may link in.
	Writable []string
	// Fallback is the link an administrator creates.
	Fallback string
	// Run runs a command (osascript), returning its error with its output.
	Run func(name string, args ...string) error
}

// paths are the links that may hold the CLI: macuse in each absolute
// directory on PATH, then Fallback.
func (l Link) paths() []string {
	var ps []string
	for _, d := range l.Dirs() {
		if filepath.IsAbs(d) {
			ps = append(ps, filepath.Join(d, "macuse"))
		}
	}
	return append(ps, l.Fallback)
}

// Installed reports whether one of the paths resolves to Target.
func (l Link) Installed() bool {
	target, err := filepath.EvalSymlinks(l.Target)
	if err != nil {
		return false
	}
	for _, p := range l.paths() {
		if real, err := filepath.EvalSymlinks(p); err == nil && real == target {
			return true
		}
	}
	return false
}

// Install links macuse in the first of the Writable directories on PATH
// the user can write. Where there is none, it asks for an administrator's
// password, as installers do, to link Fallback.
func (l Link) Install() error {
	for _, d := range l.Dirs() {
		if slices.Contains(l.Writable, filepath.Clean(d)) && link(l.Target, filepath.Join(d, "macuse")) == nil {
			return nil
		}
	}
	cmd := fmt.Sprintf("mkdir -p %s && ln -sf %s %s",
		shellQuote(filepath.Dir(l.Fallback)), shellQuote(l.Target), shellQuote(l.Fallback))
	script := fmt.Sprintf("do shell script %s with administrator privileges", appleString(cmd))
	if err := l.Run("osascript", "-e", script); err != nil {
		return fmt.Errorf("linking %s: %w", l.Fallback, err)
	}
	return nil
}

// link points path at target, replacing a symlink there but never a file.
// It doesn't create directories: one that isn't there isn't on PATH yet.
func link(target, path string) error {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSymlink == 0 {
		return errors.New(path + " isn't a symlink")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Symlink(target, path)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func appleString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
