package native

import (
	"context"
	"fmt"
	"image"
	"log/slog"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/ebitengine/purego/objc"

	"github.com/radutopala/macuse/internal/core"
	"github.com/radutopala/macuse/internal/proto"
)

// Platform drives macOS through Accessibility, Quartz events and
// CoreGraphics capture. Every call runs on the main thread inside Run, which
// also pumps the main run loop so NSWorkspace sees apps launch and quit.
type Platform struct {
	l      *lib
	logger *slog.Logger
	calls  chan func()

	attrs   map[string]uintptr // cached CFString attribute and action names
	teamIDs map[string]string  // bundle path → team id
	sel     map[string]objc.SEL
	typeIDs typeIDs
	menu    *menuBar // nil until StartMenuBar
}

type typeIDs struct {
	str, array, boolean, number, axValue uint64
}

var _ core.Platform = (*Platform)(nil)

// New binds the macOS frameworks.
func New(logger *slog.Logger) (Runner, error) {
	l, err := loadLib()
	if err != nil {
		return nil, err
	}
	p := &Platform{
		l:       l,
		logger:  logger,
		calls:   make(chan func()),
		attrs:   map[string]uintptr{},
		teamIDs: map[string]string{},
		sel:     map[string]objc.SEL{},
		typeIDs: typeIDs{
			str:     l.CFStringGetTypeID(),
			array:   l.CFArrayGetTypeID(),
			boolean: l.CFBooleanGetTypeID(),
			number:  l.CFNumberGetTypeID(),
			axValue: l.AXValueGetTypeID(),
		},
	}
	// A hung app must not stall the helper for the 6s system default.
	sys := l.AXUIElementCreateSystemWide()
	l.AXUIElementSetMessagingTimeout(sys, 2)
	l.CFRelease(sys)
	return p, nil
}

// pumpInterval paces the run loop; the menu bar's clicks and the popover
// need a quicker beat.
const (
	pumpInterval     = 50 * time.Millisecond
	menuPumpInterval = 15 * time.Millisecond
)

// Run serves platform calls on the calling goroutine, which must be locked
// to the main OS thread, until ctx is done.
func (p *Platform) Run(ctx context.Context) error {
	ticker := time.NewTicker(pumpInterval)
	defer ticker.Stop()
	fast := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case f := <-p.calls:
			p.pump()
			p.withPool(f)
		case <-ticker.C:
			p.pump()
		}
		if !fast && p.menu != nil {
			ticker.Reset(menuPumpInterval)
			fast = true
		}
	}
}

// pump delivers pending run loop work (workspace notifications, the web
// view's loads) and menu bar events without blocking.
func (p *Platform) pump() {
	p.l.CFRunLoopRunInMode(p.l.kCFRunLoopDefaultMode, 0, false)
	p.withPool(p.pumpEvents)
}

func (p *Platform) withPool(f func()) {
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(p.s("new"))
	defer pool.Send(p.s("drain"))
	f()
}

// do runs f on the main thread and waits for it.
func (p *Platform) do(f func()) {
	done := make(chan struct{})
	p.calls <- func() {
		defer close(done)
		f()
	}
	<-done
}

func (p *Platform) s(name string) objc.SEL {
	sel, ok := p.sel[name]
	if !ok {
		sel = objc.RegisterName(name)
		p.sel[name] = sel
	}
	return sel
}

// attr returns a cached CFString for an attribute or action name.
func (p *Platform) attr(name string) uintptr {
	ref, ok := p.attrs[name]
	if !ok {
		ref = p.l.cfString(name)
		p.attrs[name] = ref
	}
	return ref
}

func permissionError(msg string) error {
	return &proto.Error{Code: proto.CodePermission, Message: msg}
}

var errAccessibility = permissionError("macuse needs Accessibility access: System Settings › Privacy & Security › Accessibility")

func axError(code int32, what string) error {
	switch code {
	case kAXErrorAPIDisabled:
		return errAccessibility
	case kAXErrorInvalidElement:
		return &proto.Error{Code: proto.CodeElementNotFound, Message: "the element is gone; read the window state again"}
	case kAXErrorIllegalArgument:
		return &proto.Error{Code: proto.CodeInvalidParams, Message: what + ": the element doesn't take that value"}
	case kAXErrorActionUnsupported, kAXErrorAttrUnsupported:
		// Unsupported, not invalid, so the Service falls back to input.
		return &proto.Error{Code: proto.CodeUnsupported, Message: what + " is not supported by this element"}
	}
	return fmt.Errorf("%s: accessibility error %d", what, code)
}

func (p *Platform) requireTrusted() error {
	if !p.l.AXIsProcessTrusted() {
		return errAccessibility
	}
	return nil
}

