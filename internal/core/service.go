package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/radutopala/macuse/internal/proto"
)

// snapshot is the last state read for an app. Indexes and screenshot
// coordinates in later actions resolve against it.
type snapshot struct {
	elements []Element
	rendered string
	// clips is where each element can show, by index - 1.
	clips []Rect
	// frame is the area the screenshot covers: the window and any popover
	// hanging past its edge.
	frame Rect
	// scale is screenshot pixels per screen point; 0 when no screenshot was
	// taken.
	scale float64
}

// Service implements the helper protocol on top of a Platform.
type Service struct {
	platform     Platform
	sleep        func(time.Duration)
	startTimeout time.Duration
	limits       Limits

	mu        sync.Mutex
	snapshots map[string]*snapshot
}

// NewService returns a Service driving p. sleep paces the wait for a
// launched app to appear.
func NewService(p Platform, sleep func(time.Duration)) *Service {
	return &Service{
		platform:     p,
		sleep:        sleep,
		startTimeout: 15 * time.Second,
		limits:       DefaultLimits,
		snapshots:    map[string]*snapshot{},
	}
}

const startPoll = 250 * time.Millisecond

// Pacing for input that needs the app frontmost.
const (
	// idleBefore is how long the user must have left the keyboard and mouse
	// alone before the helper takes the focus.
	idleBefore = time.Second
	idlePoll   = 100 * time.Millisecond
	// idleWait caps the wait for the user to pause.
	idleWait = 5 * time.Second
	// settle lets the app take in the input before the focus goes back.
	settle = 300 * time.Millisecond
	// frontWait caps the wait for an activated app to become frontmost.
	frontWait = time.Second
	frontPoll = 50 * time.Millisecond
)

// Handle serves one request.
func (s *Service) Handle(req proto.Request) proto.Response {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.dispatch(req)
	if err != nil {
		return proto.Response{ID: req.ID, Error: toProtoError(err)}
	}
	// Every result is a plain struct of JSON-safe fields.
	data, _ := json.Marshal(result)
	return proto.Response{ID: req.ID, Result: data}
}

func toProtoError(err error) *proto.Error {
	var pe *proto.Error
	if errors.As(err, &pe) {
		return pe
	}
	return &proto.Error{Code: proto.CodeInternal, Message: err.Error()}
}

func invalid(format string, args ...any) error {
	return &proto.Error{Code: proto.CodeInvalidParams, Message: fmt.Sprintf(format, args...)}
}

func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return invalid("missing params")
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return invalid("bad params: %v", err)
	}
	return nil
}

func (s *Service) dispatch(req proto.Request) (any, error) {
	switch req.Method {
	case proto.MethodListApps:
		apps, err := s.platform.ListApps()
		if err != nil {
			return nil, err
		}
		return proto.AppList{Apps: apps}, nil
	case proto.MethodStartApp:
		var p proto.StartAppParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		return s.startApp(p)
	case proto.MethodGetState:
		var p proto.GetStateParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		return s.getState(p)
	case proto.MethodAction:
		var p proto.ActionParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		return s.action(p)
	case proto.MethodPermissions:
		return s.platform.Permissions(), nil
	case proto.MethodRequestPermissions:
		return s.platform.RequestPermissions(), nil
	}
	return nil, &proto.Error{Code: proto.CodeUnknownMethod, Message: req.Method}
}

// findApp looks up a running app by bundle id, and reports the frontmost
// app, nil when none is.
func (s *Service) findApp(bundleID string) (app proto.App, ok bool, front *proto.App, err error) {
	apps, err := s.platform.ListApps()
	if err != nil {
		return proto.App{}, false, nil, err
	}
	for i, a := range apps {
		if a.BundleID == bundleID {
			app, ok = a, true
		}
		if a.Active {
			front = &apps[i]
		}
	}
	return app, ok, front, nil
}

func (s *Service) runningApp(bundleID string) (proto.App, *proto.App, error) {
	if bundleID == "" {
		return proto.App{}, nil, invalid("bundle_id is required")
	}
	app, ok, front, err := s.findApp(bundleID)
	if err != nil {
		return proto.App{}, nil, err
	}
	if !ok {
		return proto.App{}, nil, &proto.Error{Code: proto.CodeAppNotFound, Message: bundleID + " is not running; start it first"}
	}
	return app, front, nil
}

