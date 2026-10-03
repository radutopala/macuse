package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/radutopala/macuse/internal/proto"
)

type ServiceSuite struct {
	suite.Suite
	p      *mockPlatform
	svc    *Service
	sleeps []time.Duration
}

func TestServiceSuite(t *testing.T) {
	suite.Run(t, new(ServiceSuite))
}

var textEdit = proto.App{BundleID: "com.apple.TextEdit", Name: "TextEdit", PID: 42, TeamID: "APPLE"}

// finder is frontmost: the user is chatting while the agent acts.
var finder = proto.App{BundleID: "com.apple.finder", Name: "Finder", PID: 7, Active: true}

var testFrame = Rect{X: 100, Y: 50, W: 100, H: 50}

// testWin has a window id, so it's captured without coming forward.
var testWin = Window{Title: "Doc", Frame: testFrame, Ref: 9, ID: 5}

func (s *ServiceSuite) SetupTest() {
	s.p = new(mockPlatform)
	s.sleeps = nil
	s.svc = NewService(s.p, func(d time.Duration) { s.sleeps = append(s.sleeps, d) })
}

func (s *ServiceSuite) TearDownTest() {
	s.p.AssertExpectations(s.T())
}

func (s *ServiceSuite) call(method string, params any) proto.Response {
	var raw json.RawMessage
	if params != nil {
		raw, _ = json.Marshal(params)
	}
	return s.svc.Handle(proto.Request{ID: 7, Method: method, Params: raw})
}

func (s *ServiceSuite) ok(resp proto.Response, out any) {
	require.Nil(s.T(), resp.Error)
	require.Equal(s.T(), uint64(7), resp.ID)
	require.NoError(s.T(), json.Unmarshal(resp.Result, out))
}

func (s *ServiceSuite) fails(resp proto.Response, code, msg string) {
	require.NotNil(s.T(), resp.Error)
	require.Equal(s.T(), code, resp.Error.Code)
	require.Contains(s.T(), resp.Error.Message, msg)
	require.Nil(s.T(), resp.Result)
}

func (s *ServiceSuite) running() {
	s.p.On("ListApps").Return([]proto.App{finder, textEdit}, nil)
}

// read takes a text+image state so later actions can use indexes and
// coordinates.
func (s *ServiceSuite) read() {
	s.running()
	s.p.On("FocusedWindow", textEdit).Return(testWin, nil).Once()
	s.p.On("Release", []uintptr{9}).Once()
	s.p.On("Tree", testWin, DefaultLimits).Return(sampleTree(), false, nil).Once()
	s.p.On("Capture", testWin).Return(solid(200, 100), nil).Once()
	var st proto.State
	s.ok(s.call(proto.MethodGetState, proto.GetStateParams{BundleID: textEdit.BundleID, Capture: proto.CaptureBoth}), &st)
}

// foreground expects input sent with TextEdit brought forward once the user
// is idle, and the focus given back to Finder when restore is set.
func (s *ServiceSuite) foreground(restore bool) {
	s.p.On("UserIdle").Return(time.Hour).Once()
	s.p.On("Activate", textEdit).Return(nil).Once()
	s.p.On("Frontmost", textEdit).Return(true).Once()
	if restore {
		s.p.On("Windows", textEdit).Return([]uint32{5}).Twice()
		s.p.On("Activate", finder).Return(nil).Once()
	}
}

func ptr(v float64) *float64 { return &v }

// --- dispatch ---

func (s *ServiceSuite) TestListApps() {
	s.running()
	var out proto.AppList
	s.ok(s.call(proto.MethodListApps, nil), &out)
	require.Len(s.T(), out.Apps, 2)
}

func (s *ServiceSuite) TestListAppsError() {
	s.p.On("ListApps").Return(nil, errors.New("boom"))
	s.fails(s.call(proto.MethodListApps, nil), proto.CodeInternal, "boom")
}

func (s *ServiceSuite) TestUnknownMethod() {
	s.fails(s.call("nope", nil), proto.CodeUnknownMethod, "nope")
}

func (s *ServiceSuite) TestParamErrors() {
	for _, m := range []string{proto.MethodStartApp, proto.MethodGetState, proto.MethodAction} {
		s.fails(s.call(m, nil), proto.CodeInvalidParams, "missing params")
		resp := s.svc.Handle(proto.Request{Method: m, Params: json.RawMessage(`[1]`)})
		s.fails(resp, proto.CodeInvalidParams, "bad params")
	}
}

func (s *ServiceSuite) TestPermissions() {
	s.p.On("Permissions").Return(proto.Permissions{Accessibility: true})
	s.p.On("RequestPermissions").Return(proto.Permissions{ScreenRecording: true})
	var got proto.Permissions
	s.ok(s.call(proto.MethodPermissions, nil), &got)
	require.True(s.T(), got.Accessibility)
	s.ok(s.call(proto.MethodRequestPermissions, nil), &got)
	require.Equal(s.T(), proto.Permissions{ScreenRecording: true}, got)
}

func (s *ServiceSuite) TestProtoErrorPassesThrough() {
	s.p.On("ListApps").Return(nil, &proto.Error{Code: proto.CodePermission, Message: "grant it"})
	s.fails(s.call(proto.MethodListApps, nil), proto.CodePermission, "grant it")
}