// ListApps lists regular (Dock) apps.
func (p *Platform) ListApps() (apps []proto.App, err error) {
	p.do(func() {
		ws := objc.ID(objc.GetClass("NSWorkspace")).Send(p.s("sharedWorkspace"))
		running := ws.Send(p.s("runningApplications"))
		n := objc.Send[uint64](running, p.s("count"))
		for i := range n {
			app := running.Send(p.s("objectAtIndex:"), i)
			if objc.Send[int64](app, p.s("activationPolicy")) != 0 {
				continue
			}
			bundleID := p.l.goString(uintptr(app.Send(p.s("bundleIdentifier"))))
			if bundleID == "" {
				continue
			}
			pid := int(objc.Send[int32](app, p.s("processIdentifier")))
			if pid <= 0 {
				pid = p.pidOf(app)
			}
			apps = append(apps, proto.App{
				BundleID: bundleID,
				Name:     p.l.goString(uintptr(app.Send(p.s("localizedName")))),
				PID:      pid,
				Active:   objc.Send[bool](app, p.s("isActive")),
				TeamID:   p.teamID(app.Send(p.s("bundleURL"))),
			})
		}
	})
	return apps, nil
}

// pidOf finds the process running app's executable. An app whose launcher
// execs another binary, such as FreeCAD, keeps its pid, but AppKit then
// reports it as -1.
func (p *Platform) pidOf(app objc.ID) int {
	url := app.Send(p.s("executableURL"))
	if url == 0 {
		return -1
	}
	path := p.l.goString(uintptr(url.Send(p.s("path"))))
	pids := make([]int32, 4096)
	n := p.l.proc_listallpids(&pids[0], int32(len(pids)*4))
	buf := make([]byte, 4096) // PROC_PIDPATHINFO_MAXSIZE
	for _, pid := range pids[:max(n, 0)] {
		if l := p.l.proc_pidpath(pid, &buf[0], uint32(len(buf))); l > 0 && string(buf[:l]) == path {
			return int(pid)
		}
	}
	return -1
}

// teamID reads the code-signing team of the bundle at url (an NSURL, toll-free
// bridged to CFURL), cached by path.
func (p *Platform) teamID(url objc.ID) string {
	if url == 0 {
		return ""
	}
	path := p.l.goString(uintptr(url.Send(p.s("path"))))
	if id, ok := p.teamIDs[path]; ok {
		return id
	}
	var code, info uintptr
	id := ""
	if p.l.SecStaticCodeCreateWithPath(uintptr(url), 0, &code) == 0 {
		if p.l.SecCodeCopySigningInformation(code, kSecCSSigningInformation, &info) == 0 {
			id = p.l.goString(p.l.CFDictionaryGetValue(info, p.l.kSecCodeInfoTeamIdentifier))
			p.l.CFRelease(info)
		}
		p.l.CFRelease(code)
	}
	p.teamIDs[path] = id
	return id
}

// StartApp launches the app through LaunchServices in the background, so
// the user keeps the focus.
func (p *Platform) StartApp(bundleID string) error {
	out, err := exec.Command("/usr/bin/open", "-g", "-b", bundleID).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		// Older macOS says "Unable to find application"; newer names the
		// failed LaunchServices lookup.
		if strings.Contains(msg, "Unable to find application") || strings.Contains(msg, "LSCopyApplicationURLsForBundleIdentifier") {
			return &proto.Error{Code: proto.CodeAppNotFound, Message: "no app with bundle id " + bundleID + " is installed"}
		}
		return fmt.Errorf("open -b %s: %v: %s", bundleID, err, msg)
	}
	return nil
}

// Activate brings the app frontmost through its accessibility element.
func (p *Platform) Activate(app proto.App) (err error) {
	p.do(func() {
		if err = p.requireTrusted(); err != nil {
			return
		}
		el := p.l.AXUIElementCreateApplication(int32(app.PID))
		defer p.l.CFRelease(el)
		if code := p.l.AXUIElementSetAttributeValue(el, p.attr("AXFrontmost"), p.l.kCFBooleanTrue); code != kAXErrorSuccess {
			err = axError(code, "activate "+app.Name)
			return
		}
		// An app without windows takes AXFrontmost and stays behind.
		if p.stringAttr(el, "AXFrontmost") != "true" {
			ra := objc.ID(objc.GetClass("NSRunningApplication")).Send(p.s("runningApplicationWithProcessIdentifier:"), int32(app.PID))
			if ra != 0 {
				ra.Send(p.s("activateWithOptions:"), uint64(0))
			}
		}
	})
	if err == nil {
		// Give the window server a moment to route input to the app.
		time.Sleep(150 * time.Millisecond)
	}
	return err
}

