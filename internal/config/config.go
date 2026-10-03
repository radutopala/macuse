// Package config locates macuse's files and loads its settings.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/radutopala/macuse/internal/buildinfo"
)

// DefaultListen is where the API listens: loopback only. Docker Desktop
// forwards host.docker.internal to it, so containers reach it too.
const DefaultListen = "127.0.0.1:7710"

// DefaultApprovalTimeout is how long an agent waits for the user to answer.
const DefaultApprovalTimeout = 2 * time.Minute

// Paths are the files macuse keeps.
type Paths struct {
	Dir         string // ~/.macuse
	Config      string // config.json
	Token       string // the API token, 0600
	Approvals   string // approvals.json
	Audit       string // logs/audit.jsonl
	Log         string // logs/macuse.log
	LaunchAgent string // ~/Library/LaunchAgents/<bundle id>.plist
}

// NewPaths returns the paths under home.
func NewPaths(home string) Paths {
	dir := filepath.Join(home, ".macuse")
	return Paths{
		Dir:         dir,
		Config:      filepath.Join(dir, "config.json"),
		Token:       filepath.Join(dir, "token"),
		Approvals:   filepath.Join(dir, "approvals.json"),
		Audit:       filepath.Join(dir, "logs", "audit.jsonl"),
		Log:         filepath.Join(dir, "logs", "macuse.log"),
		LaunchAgent: filepath.Join(home, "Library", "LaunchAgents", buildinfo.BundleID+".plist"),
	}
}

// Config is config.json. Every field is optional.
type Config struct {
	// Listen is the API's host:port.
	Listen string `json:"listen,omitempty"`
	// DenyApps adds bundle ids agents may never control to the built-in list.
	DenyApps []string `json:"deny_apps,omitempty"`
	// ApprovalTimeoutSec is how long an agent waits for an answer.
	ApprovalTimeoutSec int `json:"approval_timeout_sec,omitempty"`
}

// Load reads the config at path; a missing file is the defaults.
func Load(path string) (Config, error) {
	var c Config
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c.withDefaults(), nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("config %s: %w", path, err)
	}
	return c.withDefaults(), nil
}

func (c Config) withDefaults() Config {
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	return c
}

// ApprovalTimeout is ApprovalTimeoutSec, or the default when unset.
func (c Config) ApprovalTimeout() time.Duration {
	if c.ApprovalTimeoutSec <= 0 {
		return DefaultApprovalTimeout
	}
	return time.Duration(c.ApprovalTimeoutSec) * time.Second
}