// --- start_app ---

func (s *ServiceSuite) TestStartAppRequiresBundle() {
	s.fails(s.call(proto.MethodStartApp, proto.StartAppParams{}), proto.CodeInvalidParams, "bundle_id is required")
}

func (s *ServiceSuite) TestStartAppLaunchError() {
	s.p.On("StartApp", "x").Return(errors.New("no such app"))
	s.fails(s.call(proto.MethodStartApp, proto.StartAppParams{BundleID: "x"}), proto.CodeInternal, "no such app")
}

func (s *ServiceSuite) TestStartAppWaitsForApp() {
	s.p.On("StartApp", textEdit.BundleID).Return(nil)
	s.p.On("ListApps").Return([]proto.App{}, nil).Twice()
	s.running()
	var app proto.App
	s.ok(s.call(proto.MethodStartApp, proto.StartAppParams{BundleID: textEdit.BundleID}), &app)
	require.Equal(s.T(), textEdit, app)
	require.Equal(s.T(), []time.Duration{startPoll, startPoll}, s.sleeps)
}

func (s *ServiceSuite) TestStartAppListError() {
	s.p.On("StartApp", "x").Return(nil)
	s.p.On("ListApps").Return(nil, errors.New("ws down"))
	s.fails(s.call(proto.MethodStartApp, proto.StartAppParams{BundleID: "x"}), proto.CodeInternal, "ws down")
}

func (s *ServiceSuite) TestStartAppTimesOut() {
	s.svc.startTimeout = 2 * startPoll
	s.p.On("StartApp", "x").Return(nil)
	s.p.On("ListApps").Return([]proto.App{}, nil)
	s.fails(s.call(proto.MethodStartApp, proto.StartAppParams{BundleID: "x"}), proto.CodeAppNotFound, "x did not start")
	require.Len(s.T(), s.sleeps, 2)
}

// --- get_state ---

func (s *ServiceSuite) TestGetStateValidation() {
	s.fails(s.call(proto.MethodGetState, proto.GetStateParams{BundleID: "x", Capture: "video"}), proto.CodeInvalidParams, "capture must be")
	s.fails(s.call(proto.MethodGetState, proto.GetStateParams{BundleID: "x", Projection: "tree"}), proto.CodeInvalidParams, "projection must be")
	s.fails(s.call(proto.MethodGetState, proto.GetStateParams{}), proto.CodeInvalidParams, "bundle_id is required")
}

func (s *ServiceSuite) TestGetStateAppNotRunning() {
	s.running()
	s.fails(s.call(proto.MethodGetState, proto.GetStateParams{BundleID: "gone"}), proto.CodeAppNotFound, "gone is not running")
}

func (s *ServiceSuite) TestGetStateListError() {
	s.p.On("ListApps").Return(nil, errors.New("ws down"))
	s.fails(s.call(proto.MethodGetState, proto.GetStateParams{BundleID: "x"}), proto.CodeInternal, "ws down")
}

func (s *ServiceSuite) TestGetStateText() {
	s.running()
	win := Window{Title: "Doc", Frame: testFrame, Ref: 9}
	s.p.On("FocusedWindow", textEdit).Return(win, nil)
	s.p.On("Release", []uintptr{9})
	s.p.On("Tree", win, DefaultLimits).Return(sampleTree(), true, nil).Once()

	var st proto.State
	s.ok(s.call(proto.MethodGetState, proto.GetStateParams{BundleID: textEdit.BundleID}), &st)
	require.Equal(s.T(), "Doc", st.Window)
	require.Equal(s.T(), 4, st.Elements)
	require.True(s.T(), st.Truncated)
	require.Contains(s.T(), st.Tree, `[3] Button "OK"`)
	require.Empty(s.T(), st.Image)

	// A second read with the diff projection reports changes only, and the
	// first snapshot's handles are released.
	changed := sampleTree()
	changed.Children[1].Name = "Cancel"
	s.p.On("Tree", win, DefaultLimits).Return(changed, false, nil).Once()
	s.p.On("Release", []uintptr{1, 2, 3, 4}).Once()
	s.ok(s.call(proto.MethodGetState, proto.GetStateParams{BundleID: textEdit.BundleID, Projection: proto.ProjectionDiff}), &st)
	require.Contains(s.T(), st.Tree, `-  [3] Button "OK"`)
	require.Contains(s.T(), st.Tree, `+  [3] Button "Cancel"`)
}

func (s *ServiceSuite) TestGetStateDiffWithoutPreviousIsFull() {
	s.running()
	win := Window{Frame: testFrame, Ref: 9}
	s.p.On("FocusedWindow", textEdit).Return(win, nil)
	s.p.On("Release", []uintptr{9})
	s.p.On("Tree", win, DefaultLimits).Return(sampleTree(), false, nil)
	var st proto.State
	s.ok(s.call(proto.MethodGetState, proto.GetStateParams{BundleID: textEdit.BundleID, Projection: proto.ProjectionDiff}), &st)
	require.Contains(s.T(), st.Tree, `[1] Window "Untitled"`)
}

