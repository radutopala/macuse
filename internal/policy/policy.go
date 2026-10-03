package policy

import (
	"strings"

	"github.com/radutopala/macuse/internal/buildinfo"
	"github.com/radutopala/macuse/internal/proto"
)

// hardDenyList are apps whose control would hand an agent a shell, macuse
// itself, system configuration, or stored secrets.
var hardDenyList = []string{
	// Terminals: controlling one is running arbitrary commands.
	"com.apple.Terminal",
	"com.github.wez.wezterm",
	"com.googlecode.iterm2",
	"com.mitchellh.ghostty",
	"dev.warp.Warp-Stable",
	"io.alacritty",
	"net.kovidgoyal.kitty",
	"org.alacritty", // the id Alacritty's release builds ship with
	// macuse itself: an agent could approve its own requests.
	buildinfo.BundleID,
	// System Settings: privacy grants, accounts, security.
	"com.apple.systempreferences",
	// Keychain and password managers.
	"com.1password.1password",
	"com.agilebits.onepassword7",
	"com.apple.Passwords",
	"com.apple.keychainaccess",
	"com.bitwarden.desktop",
	// Scripting tools: each runs arbitrary code.
	"com.apple.Automator",
	"com.apple.ScriptEditor2",
	"com.apple.shortcuts",
}

// HardDenyList are bundle ids agents may never control: terminals (and so a
// shell), macuse itself, System Settings, the keychain, password managers, and
// scripting tools. The returned slice is a copy.
func HardDenyList() []string {
	return append([]string(nil), hardDenyList...)
}

// Policy decides which apps agents may not control.
type Policy struct {
	denied map[string]struct{}
}

// NewPolicy denies the hard list plus extra (config deny_apps); extra
// can only add to the hard list.
func NewPolicy(extra []string) *Policy {
	p := &Policy{denied: make(map[string]struct{}, len(hardDenyList)+len(extra))}
	for _, id := range hardDenyList {
		p.denied[strings.ToLower(id)] = struct{}{}
	}
	for _, id := range extra {
		if id = strings.TrimSpace(id); id != "" {
			p.denied[strings.ToLower(id)] = struct{}{}
		}
	}
	return p
}

// Denied reports whether bundleID may not be controlled. Matching is exact and
// case-insensitive; an empty bundle id is denied since the app can't be
// identified.
func (p *Policy) Denied(bundleID string) bool {
	if bundleID == "" {
		return true
	}
	_, ok := p.denied[strings.ToLower(bundleID)]
	return ok
}

// Filter drops denied apps, preserving order.
func (p *Policy) Filter(apps []proto.App) []proto.App {
	out := make([]proto.App, 0, len(apps))
	for _, a := range apps {
		if !p.Denied(a.BundleID) {
			out = append(out, a)
		}
	}
	return out
}