// Frontmost reads the app's AXFrontmost, which the app itself reports.
func (p *Platform) Frontmost(app proto.App) (front bool) {
	p.do(func() {
		el := p.l.AXUIElementCreateApplication(int32(app.PID))
		defer p.l.CFRelease(el)
		front = p.stringAttr(el, "AXFrontmost") == "true"
	})
	return front
}

// copyAttr returns the attribute's value retained, or 0.
func (p *Platform) copyAttr(el uintptr, name string) uintptr {
	var v uintptr
	if p.l.AXUIElementCopyAttributeValue(el, p.attr(name), &v) != kAXErrorSuccess {
		return 0
	}
	return v
}

func (p *Platform) stringAttr(el uintptr, name string) string {
	v := p.copyAttr(el, name)
	if v == 0 {
		return ""
	}
	defer p.l.CFRelease(v)
	return p.describe(v)
}

// describe renders a string, number or boolean attribute value.
func (p *Platform) describe(v uintptr) string {
	switch p.l.CFGetTypeID(v) {
	case p.typeIDs.str:
		return p.l.goString(v)
	case p.typeIDs.number:
		var f float64
		if p.l.CFNumberGetValue(v, kCFNumberDoubleType, &f) {
			return strconv.FormatFloat(f, 'f', -1, 64)
		}
	case p.typeIDs.boolean:
		return strconv.FormatBool(p.l.CFBooleanGetValue(v))
	}
	return ""
}

func (p *Platform) frame(el uintptr) core.Rect {
	var r core.Rect
	if v := p.copyAttr(el, "AXPosition"); v != 0 {
		var pt cgPoint
		if p.l.axValueGetPoint(v, kAXValueCGPointType, &pt) {
			r.X, r.Y = pt.X, pt.Y
		}
		p.l.CFRelease(v)
	}
	if v := p.copyAttr(el, "AXSize"); v != 0 {
		var sz cgSize
		if p.l.axValueGetSize(v, kAXValueCGSizeType, &sz) {
			r.W, r.H = sz.W, sz.H
		}
		p.l.CFRelease(v)
	}
	return r
}

// FocusedWindow returns the app's focused window, falling back to its main
// window and then its first window.
func (p *Platform) FocusedWindow(app proto.App) (win core.Window, err error) {
	p.do(func() {
		if err = p.requireTrusted(); err != nil {
			return
		}
		el := p.l.AXUIElementCreateApplication(int32(app.PID))
		defer p.l.CFRelease(el)
		ref := p.appWindow(el)
		if ref == 0 {
			err = &proto.Error{Code: proto.CodeAppNotFound, Message: app.Name + " has no open window"}
			return
		}
		win = core.Window{Title: p.stringAttr(ref, "AXTitle"), Frame: p.frame(ref), Ref: ref, PID: app.PID}
		if p.l.axUIElementGetWindow != nil {
			p.l.axUIElementGetWindow(ref, &win.ID)
		}
	})
	return win, err
}

// appWindow returns the app element's focused window, falling back to its
// main window and then its first window, retained; or 0.
func (p *Platform) appWindow(el uintptr) uintptr {
	if ref := p.copyAttr(el, "AXFocusedWindow"); ref != 0 {
		return ref
	}
	if ref := p.copyAttr(el, "AXMainWindow"); ref != 0 {
		return ref
	}
	var ref uintptr
	if list := p.copyAttr(el, "AXWindows"); list != 0 {
		if p.l.CFGetTypeID(list) == p.typeIDs.array && p.l.CFArrayGetCount(list) > 0 {
			ref = p.l.CFRetain(p.l.CFArrayGetValueAtIndex(list, 0))
		}
		p.l.CFRelease(list)
	}
	return ref
}

// Tree walks the window's accessibility tree. Every node's Ref is retained;
// the Service releases them.
func (p *Platform) Tree(win core.Window, lim core.Limits) (root *core.Node, truncated bool, err error) {
	p.do(func() {
		count := 0
		var walk func(el uintptr, depth int) *core.Node
		walk = func(el uintptr, depth int) *core.Node {
			count++
			n := &core.Node{
				Role:  p.stringAttr(el, "AXRole"),
				Name:  p.stringAttr(el, "AXTitle"),
				Value: p.stringAttr(el, "AXValue"),
				Frame: p.frame(el),
				Ref:   p.l.CFRetain(el),
			}
			if n.Name == "" {
				n.Name = p.stringAttr(el, "AXDescription")
			}
			var actions uintptr
			if p.l.AXUIElementCopyActionNames(el, &actions) == kAXErrorSuccess && actions != 0 {
				for i := range p.l.CFArrayGetCount(actions) {
					n.Actions = append(n.Actions, p.l.goString(p.l.CFArrayGetValueAtIndex(actions, i)))
				}
				p.l.CFRelease(actions)
			}
			children := p.copyAttr(el, "AXChildren")
			if children == 0 {
				return n
			}
			defer p.l.CFRelease(children)
			if p.l.CFGetTypeID(children) != p.typeIDs.array {
				return n
			}
			for i := range p.l.CFArrayGetCount(children) {
				if depth+1 > lim.MaxDepth || count >= lim.MaxNodes {
					truncated = true
					break
				}
				n.Children = append(n.Children, walk(p.l.CFArrayGetValueAtIndex(children, i), depth+1))
			}
			return n
		}
		root = walk(win.Ref, 0)
	})
	return root, truncated, nil
}