func (s *ServiceSuite) TestGetStateImage() {
	s.running()
	win := Window{Frame: Rect{W: 100, H: 50}, Ref: 9, ID: 5}
	s.p.On("FocusedWindow", textEdit).Return(win, nil)
	s.p.On("Release", []uintptr{9})
	s.p.On("Tree", win, DefaultLimits).Return(sampleTree(), false, nil)
	s.p.On("Capture", win).Return(solid(200, 100), nil)
	var st proto.State
	s.ok(s.call(proto.MethodGetState, proto.GetStateParams{BundleID: textEdit.BundleID, Capture: proto.CaptureImage, MaxImageEdge: 100}), &st)
	require.Empty(s.T(), st.Tree)
	require.Equal(s.T(), "image/jpeg", st.MIMEType)
	require.Equal(s.T(), 100, st.Width)
	require.Equal(s.T(), 50, st.Height)
	require.InDelta(s.T(), 1.0, st.Scale, 0.0001)
	require.NotEmpty(s.T(), st.Image)
	require.Empty(s.T(), s.sleeps, "the window is captured where it is")
}

func (s *ServiceSuite) TestGetStateImageWithoutWindowIDComesForward() {
	s.running()
	win := Window{Ref: 9}
	s.p.On("FocusedWindow", textEdit).Return(win, nil)
	s.p.On("Release", []uintptr{9})
	s.p.On("Tree", win, DefaultLimits).Return(sampleTree(), false, nil)
	s.foreground(true)
	s.p.On("Capture", win).Return(solid(4, 4), nil)
	var st proto.State
	s.ok(s.call(proto.MethodGetState, proto.GetStateParams{BundleID: textEdit.BundleID, Capture: proto.CaptureImage}), &st)
	require.Zero(s.T(), st.Scale, "a zero-width frame has no scale")
	require.Equal(s.T(), []time.Duration{settle}, s.sleeps)
}

func (s *ServiceSuite) TestGetStateErrors() {
	noID := Window{Frame: testFrame, Ref: 9}
	tests := []struct {
		name  string
		setup func()
		msg   string
	}{
		{name: "window", setup: func() {
			s.p.On("FocusedWindow", textEdit).Return(Window{}, errors.New("no window"))
		}, msg: "no window"},
		{name: "tree", setup: func() {
			s.p.On("FocusedWindow", textEdit).Return(testWin, nil)
			s.p.On("Release", []uintptr{9})
			s.p.On("Tree", testWin, DefaultLimits).Return(nil, false, errors.New("tree failed"))
		}, msg: "tree failed"},
		{name: "activate", setup: func() {
			s.p.On("FocusedWindow", textEdit).Return(noID, nil)
			s.p.On("Release", []uintptr{9})
			s.p.On("Tree", noID, DefaultLimits).Return(sampleTree(), false, nil)
			s.p.On("UserIdle").Return(time.Hour)
			s.p.On("Activate", textEdit).Return(errors.New("activate failed"))
			s.p.On("Release", []uintptr{1, 2, 3, 4})
		}, msg: "activate failed"},
		{name: "capture", setup: func() {
			s.p.On("FocusedWindow", textEdit).Return(testWin, nil)
			s.p.On("Release", []uintptr{9})
			s.p.On("Tree", testWin, DefaultLimits).Return(sampleTree(), false, nil)
			s.p.On("Capture", testWin).Return(nil, errors.New("capture failed"))
			s.p.On("Release", []uintptr{1, 2, 3, 4})
		}, msg: "capture failed"},
		{name: "encode", setup: func() {
			s.p.On("FocusedWindow", textEdit).Return(testWin, nil)
			s.p.On("Release", []uintptr{9})
			s.p.On("Tree", testWin, DefaultLimits).Return(sampleTree(), false, nil)
			s.p.On("Capture", testWin).Return(solid(70000, 1), nil)
			s.p.On("Release", []uintptr{1, 2, 3, 4})
		}, msg: "jpeg"},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			s.running()
			tc.setup()
			resp := s.call(proto.MethodGetState, proto.GetStateParams{BundleID: textEdit.BundleID, Capture: proto.CaptureBoth, MaxImageEdge: 100000})
			s.fails(resp, proto.CodeInternal, tc.msg)
			s.p.AssertExpectations(s.T())
		})
	}
}

// --- action ---

func (s *ServiceSuite) act(p proto.ActionParams) proto.Response {
	p.BundleID = textEdit.BundleID
	return s.call(proto.MethodAction, p)
}

func (s *ServiceSuite) TestActionAppNotRunning() {
	s.running()
	s.fails(s.call(proto.MethodAction, proto.ActionParams{BundleID: "gone", Action: proto.ActionKey, Keys: "a"}), proto.CodeAppNotFound, "not running")
}

func (s *ServiceSuite) TestActionsNeedState() {
	s.running()
	s.fails(s.act(proto.ActionParams{Action: proto.ActionClick, Index: 1}), proto.CodeNoState, "read the window state")
	s.fails(s.act(proto.ActionParams{Action: proto.ActionClick, X: ptr(1), Y: ptr(1)}), proto.CodeNoState, "take a screenshot")
}

func (s *ServiceSuite) TestCoordinatesNeedScreenshot() {
	s.running()
	win := Window{Frame: testFrame, Ref: 9}
	s.p.On("FocusedWindow", textEdit).Return(win, nil)
	s.p.On("Release", []uintptr{9})
	s.p.On("Tree", win, DefaultLimits).Return(sampleTree(), false, nil)
	var st proto.State
	s.ok(s.call(proto.MethodGetState, proto.GetStateParams{BundleID: textEdit.BundleID}), &st)
	s.fails(s.act(proto.ActionParams{Action: proto.ActionClick, X: ptr(1), Y: ptr(1)}), proto.CodeNoState, "take a screenshot")
}