// waitIdle waits for the user to pause, so input sent with the app brought
// forward neither lands in what they're typing into nor takes theirs.
func (s *Service) waitIdle() error {
	for waited := time.Duration(0); s.platform.UserIdle() < idleBefore; waited += idlePoll {
		if waited >= idleWait {
			return &proto.Error{Code: proto.CodeUserActive, Message: "the user is using the keyboard or mouse; try again in a moment"}
		}
		s.sleep(idlePoll)
	}
	return nil
}

// foreground runs input that only reaches app while it's frontmost: once the
// user pauses, it brings app forward, and afterwards, when restore is set,
// gives the focus back to front, the app the user was in. A popover or menu
// the input opened would close with the focus gone, so when the app opened a
// window it's left in front instead, and stayed reports that.
func (s *Service) foreground(app proto.App, front *proto.App, restore bool, run func() error) (stayed bool, err error) {
	if err := s.waitIdle(); err != nil {
		return false, err
	}
	if err := s.platform.Activate(app); err != nil {
		return false, err
	}
	if err := s.waitFront(app); err != nil {
		return false, err
	}
	restore = restore && front != nil && front.PID != app.PID
	var before []uint32
	if restore {
		before = s.platform.Windows(app)
	}
	err = run()
	if restore {
		s.sleep(settle)
		if opened(before, s.platform.Windows(app)) {
			return true, err
		}
		// Best effort: the action itself went through.
		_ = s.platform.Activate(*front)
	}
	return false, err
}

// opened reports whether after holds a window that before doesn't.
func opened(before, after []uint32) bool {
	for _, id := range after {
		if !slices.Contains(before, id) {
			return true
		}
	}
	return false
}

// waitFront waits for an activated app to become frontmost: input posted
// before then would land in the app the user is in.
func (s *Service) waitFront(app proto.App) error {
	for waited := time.Duration(0); !s.platform.Frontmost(app); waited += frontPoll {
		if waited >= frontWait {
			return &proto.Error{Code: proto.CodeNotFrontmost, Message: app.Name + " didn't come to the front, so the input wasn't sent; try again"}
		}
		s.sleep(frontPoll)
	}
	return nil
}

func (s *Service) startApp(p proto.StartAppParams) (proto.App, error) {
	if p.BundleID == "" {
		return proto.App{}, invalid("bundle_id is required")
	}
	if err := s.platform.StartApp(p.BundleID); err != nil {
		return proto.App{}, err
	}
	for waited := time.Duration(0); ; waited += startPoll {
		app, ok, _, err := s.findApp(p.BundleID)
		if err != nil {
			return proto.App{}, err
		}
		if ok {
			return app, nil
		}
		if waited >= s.startTimeout {
			return proto.App{}, &proto.Error{Code: proto.CodeAppNotFound, Message: p.BundleID + " did not start"}
		}
		s.sleep(startPoll)
	}
}