// Capture grabs the app's windows alone when the window's id is known, so a
// popover, a window of its own, shows with it; else whatever is on screen
// within the frame.
func (p *Platform) Capture(win core.Window) (img image.Image, err error) {
	p.do(func() {
		if !p.l.CGPreflightScreenCaptureAccess() {
			err = permissionError("macuse needs Screen Recording access: System Settings › Privacy & Security › Screen & System Audio Recording")
			return
		}
		if p.l.CGWindowListCreateImage == nil {
			err = &proto.Error{Code: proto.CodeUnsupported, Message: "screen capture is unavailable on this macOS version; use capture=text"}
			return
		}
		opts, id := uint32(kCGWindowListOptionOnScreenOnly), uint32(kCGNullWindowID)
		if win.ID != 0 {
			opts, id = kCGWindowListOptionIncludingWindow, win.ID
		}
		frame := win.Frame
		rect := cgRect{cgPoint{frame.X, frame.Y}, cgSize{frame.W, frame.H}}
		var ref uintptr
		if list := p.appWindows(win); list != 0 {
			ref = p.l.CGWindowListCreateImageFromArray(rect, list, kCGWindowImageDefault)
			p.l.CFRelease(list)
		}
		if ref == 0 {
			ref = p.l.CGWindowListCreateImage(rect, opts, id, kCGWindowImageDefault)
		}
		if ref == 0 {
			err = fmt.Errorf("screen capture returned no image")
			return
		}
		defer p.l.CGImageRelease(ref)
		img = p.toRGBA(ref)
	})
	return img, err
}

// Windows lists the ids of the app's on-screen windows, front to back.
func (p *Platform) Windows(app proto.App) (ids []uint32) {
	p.do(func() { ids = p.windowIDs(app.PID) })
	return ids
}

func (p *Platform) windowIDs(pid int) []uint32 {
	info := p.l.CGWindowListCopyWindowInfo(kCGWindowListOptionOnScreenOnly, kCGNullWindowID)
	if info == 0 {
		return nil
	}
	defer p.l.CFRelease(info)
	var ids []uint32
	for i := range p.l.CFArrayGetCount(info) {
		desc := p.l.CFArrayGetValueAtIndex(info, i)
		if p.number(desc, p.l.kCGWindowOwnerPID) == float64(pid) {
			ids = append(ids, uint32(p.number(desc, p.l.kCGWindowNumber)))
		}
	}
	return ids
}

// appWindows lists the on-screen windows of the window's app, front to back,
// as a CFArray the caller releases; 0 when the window isn't among them.
func (p *Platform) appWindows(win core.Window) uintptr {
	if win.ID == 0 || p.l.CGWindowListCreateImageFromArray == nil {
		return 0
	}
	ids := p.windowIDs(win.PID)
	if !slices.Contains(ids, win.ID) {
		return 0
	}
	// A window id is stored as the value itself, so no callbacks.
	values := make([]uintptr, len(ids))
	for i, id := range ids {
		values[i] = uintptr(id)
	}
	return p.l.CFArrayCreate(0, &values[0], int64(len(values)), 0)
}

// number reads a dictionary's number, -1 when it has none.
func (p *Platform) number(dict, key uintptr) float64 {
	v := p.l.CFDictionaryGetValue(dict, key)
	var f float64
	if v == 0 || p.l.CFGetTypeID(v) != p.typeIDs.number || !p.l.CFNumberGetValue(v, kCFNumberDoubleType, &f) {
		return -1
	}
	return f
}

func (p *Platform) toRGBA(ref uintptr) *image.RGBA {
	w, h := p.l.CGImageGetWidth(ref), p.l.CGImageGetHeight(ref)
	rgba := image.NewRGBA(image.Rect(0, 0, int(w), int(h)))
	if w == 0 || h == 0 {
		return rgba
	}
	cs := p.l.CGColorSpaceCreateDeviceRGB()
	defer p.l.CGColorSpaceRelease(cs)
	ctx := p.l.CGBitmapContextCreate(&rgba.Pix[0], w, h, 8, uint64(rgba.Stride), cs,
		kCGImageAlphaPremultipliedLast|kCGBitmapByteOrder32Big)
	if ctx == 0 {
		return rgba
	}
	p.l.CGContextDrawImage(ctx, cgRect{Size: cgSize{float64(w), float64(h)}}, ref)
	p.l.CGContextRelease(ctx)
	runtime.KeepAlive(rgba)
	return rgba
}