func (s *ServiceSuite) TestActionValidation() {
	s.read()
	tests := []struct {
		name string
		p    proto.ActionParams
		code string
		msg  string
	}{
		{name: "unknown", p: proto.ActionParams{Action: "dance"}, code: proto.CodeInvalidParams, msg: `unknown action "dance"`},
		{name: "click target", p: proto.ActionParams{Action: proto.ActionClick}, code: proto.CodeInvalidParams, msg: "click needs an element index or x and y"},
		{name: "click index", p: proto.ActionParams{Action: proto.ActionClick, Index: 9}, code: proto.CodeElementNotFound, msg: "no element [9]"},
		{name: "double click index", p: proto.ActionParams{Action: proto.ActionDoubleClick, Index: 9}, code: proto.CodeElementNotFound, msg: "no element [9]"},
		{name: "type text", p: proto.ActionParams{Action: proto.ActionType}, code: proto.CodeInvalidParams, msg: "type needs text"},
		{name: "type index", p: proto.ActionParams{Action: proto.ActionType, Text: "x", Index: 9}, code: proto.CodeElementNotFound, msg: "no element"},
		{name: "keys", p: proto.ActionParams{Action: proto.ActionKey, Keys: "cmd+nope"}, code: proto.CodeInvalidParams, msg: "unknown key"},
		{name: "keys index", p: proto.ActionParams{Action: proto.ActionKey, Keys: "a", Index: 9}, code: proto.CodeElementNotFound, msg: "no element"},
		{name: "scroll delta", p: proto.ActionParams{Action: proto.ActionScroll}, code: proto.CodeInvalidParams, msg: "scroll needs dx or dy"},
		{name: "scroll target", p: proto.ActionParams{Action: proto.ActionScroll, DY: 3, Index: 9}, code: proto.CodeElementNotFound, msg: "no element"},
		{name: "scroll outside the window", p: proto.ActionParams{Action: proto.ActionScroll, DY: 3, Index: 4}, code: proto.CodeElementNotFound, msg: "element [4] is outside the window"},
		{name: "drag from", p: proto.ActionParams{Action: proto.ActionDrag}, code: proto.CodeInvalidParams, msg: "drag needs an element index"},
		{name: "drag to", p: proto.ActionParams{Action: proto.ActionDrag, Index: 1}, code: proto.CodeInvalidParams, msg: "drag needs to_x and to_y"},
		{name: "set value index", p: proto.ActionParams{Action: proto.ActionSetValue}, code: proto.CodeInvalidParams, msg: "set_value needs an element index"},
		{name: "set value element", p: proto.ActionParams{Action: proto.ActionSetValue, Index: 9}, code: proto.CodeElementNotFound, msg: "no element"},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.fails(s.act(tc.p), tc.code, tc.msg)
		})
	}
}

func (s *ServiceSuite) TestDragToNeedsScreenshot() {
	s.running()
	win := Window{Frame: testFrame, Ref: 9}
	s.p.On("FocusedWindow", textEdit).Return(win, nil)
	s.p.On("Release", []uintptr{9})
	s.p.On("Tree", win, DefaultLimits).Return(sampleTree(), false, nil)
	var st proto.State
	s.ok(s.call(proto.MethodGetState, proto.GetStateParams{BundleID: textEdit.BundleID}), &st)
	s.fails(s.act(proto.ActionParams{Action: proto.ActionDrag, Index: 2, ToX: ptr(1), ToY: ptr(1)}), proto.CodeNoState, "take a screenshot")
}