func (s *Service) getState(p proto.GetStateParams) (proto.State, error) {
	capture := p.Capture
	if capture == "" {
		capture = proto.CaptureText
	}
	if capture != proto.CaptureText && capture != proto.CaptureImage && capture != proto.CaptureBoth {
		return proto.State{}, invalid("capture must be text, image or both")
	}
	if p.Projection != "" && p.Projection != proto.ProjectionFull && p.Projection != proto.ProjectionDiff {
		return proto.State{}, invalid("projection must be full or diff")
	}
	app, front, err := s.runningApp(p.BundleID)
	if err != nil {
		return proto.State{}, err
	}
	win, err := s.platform.FocusedWindow(app)
	if err != nil {
		return proto.State{}, err
	}
	defer s.platform.Release([]uintptr{win.Ref})

	root, truncated, err := s.platform.Tree(win, s.limits)
	if err != nil {
		return proto.State{}, err
	}
	snap := &snapshot{elements: Flatten(root)}
	snap.clips, snap.frame = Clips(snap.elements, win.Frame)
	snap.rendered = Render(snap.elements)
	state := proto.State{
		App:       app,
		Window:    win.Title,
		Elements:  len(snap.elements),
		Truncated: truncated,
	}
	if capture != proto.CaptureImage {
		state.Tree = snap.rendered
		if prev := s.snapshots[app.BundleID]; p.Projection == proto.ProjectionDiff && prev != nil {
			state.Tree = Diff(prev.rendered, snap.rendered)
		}
	}
	if capture != proto.CaptureText {
		// The shot covers popovers too, so their elements can be seen and
		// clicked.
		win.Frame = snap.frame
		shot := func() error { return s.screenshot(&state, snap, win, p.MaxImageEdge) }
		if win.ID == 0 {
			// Without the window's id only the screen can be captured, so
			// the window must be on top.
			_, err = s.foreground(app, front, true, shot)
		} else {
			err = shot()
		}
		if err != nil {
			s.platform.Release(Refs(snap.elements))
			return proto.State{}, err
		}
	}
	s.replaceSnapshot(app.BundleID, snap)
	return state, nil
}

func (s *Service) screenshot(state *proto.State, snap *snapshot, win Window, maxEdge int) error {
	if maxEdge <= 0 {
		maxEdge = DefaultMaxImageEdge
	}
	img, err := s.platform.Capture(win)
	if err != nil {
		return err
	}
	enc, err := EncodeJPEG(img, maxEdge)
	if err != nil {
		return err
	}
	if snap.frame.W > 0 {
		snap.scale = float64(enc.Width) / snap.frame.W
	}
	state.Image = enc.Data
	state.MIMEType = "image/jpeg"
	state.Width = enc.Width
	state.Height = enc.Height
	state.Scale = snap.scale
	return nil
}

func (s *Service) replaceSnapshot(bundleID string, snap *snapshot) {
	if old := s.snapshots[bundleID]; old != nil {
		s.platform.Release(Refs(old.elements))
	}
	s.snapshots[bundleID] = snap
}

func noState(msg string) error {
	return &proto.Error{Code: proto.CodeNoState, Message: msg}
}

// element resolves an index from the app's last state read.
func (s *Service) element(bundleID string, index int) (Element, error) {
	snap := s.snapshots[bundleID]
	if snap == nil {
		return Element{}, noState("read the window state before acting on an element index")
	}
	if index < 1 || index > len(snap.elements) {
		return Element{}, &proto.Error{Code: proto.CodeElementNotFound, Message: fmt.Sprintf("no element [%d]; read the window state again", index)}
	}
	return snap.elements[index-1], nil
}

// imagePoint maps screenshot pixel coordinates to screen points.
func (s *Service) imagePoint(bundleID string, x, y float64) (Point, error) {
	snap := s.snapshots[bundleID]
	if snap == nil || snap.scale == 0 {
		return Point{}, noState("take a screenshot (capture image or both) before using coordinates")
	}
	return Point{snap.frame.X + x/snap.scale, snap.frame.Y + y/snap.scale}, nil
}

// target resolves where an action lands: for an index, the center of the
// element's part inside its popover or else the window (a text area's frame
// spans its whole document, so its own center can lie off-screen);
// otherwise the given screenshot coordinates.
func (s *Service) target(p proto.ActionParams) (Point, error) {
	if p.Index > 0 {
		el, err := s.element(p.BundleID, p.Index)
		if err != nil {
			return Point{}, err
		}
		frame := el.Node.Frame
		if frame.W == 0 || frame.H == 0 {
			return frame.Center(), nil
		}
		visible, ok := frame.Intersect(s.snapshots[p.BundleID].clips[p.Index-1])
		if !ok {
			return Point{}, &proto.Error{Code: proto.CodeElementNotFound, Message: fmt.Sprintf("element [%d] is outside the window; scroll it into view and read the window state again", p.Index)}
		}
		return visible.Center(), nil
	}
	if p.X == nil || p.Y == nil {
		return Point{}, invalid("%s needs an element index or x and y", p.Action)
	}
	return s.imagePoint(p.BundleID, *p.X, *p.Y)
}