// Press performs the element's AXPress action. An app that opens a modal
// dialog on the press can leave it unanswered; that is ErrNoReply.
func (p *Platform) Press(ref uintptr) (err error) {
	p.do(func() {
		switch code := p.l.AXUIElementPerformAction(ref, p.attr("AXPress")); code {
		case kAXErrorSuccess:
		case kAXErrorCannotComplete:
			err = fmt.Errorf("press: %w", core.ErrNoReply)
		case kAXErrorFailure:
			// Some elements, like Freeform's color swatches, list the
			// press and fail it without acting, so a click can stand in.
			err = &proto.Error{Code: proto.CodeUnsupported, Message: "the element refused the press"}
		default:
			err = axError(code, "press")
		}
	})
	return err
}

// SetValue focuses the element and sets its AXValue, as a number when the
// element holds one (sliders, scrollbars). Elements may accept a value and
// keep their own, so it reads the value back. A combo box applies its value
// only when it ends editing, which needs the focus: one that won't take the
// focus would show the value and never apply it.
func (p *Platform) SetValue(ref uintptr, value string) (err error) {
	p.do(func() {
		var settable bool
		if p.l.AXUIElementIsAttributeSettable(ref, p.attr("AXValue"), &settable) == kAXErrorSuccess && !settable {
			err = &proto.Error{Code: proto.CodeUnsupported, Message: "this element's value can't be set; click it or type into it instead"}
			return
		}
		p.l.AXUIElementSetAttributeValue(ref, p.attr("AXFocused"), p.l.kCFBooleanTrue)
		if p.stringAttr(ref, "AXRole") == "AXComboBox" && p.stringAttr(ref, "AXFocused") != "true" {
			err = &proto.Error{Code: proto.CodeUnsupported, Message: "this combo box doesn't take the focus, so it wouldn't apply the value; click it, then type the value and press Return with foreground"}
			return
		}
		v := p.l.cfString(value)
		if cur := p.copyAttr(ref, "AXValue"); cur != 0 {
			isNumber := p.l.CFGetTypeID(cur) == p.typeIDs.number
			p.l.CFRelease(cur)
			if f, perr := strconv.ParseFloat(value, 64); isNumber && perr == nil {
				p.l.CFRelease(v)
				v = p.l.CFNumberCreate(0, kCFNumberDoubleType, &f)
			}
		}
		defer p.l.CFRelease(v)
		if code := p.l.AXUIElementSetAttributeValue(ref, p.attr("AXValue"), v); code != kAXErrorSuccess {
			err = axError(code, "set value")
			return
		}
		if got := p.stringAttr(ref, "AXValue"); !sameValue(got, value) {
			err = &proto.Error{Code: proto.CodeUnsupported, Message: fmt.Sprintf("the element kept its value %q; click it or type into it instead", got)}
		}
	})
	return err
}

// sameValue reports whether an element's value reads as the one set, as
// text or as the same number.
func sameValue(got, want string) bool {
	if got == want {
		return true
	}
	g, gerr := strconv.ParseFloat(got, 64)
	w, werr := strconv.ParseFloat(want, 64)
	return gerr == nil && werr == nil && g == w
}

// Focus makes the element its app's focused element. Elements may report
// success and leave the focus where it was, so it reads the focus back.
func (p *Platform) Focus(ref uintptr) (err error) {
	p.do(func() {
		if code := p.l.AXUIElementSetAttributeValue(ref, p.attr("AXFocused"), p.l.kCFBooleanTrue); code != kAXErrorSuccess {
			err = axError(code, "focus")
			return
		}
		if p.stringAttr(ref, "AXFocused") != "true" {
			err = &proto.Error{Code: proto.CodeUnsupported, Message: "the element doesn't take the focus, so keys would go elsewhere; click it first"}
		}
	})
	return err
}

var errNoInsert = &proto.Error{Code: proto.CodeUnsupported, Message: "the element doesn't take text through accessibility"}