func (s *ServiceSuite) TestActions() {
	// Element [2] is the text area at {150,60,100,200}, running past the
	// bottom of the window {100,50,100,50}; its visible part's center is
	// {175,80}. Element [4] lies outside the window.
	// Screenshot pixels map at scale 2 from the window origin {100,50}.
	center := Point{175, 80}
	unsupported := &proto.Error{Code: proto.CodeUnsupported, Message: "no"}
	const (
		background = iota
		restore
		keep
	)
	tests := []struct {
		name   string
		p      proto.ActionParams
		focus  int
		expect func()
	}{
		{name: "click presses", p: proto.ActionParams{Action: proto.ActionClick, Index: 3}, expect: func() {
			s.p.On("Press", uintptr(3)).Return(nil)
		}},
		{name: "click falls back to the center when the press is refused", p: proto.ActionParams{Action: proto.ActionClick, Index: 3}, focus: restore, expect: func() {
			s.p.On("Press", uintptr(3)).Return(unsupported)
			s.p.On("Click", Point{}, ButtonLeft, 1).Return(nil)
		}},
		{name: "click without press uses the center", p: proto.ActionParams{Action: proto.ActionClick, Index: 2}, focus: restore, expect: func() {
			s.p.On("Click", center, ButtonLeft, 1).Return(nil)
		}},
		{name: "click at coordinates", p: proto.ActionParams{Action: proto.ActionClick, X: ptr(20), Y: ptr(10)}, focus: restore, expect: func() {
			s.p.On("Click", Point{110, 55}, ButtonLeft, 1).Return(nil)
		}},
		{name: "double click", p: proto.ActionParams{Action: proto.ActionDoubleClick, Index: 3}, focus: restore, expect: func() {
			s.p.On("Click", Point{}, ButtonLeft, 2).Return(nil)
		}},
		{name: "right click keeps the menu open", p: proto.ActionParams{Action: proto.ActionRightClick, Index: 2}, focus: keep, expect: func() {
			s.p.On("Click", center, ButtonRight, 1).Return(nil)
		}},
		{name: "type inserts at the focus", p: proto.ActionParams{Action: proto.ActionType, Text: "hi"}, expect: func() {
			s.p.On("InsertText", textEdit, uintptr(0), "hi").Return(nil)
		}},
		{name: "type falls back to keys", p: proto.ActionParams{Action: proto.ActionType, Text: "hi"}, expect: func() {
			s.p.On("InsertText", textEdit, uintptr(0), "hi").Return(unsupported)
			s.p.On("TypeTo", textEdit, "hi").Return(nil)
		}},
		{name: "type falls back to keys in the foreground", p: proto.ActionParams{Action: proto.ActionType, Text: "hi", Foreground: true}, focus: restore, expect: func() {
			s.p.On("InsertText", textEdit, uintptr(0), "hi").Return(unsupported)
			s.p.On("Type", "hi").Return(nil)
		}},
		{name: "type inserts into element", p: proto.ActionParams{Action: proto.ActionType, Text: "hi", Index: 2}, expect: func() {
			s.p.On("InsertText", textEdit, uintptr(2), "hi").Return(nil)
		}},
		{name: "type into element falls back to keys", p: proto.ActionParams{Action: proto.ActionType, Text: "hi", Index: 2}, expect: func() {
			s.p.On("InsertText", textEdit, uintptr(2), "hi").Return(unsupported)
			s.p.On("TypeTo", textEdit, "hi").Return(nil)
		}},
		{name: "type into element falls back to click and keys in the foreground", p: proto.ActionParams{Action: proto.ActionType, Text: "hi", Index: 2, Foreground: true}, focus: restore, expect: func() {
			s.p.On("InsertText", textEdit, uintptr(2), "hi").Return(unsupported)
			s.p.On("Click", center, ButtonLeft, 1).Return(nil)
			s.p.On("Type", "hi").Return(nil)
		}},
		{name: "type inserts into element outside the window", p: proto.ActionParams{Action: proto.ActionType, Text: "hi", Index: 4}, expect: func() {
			s.p.On("InsertText", textEdit, uintptr(4), "hi").Return(nil)
		}},
		{name: "keys", p: proto.ActionParams{Action: proto.ActionKey, Keys: "ctrl+a backspace"}, expect: func() {
			s.p.On("KeyTo", textEdit, Combo{Mods: ModCtrl, Key: "a"}).Return(nil)
			s.p.On("KeyTo", textEdit, Combo{Key: "backspace"}).Return(nil)
		}},
		{name: "menu shortcut to an app in the background", p: proto.ActionParams{Action: proto.ActionKey, Keys: "cmd+a backspace"}, focus: restore, expect: func() {
			s.p.On("Key", Combo{Mods: ModCmd, Key: "a"}).Return(nil)
			s.p.On("Key", Combo{Key: "backspace"}).Return(nil)
		}},
		{name: "keys in the foreground", p: proto.ActionParams{Action: proto.ActionKey, Keys: "cmd+a backspace", Foreground: true}, focus: restore, expect: func() {
			s.p.On("Key", Combo{Mods: ModCmd, Key: "a"}).Return(nil)
			s.p.On("Key", Combo{Key: "backspace"}).Return(nil)
		}},
		{name: "keys to element", p: proto.ActionParams{Action: proto.ActionKey, Keys: "a", Index: 2}, expect: func() {
			s.p.On("Focus", uintptr(2)).Return(nil)
			s.p.On("KeyTo", textEdit, Combo{Key: "a"}).Return(nil)
		}},
		{name: "keys to element in the foreground", p: proto.ActionParams{Action: proto.ActionKey, Keys: "a", Index: 2, Foreground: true}, focus: restore, expect: func() {
			s.p.On("Focus", uintptr(2)).Return(nil)
			s.p.On("Key", Combo{Key: "a"}).Return(nil)
		}},
		{name: "scroll", p: proto.ActionParams{Action: proto.ActionScroll, Index: 2, DY: 40}, focus: restore, expect: func() {
			s.p.On("Scroll", center, 0, 40).Return(nil)
		}},
		{name: "drag", p: proto.ActionParams{Action: proto.ActionDrag, X: ptr(0), Y: ptr(0), ToX: ptr(200), ToY: ptr(100)}, focus: restore, expect: func() {
			s.p.On("Drag", Point{100, 50}, Point{200, 100}).Return(nil)
		}},
		{name: "set value", p: proto.ActionParams{Action: proto.ActionSetValue, Index: 2, Value: "new"}, expect: func() {
			s.p.On("SetValue", uintptr(2), "new").Return(nil)
		}},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			s.read()
			if tc.focus != background {
				s.foreground(tc.focus == restore)
			}
			tc.expect()
			var res proto.ActionResult
			s.ok(s.act(tc.p), &res)
			require.Equal(s.T(), tc.p.Action+" done in TextEdit", res.Message)
			s.p.AssertExpectations(s.T())
			if tc.focus == restore {
				require.Equal(s.T(), []time.Duration{settle}, s.sleeps)
			} else {
				require.Empty(s.T(), s.sleeps)
			}
		})
	}
}

