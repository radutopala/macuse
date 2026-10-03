package core

import (
	"errors"
	"image"
	"time"

	"github.com/radutopala/macuse/internal/proto"
)

// Button is a mouse button.
type Button int

const (
	ButtonLeft Button = iota
	ButtonRight
)

// Window is an app's focused window. Ref is the platform handle, released
// with Platform.Release; ID is the window server's id, 0 when unknown; PID
// is its app's process.
type Window struct {
	Title string
	Frame Rect
	Ref   uintptr
	ID    uint32
	PID   int
}

// ErrNoReply means the app took a press but didn't answer in time, as
// when the press opens a modal dialog. The press most likely happened.
var ErrNoReply = errors.New("the app didn't reply in time")

// Platform is the OS layer the Service drives. Errors may be *proto.Error to
// carry a specific code; anything else is reported as internal.
type Platform interface {
	ListApps() ([]proto.App, error)
	// StartApp launches the app in the background; a running app is left
	// as it is.
	StartApp(bundleID string) error
	// Activate brings the app to the front so input reaches it.
	Activate(app proto.App) error
	// Frontmost reports whether the app is the active one, where input
	// posted to the front goes.
	Frontmost(app proto.App) bool
	FocusedWindow(app proto.App) (Window, error)
	// Windows lists the ids of the app's windows on screen, popovers and
	// menus included.
	Windows(app proto.App) []uint32
	// Tree walks the window's accessibility tree within lim. truncated is
	// true when a limit cut the walk short.
	Tree(win Window, lim Limits) (root *Node, truncated bool, err error)
	// Capture returns what's within win.Frame: just the app's windows, even
	// when covered, if the window's ID is known, so a popover it opened
	// shows too; else the screen.
	Capture(win Window) (image.Image, error)
	Press(ref uintptr) error
	SetValue(ref uintptr, value string) error
	// Focus makes the element its app's focused element, where keys go.
	Focus(ref uintptr) error
	// InsertText focuses the element and inserts text at its selection, or
	// the app's focused element's when ref is 0, without the app being
	// frontmost. It fails with proto.CodeUnsupported when the element
	// doesn't take it, leaving the element focused for keys.
	InsertText(app proto.App, ref uintptr, text string) error
	// UserIdle is how long ago the keyboard or mouse was last used,
	// including by the platform's own input.
	UserIdle() time.Duration
	// The input below goes to the frontmost app, as the user's would.
	Click(p Point, button Button, count int) error
	Drag(from, to Point) error
	Scroll(p Point, dx, dy int) error
	Key(c Combo) error
	Type(text string) error
	// KeyTo and TypeTo send keys to the app's process, which needn't be
	// frontmost. They reach its focused element, which may ignore them,
	// with no error.
	KeyTo(app proto.App, c Combo) error
	TypeTo(app proto.App, text string) error
	Release(refs []uintptr)
	Permissions() proto.Permissions
	RequestPermissions() proto.Permissions
}
