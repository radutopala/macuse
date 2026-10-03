// Package proto defines the requests the Service serves and the types the
// HTTP API and its clients exchange.
package proto

import "encoding/json"

// Methods the helper serves.
const (
	MethodListApps           = "list_apps"
	MethodStartApp           = "start_app"
	MethodGetState           = "get_state"
	MethodAction             = "action"
	MethodPermissions        = "permissions"
	MethodRequestPermissions = "request_permissions"
)

// Error codes carried in Error.Code.
const (
	CodeInvalidParams     = "invalid_params"
	CodeUnknownMethod     = "unknown_method"
	CodeUnsupported       = "unsupported"
	CodePermission        = "permission_denied"
	CodeAppNotFound       = "app_not_found"
	CodeElementNotFound   = "element_not_found"
	CodeNoState           = "no_state"
	CodeInternal          = "internal"
	CodeHelperUnavailable = "helper_unavailable"
	// CodeUserActive means the user kept using the keyboard or mouse, so
	// input that needs the app frontmost wasn't sent.
	CodeUserActive = "user_active"
	// CodeNotFrontmost means the app didn't come to the front, so input
	// that would have gone to the frontmost app wasn't sent.
	CodeNotFrontmost = "not_frontmost"
	// CodeDenied means the deny list or the user refused the app.
	CodeDenied = "denied"
	// CodeNotRunning means the app has to be started first.
	CodeNotRunning = "not_running"
	// CodePaused means the user paused control for every agent.
	CodePaused = "paused"
	// CodeStopped means the user stopped the calling session.
	CodeStopped = "stopped"
	// CodeUnauthorized means the token is missing or wrong.
	CodeUnauthorized = "unauthorized"
)

// Actions accepted by MethodAction.
const (
	ActionClick       = "click"
	ActionDoubleClick = "double_click"
	ActionRightClick  = "right_click"
	ActionType        = "type"
	ActionKey         = "key"
	ActionScroll      = "scroll"
	ActionDrag        = "drag"
	ActionSetValue    = "set_value"
)

// Capture modes for GetStateParams.Capture.
const (
	CaptureText  = "text"
	CaptureImage = "image"
	CaptureBoth  = "both"
)

// Projections for GetStateParams.Projection.
const (
	ProjectionFull = "full"
	ProjectionDiff = "diff"
)

// Request is one call to the Service.
type Request struct {
	ID     uint64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response answers the Request with the same ID. Exactly one of Result and
// Error is set.
type Response struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error is a failed call.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// ErrorBody is the JSON body of a failed HTTP API call.
type ErrorBody struct {
	Error *Error `json:"error"`
}

// App is a running application. TeamID is the code-signing team, empty for
// unsigned or ad-hoc signed apps; together with BundleID it identifies the app
// for approvals.
type App struct {
	BundleID string `json:"bundle_id"`
	Name     string `json:"name"`
	PID      int    `json:"pid"`
	TeamID   string `json:"team_id,omitempty"`
	Active   bool   `json:"active,omitempty"`
}

// AppList is the MethodListApps result.
type AppList struct {
	Apps []App `json:"apps"`
}

// StartAppParams launches (or activates) an app.
type StartAppParams struct {
	BundleID string `json:"bundle_id"`
}

// GetStateParams reads the app's focused window.
type GetStateParams struct {
	BundleID   string `json:"bundle_id"`
	Capture    string `json:"capture,omitempty"`
	Projection string `json:"projection,omitempty"`
	// MaxImageEdge caps the longer side of the screenshot in pixels.
	MaxImageEdge int `json:"max_image_edge,omitempty"`
}

// State is the MethodGetState result. Tree lists the window's elements, one
// per line as `[N] role "name" (value: v)`; N is the index actions take. With
// the diff projection Tree is a unified diff against the previous read.
// Image coordinates map to the screen through the window origin and Scale.
type State struct {
	App       App     `json:"app"`
	Window    string  `json:"window"`
	Tree      string  `json:"tree,omitempty"`
	Elements  int     `json:"elements"`
	Truncated bool    `json:"truncated,omitempty"`
	Image     []byte  `json:"image,omitempty"`
	MIMEType  string  `json:"mime_type,omitempty"`
	Width     int     `json:"width,omitempty"`
	Height    int     `json:"height,omitempty"`
	Scale     float64 `json:"scale,omitempty"`
}

// ActionParams drives one input action against an app. Index refers to the
// last State read for the app; X/Y (and ToX/ToY for drag) are pixels in the
// last screenshot of that app's window.
type ActionParams struct {
	BundleID string   `json:"bundle_id"`
	Action   string   `json:"action"`
	Index    int      `json:"index,omitempty"`
	X        *float64 `json:"x,omitempty"`
	Y        *float64 `json:"y,omitempty"`
	ToX      *float64 `json:"to_x,omitempty"`
	ToY      *float64 `json:"to_y,omitempty"`
	Text     string   `json:"text,omitempty"`
	Keys     string   `json:"keys,omitempty"`
	DX       int      `json:"dx,omitempty"`
	DY       int      `json:"dy,omitempty"`
	Value    string   `json:"value,omitempty"`
	// Foreground sends keys with the app frontmost, for apps that drop
	// keys in the background.
	Foreground bool `json:"foreground,omitempty"`
}

// ActionResult is the MethodAction result.
type ActionResult struct {
	Message string `json:"message"`
}

// Permissions reports the macOS privacy grants the helper holds.
type Permissions struct {
	Accessibility   bool `json:"accessibility"`
	ScreenRecording bool `json:"screen_recording"`
}

// Status is the GET /v1/status answer.
type Status struct {
	Version     string       `json:"version"`
	Paused      bool         `json:"paused"`
	Permissions *Permissions `json:"permissions,omitempty"`
}

// Headers an API client names itself with, for the approval prompt.
const (
	HeaderClient  = "X-Mac-Use-Client"
	HeaderSession = "X-Mac-Use-Session"
)