func (s *ServiceSuite) TestUnconfirmedPressIsNotRetried() {
	const sent = "click sent to TextEdit, which didn't confirm it in time; "
	frontTextEdit := textEdit
	frontTextEdit.Active = true
	tests := []struct {
		name  string
		app   proto.App
		setup func()
		want  string
	}{
		{
			name: "frontmost app",
			app:  frontTextEdit,
			want: sent + "it may be showing a dialog, so read its state",
		},
		{
			name: "background app brought forward",
			app:  textEdit,
			setup: func() {
				s.p.On("UserIdle").Return(time.Hour).Once()
				s.p.On("Activate", textEdit).Return(nil).Once()
			},
			want: sent + "it was brought to the front to show any dialog it holds, so read its state",
		},
		{
			name:  "background app while the user is active",
			app:   textEdit,
			setup: func() { s.p.On("UserIdle").Return(time.Duration(0)) },
			want:  sent + "it may be holding a dialog it shows only once it's in front, so read its state",
		},
		{
			name: "background app that won't come forward",
			app:  textEdit,
			setup: func() {
				s.p.On("UserIdle").Return(time.Hour).Once()
				s.p.On("Activate", textEdit).Return(errors.New("gone")).Once()
			},
			want: sent + "it may be holding a dialog it shows only once it's in front, so read its state",
		},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			s.p.On("ListApps").Return([]proto.App{{BundleID: finder.BundleID, Name: "Finder", PID: 7, Active: !tc.app.Active}, tc.app}, nil)
			s.p.On("FocusedWindow", tc.app).Return(testWin, nil).Once()
			s.p.On("Release", []uintptr{9}).Once()
			s.p.On("Tree", testWin, DefaultLimits).Return(sampleTree(), false, nil).Once()
			var st proto.State
			s.ok(s.call(proto.MethodGetState, proto.GetStateParams{BundleID: textEdit.BundleID, Capture: proto.CaptureText}), &st)
			s.p.On("Press", uintptr(3)).Return(fmt.Errorf("press: %w", ErrNoReply)).Once()
			if tc.setup != nil {
				tc.setup()
			}
			var res proto.ActionResult
			s.ok(s.act(proto.ActionParams{Action: proto.ActionClick, Index: 3}), &res)
			require.Equal(s.T(), tc.want, res.Message)
			s.p.AssertNotCalled(s.T(), "Click", mock.Anything, mock.Anything, mock.Anything)
			s.p.AssertExpectations(s.T())
		})
	}
}

func (s *ServiceSuite) TestForegroundWithoutAnotherFrontApp() {
	tests := []struct {
		name string
		apps []proto.App
	}{
		{name: "none frontmost", apps: []proto.App{textEdit}},
		{name: "the app itself frontmost", apps: []proto.App{{BundleID: textEdit.BundleID, Name: "TextEdit", PID: 42, Active: true}}},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			s.p.On("ListApps").Return(tc.apps, nil)
			s.p.On("UserIdle").Return(time.Hour)
			s.p.On("Activate", tc.apps[0]).Return(nil).Once()
			s.p.On("Frontmost", tc.apps[0]).Return(true).Once()
			s.p.On("Key", Combo{Key: "a"}).Return(nil)
			var res proto.ActionResult
			s.ok(s.act(proto.ActionParams{Action: proto.ActionKey, Keys: "a", Foreground: true}), &res)
			require.Empty(s.T(), s.sleeps, "no focus to give back")
			s.p.AssertExpectations(s.T())
		})
	}
}

func (s *ServiceSuite) TestMenuShortcutToTheFrontApp() {
	front := textEdit
	front.Active = true
	s.p.On("ListApps").Return([]proto.App{finder, front}, nil)
	s.p.On("KeyTo", front, Combo{Mods: ModCmd, Key: "w"}).Return(nil)
	var res proto.ActionResult
	s.ok(s.act(proto.ActionParams{Action: proto.ActionKey, Keys: "cmd+w"}), &res)
}

func (s *ServiceSuite) TestKeysToAnOpenMenu() {
	tree := sampleTree()
	tree.Children = append(tree.Children, &Node{Role: "AXMenu", Ref: 5})
	s.running()
	s.p.On("FocusedWindow", textEdit).Return(testWin, nil).Once()
	s.p.On("Release", []uintptr{9}).Once()
	s.p.On("Tree", testWin, DefaultLimits).Return(tree, false, nil).Once()
	var st proto.State
	s.ok(s.call(proto.MethodGetState, proto.GetStateParams{BundleID: textEdit.BundleID}), &st)
	s.foreground(true)
	s.p.On("Key", Combo{Key: "escape"}).Return(nil)
	var res proto.ActionResult
	s.ok(s.act(proto.ActionParams{Action: proto.ActionKey, Keys: "Escape"}), &res)
}