// InsertText focuses the element, puts the cursor at the end of its text
// and inserts there. With ref 0 it inserts at the app's focused element's cursor,
// replacing its selection, as typing would. An insert that leaves the value
// as it was counts as unsupported, unless it replaced a selection with the
// same text.
func (p *Platform) InsertText(app proto.App, ref uintptr, text string) (err error) {
	p.do(func() {
		if err = p.requireTrusted(); err != nil {
			return
		}
		el := ref
		if el == 0 {
			appEl := p.l.AXUIElementCreateApplication(int32(app.PID))
			el = p.copyAttr(appEl, "AXFocusedUIElement")
			p.l.CFRelease(appEl)
			if el == 0 {
				err = errNoInsert
				return
			}
			defer p.l.CFRelease(el)
		}
		// Focused first, so keys typed instead of an insert land there.
		if ref != 0 {
			p.l.AXUIElementSetAttributeValue(el, p.attr("AXFocused"), p.l.kCFBooleanTrue)
		}
		var settable bool
		if p.l.AXUIElementIsAttributeSettable(el, p.attr("AXSelectedText"), &settable) != kAXErrorSuccess || !settable {
			err = errNoInsert
			return
		}
		if ref != 0 {
			p.moveCursorToEnd(el)
		}
		before, selected := p.stringAttr(el, "AXValue"), p.stringAttr(el, "AXSelectedText")
		v := p.l.cfString(text)
		defer p.l.CFRelease(v)
		switch code := p.l.AXUIElementSetAttributeValue(el, p.attr("AXSelectedText"), v); code {
		case kAXErrorSuccess:
			// Some elements take the insert and drop it (Chrome's, with web
			// accessibility off), so an unchanged value means keys instead;
			// replacing a selection with the same text leaves it unchanged too.
			if selected != text && p.stringAttr(el, "AXValue") == before {
				err = errNoInsert
			}
		case kAXErrorAttrUnsupported:
			err = errNoInsert
		default:
			err = axError(code, "insert text")
		}
	})
	return err
}

// moveCursorToEnd collapses the element's selection to the end of its
// text, so an insert doesn't replace a selection left there; elements that
// don't expose it keep theirs.
func (p *Platform) moveCursorToEnd(el uintptr) {
	n := p.copyAttr(el, "AXNumberOfCharacters")
	if n == 0 {
		return
	}
	var count float64
	ok := p.l.CFGetTypeID(n) == p.typeIDs.number && p.l.CFNumberGetValue(n, kCFNumberDoubleType, &count)
	p.l.CFRelease(n)
	if !ok {
		return
	}
	r := cfRange{Location: int64(count)}
	v := p.l.axValueCreateRange(kAXValueCFRangeType, &r)
	if v == 0 {
		return
	}
	p.l.AXUIElementSetAttributeValue(el, p.attr("AXSelectedTextRange"), v)
	p.l.CFRelease(v)
}

// UserIdle reads the time since the last keyboard or mouse event. Input
// the helper posts counts too, so it also paces back-to-back actions.
func (p *Platform) UserIdle() (idle time.Duration) {
	p.do(func() {
		secs := p.l.CGEventSourceSecondsSinceLastEventType(kCGEventSourceStateHIDSystemState, kCGAnyInputEventType)
		idle = time.Duration(secs * float64(time.Second))
	})
	return idle
}

// MoveCursor warps the pointer to at, which posts no mouse event, and
// returns where it was.
func (p *Platform) MoveCursor(at core.Point) (was core.Point) {
	p.do(func() {
		ev := p.l.CGEventCreate(0)
		loc := p.l.CGEventGetLocation(ev)
		p.l.CFRelease(ev)
		p.l.CGWarpMouseCursorPosition(cgPoint{at.X, at.Y})
		was = core.Point{X: loc.X, Y: loc.Y}
	})
	return was
}

// cursorSettle lets posted mouse events land before the cursor moves back.
const cursorSettle = 50 * time.Millisecond

// keepCursor runs f, which posts mouse events, on the main thread and puts
// the pointer back where the user left it.
func (p *Platform) keepCursor(f func()) {
	p.do(func() {
		ev := p.l.CGEventCreate(0)
		at := p.l.CGEventGetLocation(ev)
		p.l.CFRelease(ev)
		f()
		time.Sleep(cursorSettle)
		p.l.CGWarpMouseCursorPosition(at)
	})
}

func (p *Platform) post(ev uintptr) {
	p.l.CGEventPost(kCGHIDEventTap, ev)
	p.l.CFRelease(ev)
}

// postTo returns a post that sends events to the app's process instead of
// the frontmost app.
func (p *Platform) postTo(app proto.App) func(ev uintptr) {
	return func(ev uintptr) {
		p.l.CGEventPostToPid(int32(app.PID), ev)
		p.l.CFRelease(ev)
	}
}

func (p *Platform) mouse(typ uint32, at core.Point, button uint32, clickState int64) {
	ev := p.l.CGEventCreateMouseEvent(0, typ, cgPoint{at.X, at.Y}, button)
	if clickState > 0 {
		p.l.CGEventSetIntegerValueField(ev, kCGMouseEventClickState, clickState)
	}
	p.post(ev)
}