// step is an action resolved and ready to run. background runs it while the
// app stays where it is; input sends it as keyboard or mouse input, which
// needs the app frontmost unless inputBackground. With both, input is the
// fallback for when the element doesn't support background.
type step struct {
	background func() error
	input      func() error
	// inputErr is why there's no input fallback.
	inputErr error
	// inputBackground sends input to the app's process where it is.
	inputBackground bool
	// keepFocus leaves the app frontmost after input that opens a menu,
	// which closes when the app loses the focus.
	keepFocus bool
}

func (s *Service) action(p proto.ActionParams) (proto.ActionResult, error) {
	app, front, err := s.runningApp(p.BundleID)
	if err != nil {
		return proto.ActionResult{}, err
	}
	st, err := s.actionStep(app, p)
	if err != nil {
		return proto.ActionResult{}, err
	}
	stayed, err := s.perform(app, front, st)
	if err != nil {
		if errors.Is(err, ErrNoReply) {
			// Retrying could press twice; the agent should look first.
			return proto.ActionResult{Message: s.unconfirmed(app, front, p.Action)}, nil
		}
		return proto.ActionResult{}, err
	}
	msg := fmt.Sprintf("%s done in %s", p.Action, app.Name)
	if stayed {
		msg += "; it opened a window, such as a popover or menu, so it was left in front for that to stay open"
	}
	return proto.ActionResult{Message: msg}, nil
}

// unconfirmed reports an action the app took but didn't answer, most likely
// because it opened a modal dialog. An app in the background holds such a
// dialog back until it's frontmost, so once the user pauses it's brought
// forward, and left there, for the dialog to show.
func (s *Service) unconfirmed(app proto.App, front *proto.App, action string) string {
	msg := fmt.Sprintf("%s sent to %s, which didn't confirm it in time; ", action, app.Name)
	if front != nil && front.PID == app.PID {
		return msg + "it may be showing a dialog, so read its state"
	}
	if s.waitIdle() != nil || s.platform.Activate(app) != nil {
		return msg + "it may be holding a dialog it shows only once it's in front, so read its state"
	}
	return msg + "it was brought to the front to show any dialog it holds, so read its state"
}

// perform runs the step; stayed reports the app left in front, as foreground
// does.
func (s *Service) perform(app proto.App, front *proto.App, st step) (stayed bool, err error) {
	if st.background != nil {
		err := st.background()
		var perr *proto.Error
		if err == nil || !errors.As(err, &perr) || perr.Code != proto.CodeUnsupported {
			return false, err
		}
		if st.input == nil {
			if st.inputErr != nil {
				return false, st.inputErr
			}
			return false, err
		}
	}
	if st.inputBackground {
		return false, st.input()
	}
	return s.foreground(app, front, !st.keepFocus, st.input)
}

// actionStep validates the params and resolves targets before anything is
// sent, so a bad request never touches the app.
func (s *Service) actionStep(app proto.App, p proto.ActionParams) (step, error) {
	switch p.Action {
	case proto.ActionClick, proto.ActionDoubleClick, proto.ActionRightClick:
		return s.clickStep(p)
	case proto.ActionType:
		return s.typeStep(app, p)
	case proto.ActionKey:
		combos, err := ParseKeys(p.Keys)
		if err != nil {
			return step{}, invalid("%v", err)
		}
		var ref uintptr
		if p.Index > 0 {
			el, err := s.element(p.BundleID, p.Index)
			if err != nil {
				return step{}, err
			}
			ref = el.Node.Ref
		}
		// Keys go to the app where it is, unless it would drop them there.
		foreground := p.Foreground || s.dropsKeys(app, combos)
		key := func(c Combo) error { return s.platform.KeyTo(app, c) }
		if foreground {
			key = s.platform.Key
		}
		return step{inputBackground: !foreground, input: func() error {
			// Keys reach the app's focused element; move it first.
			if ref != 0 {
				if err := s.platform.Focus(ref); err != nil {
					return err
				}
			}
			for _, c := range combos {
				if err := key(c); err != nil {
					return err
				}
			}
			return nil
		}}, nil
	case proto.ActionScroll:
		if p.DX == 0 && p.DY == 0 {
			return step{}, invalid("scroll needs dx or dy")
		}
		at, err := s.target(p)
		if err != nil {
			return step{}, err
		}
		return step{input: func() error { return s.platform.Scroll(at, p.DX, p.DY) }}, nil
	case proto.ActionDrag:
		from, err := s.target(p)
		if err != nil {
			return step{}, err
		}
		if p.ToX == nil || p.ToY == nil {
			return step{}, invalid("drag needs to_x and to_y")
		}
		to, err := s.imagePoint(p.BundleID, *p.ToX, *p.ToY)
		if err != nil {
			return step{}, err
		}
		return step{input: func() error { return s.platform.Drag(from, to) }}, nil
	case proto.ActionSetValue:
		if p.Index == 0 {
			return step{}, invalid("set_value needs an element index")
		}
		el, err := s.element(p.BundleID, p.Index)
		if err != nil {
			return step{}, err
		}
		return step{background: func() error { return s.platform.SetValue(el.Node.Ref, p.Value) }}, nil
	}
	return step{}, invalid("unknown action %q", p.Action)
}