func (s *ServiceSuite) TestForegroundWaitsForTheUser() {
	s.running()
	s.p.On("UserIdle").Return(200 * time.Millisecond).Twice()
	s.foreground(true)
	s.p.On("Key", Combo{Key: "a"}).Return(nil)
	var res proto.ActionResult
	s.ok(s.act(proto.ActionParams{Action: proto.ActionKey, Keys: "a", Foreground: true}), &res)
	require.Equal(s.T(), []time.Duration{idlePoll, idlePoll, settle}, s.sleeps)
}

func (s *ServiceSuite) TestForegroundWaitsForTheAppToComeForward() {
	s.running()
	s.p.On("UserIdle").Return(time.Hour)
	s.p.On("Activate", textEdit).Return(nil).Once()
	s.p.On("Frontmost", textEdit).Return(false).Twice()
	s.p.On("Frontmost", textEdit).Return(true).Once()
	s.p.On("Key", Combo{Key: "a"}).Return(nil)
	s.p.On("Windows", textEdit).Return([]uint32{5}).Twice()
	s.p.On("Activate", finder).Return(nil).Once()
	var res proto.ActionResult
	s.ok(s.act(proto.ActionParams{Action: proto.ActionKey, Keys: "a", Foreground: true}), &res)
	require.Equal(s.T(), []time.Duration{frontPoll, frontPoll, settle}, s.sleeps)
}

func (s *ServiceSuite) TestForegroundSendsNothingWhenTheAppStaysBehind() {
	s.running()
	s.p.On("UserIdle").Return(time.Hour)
	s.p.On("Activate", textEdit).Return(nil).Once()
	s.p.On("Frontmost", textEdit).Return(false)
	s.fails(s.act(proto.ActionParams{Action: proto.ActionKey, Keys: "a", Foreground: true}), proto.CodeNotFrontmost, "TextEdit didn't come to the front")
	require.Len(s.T(), s.sleeps, int(frontWait/frontPoll))
	s.p.AssertNotCalled(s.T(), "Key", mock.Anything)
}

func (s *ServiceSuite) TestRefusedPressOutsideTheWindow() {
	tree := sampleTree()
	tree.Children[1].Frame = Rect{X: 500, Y: 500, W: 10, H: 10}
	s.running()
	s.p.On("FocusedWindow", textEdit).Return(testWin, nil).Once()
	s.p.On("Release", []uintptr{9}).Once()
	s.p.On("Tree", testWin, DefaultLimits).Return(tree, false, nil).Once()
	var st proto.State
	s.ok(s.call(proto.MethodGetState, proto.GetStateParams{BundleID: textEdit.BundleID}), &st)
	s.p.On("Press", uintptr(3)).Return(&proto.Error{Code: proto.CodeUnsupported, Message: "press is not supported"})
	s.fails(s.act(proto.ActionParams{Action: proto.ActionClick, Index: 3}), proto.CodeElementNotFound, "element [3] is outside the window")
}

func (s *ServiceSuite) TestPopoverPastTheWindowEdge() {
	// The popover hangs below the window {100,50,100,50}; its swatch
	// {160,105,10,10} lies wholly outside the window.
	tree := &Node{Role: "AXWindow", Ref: 1, Children: []*Node{
		{Role: rolePopover, Frame: Rect{150, 90, 60, 40}, Ref: 2, Children: []*Node{
			{Role: "AXButton", Name: "orange", Actions: []string{axPress}, Frame: Rect{160, 105, 10, 10}, Ref: 3},
		}},
	}}
	shot := testWin
	shot.Frame = Rect{100, 50, 110, 80}
	s.running()
	s.p.On("FocusedWindow", textEdit).Return(testWin, nil).Once()
	s.p.On("Release", []uintptr{9}).Once()
	s.p.On("Tree", testWin, DefaultLimits).Return(tree, false, nil).Once()
	s.p.On("Capture", shot).Return(solid(220, 160), nil).Once()
	var st proto.State
	s.ok(s.call(proto.MethodGetState, proto.GetStateParams{BundleID: textEdit.BundleID, Capture: proto.CaptureBoth}), &st)
	require.Equal(s.T(), 220, st.Width)
	require.Equal(s.T(), 160, st.Height)

	swatch := Point{165, 110}
	tests := []struct {
		name string
		p    proto.ActionParams
	}{
		{name: "refused press clicks the swatch", p: proto.ActionParams{Action: proto.ActionClick, Index: 3}},
		{name: "screenshot pixels reach the swatch", p: proto.ActionParams{Action: proto.ActionClick, X: ptr(130), Y: ptr(120)}},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			if tc.p.Index > 0 {
				s.p.On("Press", uintptr(3)).Return(&proto.Error{Code: proto.CodeUnsupported, Message: "the element refused the press"}).Once()
			}
			s.foreground(true)
			s.p.On("Click", swatch, ButtonLeft, 1).Return(nil).Once()
			var res proto.ActionResult
			s.ok(s.act(tc.p), &res)
			require.Equal(s.T(), "click done in TextEdit", res.Message)
		})
	}
}

func (s *ServiceSuite) TestForegroundGivesUpWhileTheUserIsActive() {
	s.running()
	s.p.On("UserIdle").Return(time.Duration(0))
	s.fails(s.act(proto.ActionParams{Action: proto.ActionKey, Keys: "a", Foreground: true}), proto.CodeUserActive, "the user is using the keyboard or mouse")
	require.Len(s.T(), s.sleeps, int(idleWait/idlePoll))
}