// Click clicks count times at at.
func (p *Platform) Click(at core.Point, button core.Button, count int) error {
	p.keepCursor(func() {
		down, up, btn := uint32(kCGEventLeftMouseDown), uint32(kCGEventLeftMouseUp), uint32(kCGMouseButtonLeft)
		if button == core.ButtonRight {
			down, up, btn = kCGEventRightMouseDown, kCGEventRightMouseUp, kCGMouseButtonRight
		}
		p.mouse(kCGEventMouseMoved, at, kCGMouseButtonLeft, 0)
		for i := 1; i <= count; i++ {
			p.mouse(down, at, btn, int64(i))
			p.mouse(up, at, btn, int64(i))
		}
	})
	return nil
}

const dragSteps = 20

// Drag presses at from, moves to to in steps, and releases.
func (p *Platform) Drag(from, to core.Point) error {
	p.keepCursor(func() {
		p.mouse(kCGEventMouseMoved, from, kCGMouseButtonLeft, 0)
		p.mouse(kCGEventLeftMouseDown, from, kCGMouseButtonLeft, 1)
		for i := 1; i <= dragSteps; i++ {
			t := float64(i) / dragSteps
			at := core.Point{X: from.X + (to.X-from.X)*t, Y: from.Y + (to.Y-from.Y)*t}
			p.mouse(kCGEventLeftMouseDragged, at, kCGMouseButtonLeft, 0)
			time.Sleep(10 * time.Millisecond)
		}
		p.mouse(kCGEventLeftMouseUp, to, kCGMouseButtonLeft, 1)
	})
	return nil
}

// Scroll scrolls by dx/dy lines at at; positive dy scrolls content down.
func (p *Platform) Scroll(at core.Point, dx, dy int) error {
	p.keepCursor(func() {
		p.mouse(kCGEventMouseMoved, at, kCGMouseButtonLeft, 0)
		// A scroll event takes the cursor's position when it's created, and
		// the move above may not have landed yet: place it explicitly.
		ev := p.l.CGEventCreateScrollWheelEvent2(0, kCGScrollEventUnitLine, 2, int32(-dy), int32(-dx), 0)
		p.l.CGEventSetLocation(ev, cgPoint{at.X, at.Y})
		p.post(ev)
	})
	return nil
}

func flags(m core.Modifier) uint64 {
	var f uint64
	if m&core.ModCmd != 0 {
		f |= kCGEventFlagMaskCommand
	}
	if m&core.ModCtrl != 0 {
		f |= kCGEventFlagMaskControl
	}
	if m&core.ModAlt != 0 {
		f |= kCGEventFlagMaskAlternate
	}
	if m&core.ModShift != 0 {
		f |= kCGEventFlagMaskShift
	}
	return f
}

// Key presses one combo in the frontmost app.
func (p *Platform) Key(c core.Combo) error { return p.key(c, p.post) }

// KeyTo presses one combo in the app's process, which needn't be frontmost.
func (p *Platform) KeyTo(app proto.App, c core.Combo) error {
	return p.key(c, p.postTo(app))
}

func (p *Platform) key(c core.Combo, post func(ev uintptr)) error {
	code, ok := core.MacKeyCode(c.Key)
	if !ok {
		return &proto.Error{Code: proto.CodeInvalidParams, Message: "unknown key " + c.Key}
	}
	p.do(func() {
		for _, k := range keyEvents(code, c.Mods) {
			ev := p.l.CGEventCreateKeyboardEvent(0, k.code, k.down)
			p.l.CGEventSetFlags(ev, k.flags)
			post(ev)
		}
	})
	return nil
}

// modifierKeys are the virtual key codes of the left modifier keys, in the
// order a combo presses them.
var modifierKeys = []struct {
	mod  core.Modifier
	code uint16
}{
	{core.ModCmd, 55},
	{core.ModShift, 56},
	{core.ModAlt, 58},
	{core.ModCtrl, 59},
}

type keyEvent struct {
	code  uint16
	down  bool
	flags uint64
}

// keyEvents presses a combo as a keyboard does: the modifiers down, the key
// down and up, then the modifiers up. A key posted with modifier flags but
// no modifier release leaves them held in the session state, which every
// later event created without a source inherits: text typed afterwards
// arrives as shortcuts.
func keyEvents(code uint16, mods core.Modifier) []keyEvent {
	var held core.Modifier
	var events []keyEvent
	for _, m := range modifierKeys {
		if mods&m.mod != 0 {
			held |= m.mod
			events = append(events, keyEvent{m.code, true, flags(held)})
		}
	}
	events = append(events, keyEvent{code, true, flags(held)}, keyEvent{code, false, flags(held)})
	for i := len(modifierKeys) - 1; i >= 0; i-- {
		if m := modifierKeys[i]; mods&m.mod != 0 {
			held &^= m.mod
			events = append(events, keyEvent{m.code, false, flags(held)})
		}
	}
	return events
}

