// Package native is the OS layer of mac-use. On macOS it binds
// Accessibility, Quartz events and CoreGraphics through purego, without cgo;
// other platforms are not supported yet.
package native

import (
	"context"
	"errors"
	"strconv"

	"github.com/radutopala/mac-use/internal/core"
)

// ErrUnsupported is returned off macOS.
var ErrUnsupported = errors.New("mac-use only runs on macOS")

// Runner is a Platform whose calls are served by Run on the main thread.
type Runner interface {
	core.Platform
	Run(ctx context.Context) error
	MenuBar
}

// MenuBar is mac-use's menu bar item, whose popover shows the page at the
// URL StartMenuBar loads.
type MenuBar interface {
	StartMenuBar(pageURL string) error
	UpdateMenuBar(st MenuState)
	ShowPopover()
}

// MenuState is what the menu bar icon shows.
type MenuState struct {
	// Pending counts the requests waiting on the user, shown next to the
	// icon.
	Pending int
	// Active means an agent is driving an app right now.
	Active bool
	// Paused means the user paused every agent.
	Paused bool
}

// Symbol names the SF Symbol the icon shows in this state.
func (st MenuState) Symbol() string {
	switch {
	case st.Paused:
		return "pause.circle"
	case st.Active:
		return "cursorarrow.motionlines"
	}
	return "cursorarrow.rays"
}

// Title is the text next to the icon: the pending count, if any.
func (st MenuState) Title() string {
	if st.Pending == 0 {
		return ""
	}
	return " " + strconv.Itoa(st.Pending)
}