func (s *ServiceSuite) TestRestoreFailureDoesNotFailTheAction() {
	s.running()
	s.p.On("UserIdle").Return(time.Hour)
	s.p.On("Activate", textEdit).Return(nil).Once()
	s.p.On("Frontmost", textEdit).Return(true).Once()
	s.p.On("Windows", textEdit).Return([]uint32{5}).Twice()
	s.p.On("Activate", finder).Return(errors.New("gone")).Once()
	s.p.On("Key", Combo{Key: "a"}).Return(nil)
	var res proto.ActionResult
	s.ok(s.act(proto.ActionParams{Action: proto.ActionKey, Keys: "a", Foreground: true}), &res)
}

func (s *ServiceSuite) TestForegroundLeavesAnOpenedWindowInFront() {
	// The click opens a popover, a window of its own, which would close
	// with the focus given back to Finder.
	s.read()
	s.p.On("UserIdle").Return(time.Hour).Once()
	s.p.On("Activate", textEdit).Return(nil).Once()
	s.p.On("Frontmost", textEdit).Return(true).Once()
	s.p.On("Windows", textEdit).Return([]uint32{5}).Once()
	s.p.On("Click", Point{110, 60}, ButtonLeft, 1).Return(nil).Once()
	s.p.On("Windows", textEdit).Return([]uint32{8, 5}).Once()
	var res proto.ActionResult
	s.ok(s.act(proto.ActionParams{Action: proto.ActionClick, X: ptr(20), Y: ptr(20)}), &res)
	require.Equal(s.T(), "click done in TextEdit; it opened a window, such as a popover or menu, so it was left in front for that to stay open", res.Message)
	require.Equal(s.T(), []time.Duration{settle}, s.sleeps)
	s.p.AssertNotCalled(s.T(), "Activate", finder)
}

func (s *ServiceSuite) TestActionPlatformErrors() {
	unsupported := &proto.Error{Code: proto.CodeUnsupported, Message: "not here"}
	tests := []struct {
		name   string
		p      proto.ActionParams
		expect func()
		code   string
		msg    string
	}{
		{name: "activate", p: proto.ActionParams{Action: proto.ActionKey, Keys: "a", Foreground: true}, expect: func() {
			s.p.On("UserIdle").Return(time.Hour)
			s.p.On("Activate", textEdit).Return(errors.New("platform failed")).Once()
		}},
		{name: "type click", p: proto.ActionParams{Action: proto.ActionType, Text: "x", Index: 2, Foreground: true}, expect: func() {
			s.p.On("InsertText", textEdit, uintptr(2), "x").Return(unsupported)
			s.foreground(true)
			s.p.On("Click", mock.Anything, ButtonLeft, 1).Return(errors.New("platform failed"))
		}},
		{name: "press", p: proto.ActionParams{Action: proto.ActionClick, Index: 3}, expect: func() {
			s.p.On("Press", uintptr(3)).Return(errors.New("platform failed"))
		}},
		{name: "key", p: proto.ActionParams{Action: proto.ActionKey, Keys: "a b", Foreground: true}, expect: func() {
			s.foreground(true)
			s.p.On("Key", Combo{Key: "a"}).Return(errors.New("platform failed"))
		}},
		{name: "key to the app", p: proto.ActionParams{Action: proto.ActionKey, Keys: "a b"}, expect: func() {
			s.p.On("KeyTo", textEdit, Combo{Key: "a"}).Return(errors.New("platform failed"))
		}},
		{name: "focus", p: proto.ActionParams{Action: proto.ActionKey, Keys: "a", Index: 2}, expect: func() {
			s.p.On("Focus", uintptr(2)).Return(errors.New("platform failed"))
		}},
		{name: "type to the app", p: proto.ActionParams{Action: proto.ActionType, Text: "x"}, expect: func() {
			s.p.On("InsertText", textEdit, uintptr(0), "x").Return(unsupported)
			s.p.On("TypeTo", textEdit, "x").Return(errors.New("platform failed"))
		}},
		{name: "insert", p: proto.ActionParams{Action: proto.ActionType, Text: "x"}, expect: func() {
			s.p.On("InsertText", textEdit, uintptr(0), "x").Return(errors.New("platform failed"))
		}},
		{name: "insert outside the window unsupported", p: proto.ActionParams{Action: proto.ActionType, Text: "x", Index: 4, Foreground: true}, expect: func() {
			s.p.On("InsertText", textEdit, uintptr(4), "x").Return(unsupported)
		}, code: proto.CodeElementNotFound, msg: "element [4] is outside the window"},
		{name: "set value unsupported", p: proto.ActionParams{Action: proto.ActionSetValue, Index: 2, Value: "v"}, expect: func() {
			s.p.On("SetValue", uintptr(2), "v").Return(unsupported)
		}, code: proto.CodeUnsupported, msg: "not here"},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			s.read()
			tc.expect()
			code, msg := tc.code, tc.msg
			if code == "" {
				code, msg = proto.CodeInternal, "platform failed"
			}
			s.fails(s.act(tc.p), code, msg)
			s.p.AssertExpectations(s.T())
		})
	}
}
