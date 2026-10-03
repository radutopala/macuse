package native

import (
	"fmt"

	"github.com/ebitengine/purego/objc"
)

// Geometry is passed flat: purego passes nested structs wrongly.
type nsSize struct{ W, H float64 }

type nsRect struct{ X, Y, W, H float64 }

// AppKit constants.
const (
	nsActivationPolicyAccessory = 1
	nsVariableStatusItemLength  = -1.0
	nsPopoverBehaviorTransient  = 1
	nsImageLeft                 = 2
	nsRectEdgeMinY              = 1
	nsEventMaskAny              = ^uint64(0)
)

// popoverSize is the popup page's size in points.
var popoverSize = nsSize{W: 360, H: 440}

// menuTargetClass receives the status item's clicks; registered once per
// process.
const menuTargetClass = "MacuseMenuTarget"

// webViewClass is a WKWebView that acts on the click that activates it.
// A popover opened for a request can't activate the app without the
// user, and a plain WKWebView would spend their first click on that.
const webViewClass = "MacuseWebView"

type menuBar struct {
	app     objc.ID
	button  objc.ID
	popover objc.ID
	symbol  string
}

// StartMenuBar shows the status item, whose popover loads pageURL in a web
// view. It makes the process an accessory app: no Dock icon, no menu.
func (p *Platform) StartMenuBar(pageURL string) (err error) {
	p.do(func() { err = p.startMenuBar(pageURL) })
	return err
}

func (p *Platform) startMenuBar(pageURL string) error {
	if p.menu != nil {
		return nil
	}
	app := objc.ID(objc.GetClass("NSApplication")).Send(p.s("sharedApplication"))
	app.Send(p.s("setActivationPolicy:"), int64(nsActivationPolicyAccessory))
	app.Send(p.s("finishLaunching"))

	cls, err := registerClass(menuTargetClass, "NSObject", objc.MethodDef{
		Cmd: p.s("toggle:"),
		Fn:  func(objc.ID, objc.SEL, objc.ID) { p.togglePopover() },
	})
	if err != nil {
		return err
	}
	target := objc.ID(cls).Send(p.s("new"))
	webCls, err := registerClass(webViewClass, "WKWebView", objc.MethodDef{
		Cmd: p.s("acceptsFirstMouse:"),
		Fn:  func(objc.ID, objc.SEL, objc.ID) bool { return true },
	})
	if err != nil {
		return err
	}

	bar := objc.ID(objc.GetClass("NSStatusBar")).Send(p.s("systemStatusBar"))
	item := bar.Send(p.s("statusItemWithLength:"), float64(nsVariableStatusItemLength))
	item.Send(p.s("retain"))
	button := item.Send(p.s("button"))
	button.Send(p.s("setImagePosition:"), uint64(nsImageLeft))
	button.Send(p.s("setTarget:"), target)
	button.Send(p.s("setAction:"), p.s("toggle:"))

	web := objc.ID(webCls).Send(p.s("alloc")).Send(p.s("initWithFrame:configuration:"),
		nsRect{W: popoverSize.W, H: popoverSize.H}, objc.ID(objc.GetClass("WKWebViewConfiguration")).Send(p.s("new")))
	url := objc.ID(objc.GetClass("NSURL")).Send(p.s("URLWithString:"), objc.ID(p.attr(pageURL)))
	if url == 0 {
		return fmt.Errorf("bad popup URL %q", pageURL)
	}
	web.Send(p.s("loadRequest:"), objc.ID(objc.GetClass("NSURLRequest")).Send(p.s("requestWithURL:"), url))

	vc := objc.ID(objc.GetClass("NSViewController")).Send(p.s("new"))
	vc.Send(p.s("setView:"), web)
	popover := objc.ID(objc.GetClass("NSPopover")).Send(p.s("new"))
	popover.Send(p.s("setBehavior:"), int64(nsPopoverBehaviorTransient))
	popover.Send(p.s("setContentSize:"), popoverSize)
	popover.Send(p.s("setContentViewController:"), vc)

	p.menu = &menuBar{app: app, button: button, popover: popover}
	p.updateMenuBar(MenuState{})
	return nil
}

// registerClass registers name, once per process.
func registerClass(name, super string, methods ...objc.MethodDef) (objc.Class, error) {
	if cls := objc.GetClass(name); cls != 0 {
		return cls, nil
	}
	cls, err := objc.RegisterClass(name, objc.GetClass(super), nil, nil, methods)
	if err != nil {
		return 0, fmt.Errorf("register %s: %w", name, err)
	}
	return cls, nil
}

// UpdateMenuBar redraws the icon for st.
func (p *Platform) UpdateMenuBar(st MenuState) {
	p.do(func() { p.updateMenuBar(st) })
}

func (p *Platform) updateMenuBar(st MenuState) {
	m := p.menu
	if m == nil {
		return
	}
	if sym := st.Symbol(); sym != m.symbol {
		img := objc.ID(objc.GetClass("NSImage")).Send(p.s("imageWithSystemSymbolName:accessibilityDescription:"),
			objc.ID(p.attr(sym)), objc.ID(p.attr("macuse")))
		img.Send(p.s("setTemplate:"), true)
		m.button.Send(p.s("setImage:"), img)
		m.symbol = sym
	}
	m.button.Send(p.s("setTitle:"), objc.ID(p.attr(st.Title())))
}

// ShowPopover opens the popover, as when a request needs the user.
func (p *Platform) ShowPopover() {
	p.do(func() {
		if m := p.menu; m != nil && !objc.Send[bool](m.popover, p.s("isShown")) {
			p.showPopover()
		}
	})
}

// togglePopover runs on the main thread, from the click's event.
func (p *Platform) togglePopover() {
	m := p.menu
	if objc.Send[bool](m.popover, p.s("isShown")) {
		m.popover.Send(p.s("performClose:"), objc.ID(0))
		return
	}
	p.showPopover()
}

func (p *Platform) showPopover() {
	m := p.menu
	bounds := objc.Send[nsRect](m.button, p.s("bounds"))
	m.popover.Send(p.s("showRelativeToRect:ofView:preferredEdge:"), bounds, m.button, uint64(nsRectEdgeMinY))
	// An accessory app must activate for the popover to take clicks and
	// close when the user clicks elsewhere.
	m.app.Send(p.s("activateIgnoringOtherApps:"), true)
}

// pumpEvents hands the menu bar's pending events to AppKit.
func (p *Platform) pumpEvents() {
	m := p.menu
	if m == nil {
		return
	}
	past := objc.ID(objc.GetClass("NSDate")).Send(p.s("distantPast"))
	mode := objc.ID(p.l.kCFRunLoopDefaultMode)
	for {
		ev := m.app.Send(p.s("nextEventMatchingMask:untilDate:inMode:dequeue:"), nsEventMaskAny, past, mode, true)
		if ev == 0 {
			break
		}
		m.app.Send(p.s("sendEvent:"), ev)
	}
	m.app.Send(p.s("updateWindows"))
}