// dropsKeys reports whether keys sent to app where it is would be lost: an
// app that isn't frontmost has no key window to take menu shortcuts (cmd
// combos), and an open menu, as the last state read showed, takes keys only
// from the keyboard.
func (s *Service) dropsKeys(app proto.App, combos []Combo) bool {
	if snap := s.snapshots[app.BundleID]; snap != nil {
		for _, el := range snap.elements {
			if el.Node.Role == "AXMenu" {
				return true
			}
		}
	}
	if app.Active {
		return false
	}
	for _, c := range combos {
		if c.Mods&ModCmd != 0 {
			return true
		}
	}
	return false
}

// typeStep inserts the text through accessibility, falling back to typing
// it as keys: to the app where it is, or with Foreground into the element
// clicked with the app frontmost.
func (s *Service) typeStep(app proto.App, p proto.ActionParams) (step, error) {
	if p.Text == "" {
		return step{}, invalid("type needs text")
	}
	var ref uintptr
	if p.Index > 0 {
		el, err := s.element(p.BundleID, p.Index)
		if err != nil {
			return step{}, err
		}
		ref = el.Node.Ref
	}
	st := step{background: func() error { return s.platform.InsertText(app, ref, p.Text) }}
	switch {
	case !p.Foreground:
		// The failed insert left the element focused for the keys.
		st.input = func() error { return s.platform.TypeTo(app, p.Text) }
		st.inputBackground = true
	case p.Index == 0:
		st.input = func() error { return s.platform.Type(p.Text) }
	default:
		at, err := s.target(p)
		if err != nil {
			st.inputErr = err
			return st, nil
		}
		st.input = func() error {
			if err := s.platform.Click(at, ButtonLeft, 1); err != nil {
				return err
			}
			return s.platform.Type(p.Text)
		}
	}
	return st, nil
}

const axPress = "AXPress"

func (s *Service) clickStep(p proto.ActionParams) (step, error) {
	if p.Action == proto.ActionClick && p.Index > 0 {
		el, err := s.element(p.BundleID, p.Index)
		if err != nil {
			return step{}, err
		}
		// Pressing through accessibility works even when the element is
		// covered or off screen. Some elements list the press and refuse
		// it; those get a click at their center.
		if el.HasAction(axPress) {
			st := step{background: func() error { return s.platform.Press(el.Node.Ref) }}
			at, err := s.target(p)
			if err != nil {
				st.inputErr = err
				return st, nil
			}
			st.input = func() error { return s.platform.Click(at, ButtonLeft, 1) }
			return st, nil
		}
	}
	at, err := s.target(p)
	if err != nil {
		return step{}, err
	}
	button, count := ButtonLeft, 1
	switch p.Action {
	case proto.ActionDoubleClick:
		count = 2
	case proto.ActionRightClick:
		button = ButtonRight
	}
	return step{
		input:     func() error { return s.platform.Click(at, button, count) },
		keepFocus: button == ButtonRight,
	}, nil
}