// typeChunk is how many characters one synthetic key event carries. Some
// apps take only an event's first character: Calculator from an event
// posted to its process, Blender even in front.
const typeChunk = 1

// Type sends text to the frontmost app as unicode key events, independent
// of the keyboard layout.
func (p *Platform) Type(text string) error {
	p.typeText(text, p.post)
	return nil
}

// How long TypeTo watches for an editor the first key opens.
const (
	editorWait = 300 * time.Millisecond
	editorPoll = 20 * time.Millisecond
)

// TypeTo sends text to the app's process, which needn't be frontmost. Some
// apps (Excel's grid) open an editor on the first key and drop its
// character in the background, so when that key moves the focus to an
// empty element, it goes again.
func (p *Platform) TypeTo(app proto.App, text string) error {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}
	post := p.postTo(app)
	first := string(runes[:1])
	role, _ := p.focusedRoleValue(app)
	p.typeText(first, post)
	for waited := time.Duration(0); waited < editorWait; waited += editorPoll {
		time.Sleep(editorPoll)
		if r, v := p.focusedRoleValue(app); r != role {
			if v == "" {
				p.typeText(first, post)
			}
			break
		}
	}
	p.typeText(string(runes[1:]), post)
	return nil
}

// focusedRoleValue reads the role and value of the app's focused element.
func (p *Platform) focusedRoleValue(app proto.App) (role, value string) {
	p.do(func() {
		appEl := p.l.AXUIElementCreateApplication(int32(app.PID))
		defer p.l.CFRelease(appEl)
		el := p.copyAttr(appEl, "AXFocusedUIElement")
		if el == 0 {
			return
		}
		defer p.l.CFRelease(el)
		role, value = p.stringAttr(el, "AXRole"), p.stringAttr(el, "AXValue")
	})
	return role, value
}

// kVKReturn is the Return key's virtual key code.
const kVKReturn = 36

func (p *Platform) typeText(text string, post func(ev uintptr)) {
	p.do(func() {
		for _, c := range typeChunks(text, typeChunk) {
			for _, down := range []bool{true, false} {
				var ev uintptr
				if c == nil {
					ev = p.l.CGEventCreateKeyboardEvent(0, kVKReturn, down)
				} else {
					ev = p.l.CGEventCreateKeyboardEvent(0, 0, down)
				}
				// No modifiers, whatever the session holds: text is never
				// a shortcut.
				p.l.CGEventSetFlags(ev, 0)
				if c != nil {
					p.l.CGEventKeyboardSetUnicodeString(ev, uint64(len(c)), &c[0])
				}
				post(ev)
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
}

// typeChunks splits text into the UTF-16 strings one key event carries, at
// most chunk characters each. A line break is a nil chunk, pressed as
// Return: editors drop a newline that comes as a character.
func typeChunks(text string, chunk int) [][]uint16 {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var chunks [][]uint16
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			chunks = append(chunks, nil)
		}
		runes := []rune(line)
		for start := 0; start < len(runes); start += chunk {
			chunks = append(chunks, utf16.Encode(runes[start:min(start+chunk, len(runes))]))
		}
	}
	return chunks
}

// Release drops the retained element handles.
func (p *Platform) Release(refs []uintptr) {
	p.do(func() {
		for _, r := range refs {
			if r != 0 {
				p.l.CFRelease(r)
			}
		}
	})
}

// Permissions reports the current grants without prompting.
func (p *Platform) Permissions() (perms proto.Permissions) {
	p.do(func() {
		perms = proto.Permissions{
			Accessibility:   p.l.AXIsProcessTrusted(),
			ScreenRecording: p.l.CGPreflightScreenCaptureAccess(),
		}
	})
	return perms
}

// RequestPermissions shows the system prompts for any missing grant.
func (p *Platform) RequestPermissions() (perms proto.Permissions) {
	p.do(func() {
		keys := []uintptr{p.l.kAXTrustedCheckOptionPrompt}
		values := []uintptr{p.l.kCFBooleanTrue}
		opts := p.l.CFDictionaryCreate(0, &keys[0], &values[0], 1,
			p.l.kCFTypeDictionaryKeyCallBacks, p.l.kCFTypeDictionaryValueCallBacks)
		perms.Accessibility = p.l.AXIsProcessTrustedWithOptions(opts)
		p.l.CFRelease(opts)
		perms.ScreenRecording = p.l.CGPreflightScreenCaptureAccess() || p.l.CGRequestScreenCaptureAccess()
	})
	return perms
}
