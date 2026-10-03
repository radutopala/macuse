// Package launchagent makes mac-use a per-user LaunchAgent, so it starts at
// login and launchd restarts it if it crashes.
package launchagent

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

// Agent is mac-use's LaunchAgent.
type Agent struct {
	// Path is the plist, ~/Library/LaunchAgents/<Label>.plist.
	Path  string
	Label string
	// Exe is the binary launchd runs with "serve".
	Exe string
	// Log takes the agent's stdout and stderr.
	Log string
	UID int
	// Run runs a command (launchctl), returning its error with its output.
	Run func(name string, args ...string) error
}

// Plist is the agent's property list. KeepAlive restarts mac-use after a
// crash but not after the user quits it (exit 0); Aqua limits it to a GUI
// login, where it can draw the menu bar.
func (a Agent) Plist() []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>`)
	escape(&b, a.Label)
	b.WriteString(`</string>
	<key>ProgramArguments</key>
	<array>
		<string>`)
	escape(&b, a.Exe)
	b.WriteString(`</string>
		<string>serve</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>LimitLoadToSessionType</key>
	<string>Aqua</string>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>StandardOutPath</key>
	<string>`)
	escape(&b, a.Log)
	b.WriteString(`</string>
	<key>StandardErrorPath</key>
	<string>`)
	escape(&b, a.Log)
	b.WriteString(`</string>
</dict>
</plist>
`)
	return b.Bytes()
}

func escape(b *bytes.Buffer, s string) {
	_ = xml.EscapeText(b, []byte(s)) // a bytes.Buffer never fails
}

// Installed reports whether the plist exists, so mac-use opens at login.
func (a Agent) Installed() bool {
	_, err := os.Stat(a.Path)
	return err == nil
}

// Write writes the plist; launchd loads it at the next login.
func (a Agent) Write() error {
	if err := os.MkdirAll(filepath.Dir(a.Path), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.Log), 0o700); err != nil {
		return err
	}
	return os.WriteFile(a.Path, a.Plist(), 0o644)
}

// Remove deletes the plist; a missing one is fine. A running agent keeps
// running until it quits or the user logs out.
func (a Agent) Remove() error {
	if err := os.Remove(a.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (a Agent) domain() string { return "gui/" + strconv.Itoa(a.UID) }

// Install writes the plist and starts the agent now, replacing a loaded
// one.
func (a Agent) Install() error {
	if err := a.Write(); err != nil {
		return err
	}
	// Not loaded yet is the usual case, so this one may fail.
	_ = a.Run("launchctl", "bootout", a.domain()+"/"+a.Label)
	if err := a.Run("launchctl", "bootstrap", a.domain(), a.Path); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w", err)
	}
	return nil
}

// Uninstall stops the agent and deletes the plist.
func (a Agent) Uninstall() error {
	// The agent may not be loaded, which leaves nothing to stop.
	_ = a.Run("launchctl", "bootout", a.domain()+"/"+a.Label)
	return a.Remove()
}
