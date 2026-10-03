// Package mcpserver serves the mac-use tools over MCP, calling the HTTP API
// for each one.
package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/radutopala/mac-use/internal/client"
	"github.com/radutopala/mac-use/internal/proto"
)

// API is the mac-use HTTP API. Satisfied by *client.Client.
type API interface {
	ListApps(ctx context.Context) ([]proto.App, error)
	StartApp(ctx context.Context, bundleID string) (proto.App, error)
	GetState(ctx context.Context, p proto.GetStateParams) (proto.State, error)
	Action(ctx context.Context, p proto.ActionParams) (proto.ActionResult, error)
}

// Server is the MCP server.
type Server struct {
	api    API
	logger *slog.Logger
	mcp    *mcp.Server
}

// approvalNote is shared by the tool descriptions that touch an app.
const approvalNote = " The first use of an app blocks until the user approves it in the mac-use menu bar; terminals, password managers, System Settings and mac-use itself are always refused."

// foregroundNote explains the foreground option of the keyboard tools.
const foregroundNote = " Read the state again to check it landed; foreground true brings the app forward for the keys, once the user stops typing, and gives the focus back."

// indexNote explains where element indexes come from.
const indexNote = " index is an element's [N] from the last get_state of this app; read the state again after the UI changes. Prefer indexes; x/y are pixels in the last screenshot (get_state with capture image or both)."

// New returns a Server whose tools call api.
func New(api API, version string, logger *slog.Logger) *Server {
	s := &Server{
		api:    api,
		logger: logger,
		mcp:    mcp.NewServer(&mcp.Implementation{Name: "mac-use", Version: version}, nil),
	}
	s.register()
	return s
}

// Run serves one MCP session over t until it ends.
func (s *Server) Run(ctx context.Context, t mcp.Transport) error {
	return s.mcp.Run(ctx, t)
}

func (s *Server) register() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "list_apps",
		Description: "List the running Mac apps (name, bundle id, pid, which is active). Use the bundle id as app in the other tools.",
	}, s.handleListApps)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "start_app",
		Description: "Launch a Mac app by bundle id (e.g. com.apple.TextEdit) in the background; a running app is left as it is. The tools work with apps in the background where they can, so the user keeps the focus." + approvalNote,
	}, s.handleStartApp)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "get_state",
		Description: "Read an app's focused window: its accessibility tree, one element per line as [N] role \"name\" (value: v), and optionally a screenshot. Call it before acting, and again after the UI changes; the [N] indexes are what the action tools take. projection diff returns only what changed since the last read." + approvalNote,
	}, s.handleGetState)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "click",
		Description: "Click an element by index, or a point by x/y. button right right-clicks; double double-clicks." + indexNote,
	}, s.handleClick)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "type",
		Description: "Type text into an app: at the end of the element at index, or else at the cursor of the app's focused element, replacing its selection as typing would. The app stays in the background; text an element doesn't take through accessibility goes as keys to the focused element, which may ignore them." + foregroundNote + indexNote,
	}, s.handleType)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "press_key",
		Description: "Press keys in an app: a key or chord joined with + (e.g. cmd+s, Return, shift+Tab), several separated by spaces (e.g. ctrl+a backspace). The keys go to the app in the background, to its focused element, or to the element at index once it's focused; it fails if that element won't take the focus, and a focused element that doesn't take keys (a scrollbar, a button) ignores them without an error. Menu shortcuts (cmd combos) to an app that isn't frontmost, and keys while a menu is open, are sent with the app brought forward, since it drops them in the background; clicking a window's own button by index (e.g. its close button) avoids that." + foregroundNote + indexNote,
	}, s.handlePressKey)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "scroll",
		Description: "Scroll an app by dx/dy lines (positive dy scrolls down), over the element at index, the point x/y, or else the window." + indexNote,
	}, s.handleScroll)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "drag",
		Description: "Drag from x/y to to_x/to_y, all pixels in the last screenshot of the app's window (get_state with capture image or both).",
	}, s.handleDrag)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "set_value",
		Description: "Set an element's value directly (text fields, sliders, scrollbars), replacing what's there without typing, in the background. It fails when the element doesn't keep the value, as checkboxes and toolbar combo boxes don't; click those instead." + indexNote,
	}, s.handleSetValue)
}

type listAppsInput struct{}

type appInput struct {
	App string `json:"app" jsonschema:"The app's bundle id, e.g. com.apple.TextEdit"`
}

type getStateInput struct {
	App        string `json:"app" jsonschema:"The app's bundle id"`
	Capture    string `json:"capture,omitempty" jsonschema:"text (the tree only), image (a screenshot only) or both"`
	Projection string `json:"projection,omitempty" jsonschema:"full (the whole tree) or diff (changes since the last read)"`
}

type clickInput struct {
	App    string   `json:"app" jsonschema:"The app's bundle id"`
	Index  int      `json:"index,omitempty" jsonschema:"The element's [N] from the last get_state"`
	X      *float64 `json:"x,omitempty" jsonschema:"Pixels from the screenshot's left edge, when not clicking by index"`
	Y      *float64 `json:"y,omitempty" jsonschema:"Pixels from the screenshot's top edge, when not clicking by index"`
	Button string   `json:"button,omitempty" jsonschema:"left (default) or right"`
	Double bool     `json:"double,omitempty" jsonschema:"Double-click (left button only)"`
}

type typeInput struct {
	App        string `json:"app" jsonschema:"The app's bundle id"`
	Text       string `json:"text" jsonschema:"The text to type"`
	Index      int    `json:"index,omitempty" jsonschema:"The element to type into, its [N] from the last get_state"`
	Foreground bool   `json:"foreground,omitempty" jsonschema:"Bring the app forward for typing keys, for apps that drop them in the background"`
}

type pressKeyInput struct {
	App        string `json:"app" jsonschema:"The app's bundle id"`
	Keys       string `json:"keys" jsonschema:"e.g. cmd+s, Return, ctrl+a backspace"`
	Index      int    `json:"index,omitempty" jsonschema:"The element to focus before the keys, its [N] from the last get_state"`
	Foreground bool   `json:"foreground,omitempty" jsonschema:"Bring the app forward for the keys, for apps that drop them in the background"`
}

type scrollInput struct {
	App   string   `json:"app" jsonschema:"The app's bundle id"`
	DX    int      `json:"dx,omitempty" jsonschema:"Lines to scroll right (negative: left)"`
	DY    int      `json:"dy,omitempty" jsonschema:"Lines to scroll down (negative: up)"`
	Index int      `json:"index,omitempty" jsonschema:"The element to scroll, its [N] from the last get_state"`
	X     *float64 `json:"x,omitempty" jsonschema:"Pixels in the last screenshot to scroll at"`
	Y     *float64 `json:"y,omitempty" jsonschema:"Pixels in the last screenshot to scroll at"`
}

type dragInput struct {
	App string  `json:"app" jsonschema:"The app's bundle id"`
	X   float64 `json:"x" jsonschema:"Start, pixels from the screenshot's left edge"`
	Y   float64 `json:"y" jsonschema:"Start, pixels from the screenshot's top edge"`
	ToX float64 `json:"to_x" jsonschema:"End, pixels from the screenshot's left edge"`
	ToY float64 `json:"to_y" jsonschema:"End, pixels from the screenshot's top edge"`
}

type setValueInput struct {
	App   string `json:"app" jsonschema:"The app's bundle id"`
	Index int    `json:"index" jsonschema:"The element's [N] from the last get_state"`
	Value string `json:"value" jsonschema:"The new value"`
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}

// named names the API calls after the MCP client, so the user's approval
// prompt says who asks.
func named(ctx context.Context, req *mcp.CallToolRequest) context.Context {
	if req.Session == nil {
		return ctx
	}
	if p := req.Session.InitializeParams(); p != nil && p.ClientInfo != nil && p.ClientInfo.Name != "" {
		return client.WithName(ctx, p.ClientInfo.Name)
	}
	return ctx
}

func (s *Server) handleListApps(ctx context.Context, req *mcp.CallToolRequest, _ listAppsInput) (*mcp.CallToolResult, any, error) {
	s.logger.Info("mcp tool call", "tool", "list_apps")
	apps, err := s.api.ListApps(named(ctx, req))
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	if len(apps) == 0 {
		return textResult("No apps are running."), nil, nil
	}
	lines := make([]string, len(apps))
	for i, a := range apps {
		lines[i] = fmt.Sprintf("%s (%s) pid %d", a.Name, a.BundleID, a.PID)
		if a.Active {
			lines[i] += " [active]"
		}
	}
	return textResult(strings.Join(lines, "\n")), nil, nil
}

func (s *Server) handleStartApp(ctx context.Context, req *mcp.CallToolRequest, in appInput) (*mcp.CallToolResult, any, error) {
	s.logger.Info("mcp tool call", "tool", "start_app", "app", in.App)
	app, err := s.api.StartApp(named(ctx, req), in.App)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	return textResult(fmt.Sprintf("Started %s (%s), pid %d.", app.Name, app.BundleID, app.PID)), nil, nil
}

func (s *Server) handleGetState(ctx context.Context, req *mcp.CallToolRequest, in getStateInput) (*mcp.CallToolResult, any, error) {
	s.logger.Info("mcp tool call", "tool", "get_state", "app", in.App, "capture", in.Capture, "projection", in.Projection)
	st, err := s.api.GetState(named(ctx, req), proto.GetStateParams{BundleID: in.App, Capture: in.Capture, Projection: in.Projection})
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "App: %s (%s)\nWindow: %q\nElements: %d\n", st.App.Name, st.App.BundleID, st.Window, st.Elements)
	if st.Truncated {
		b.WriteString("Note: the tree was truncated; act on what's shown or narrow the window's content.\n")
	}
	if len(st.Image) > 0 {
		fmt.Fprintf(&b, "Screenshot: %dx%d px. x/y coordinates for actions are pixels in this screenshot.\n", st.Width, st.Height)
	}
	if st.Tree != "" {
		b.WriteString("\n" + st.Tree)
	}

	res := textResult(b.String())
	if len(st.Image) > 0 {
		mime := st.MIMEType
		if mime == "" {
			mime = "image/jpeg"
		}
		res.Content = append(res.Content, &mcp.ImageContent{Data: st.Image, MIMEType: mime})
	}
	return res, nil, nil
}

func (s *Server) handleClick(ctx context.Context, req *mcp.CallToolRequest, in clickInput) (*mcp.CallToolResult, any, error) {
	s.logger.Info("mcp tool call", "tool", "click", "app", in.App, "index", in.Index)
	action := proto.ActionClick
	switch in.Button {
	case "", "left":
		if in.Double {
			action = proto.ActionDoubleClick
		}
	case "right":
		if in.Double {
			return errorResult("double is only for the left button"), nil, nil
		}
		action = proto.ActionRightClick
	default:
		return errorResult(fmt.Sprintf("button must be left or right, got %q", in.Button)), nil, nil
	}
	return s.action(ctx, req, proto.ActionParams{BundleID: in.App, Action: action, Index: in.Index, X: in.X, Y: in.Y})
}

func (s *Server) handleType(ctx context.Context, req *mcp.CallToolRequest, in typeInput) (*mcp.CallToolResult, any, error) {
	s.logger.Info("mcp tool call", "tool", "type", "app", in.App, "index", in.Index)
	return s.action(ctx, req, proto.ActionParams{BundleID: in.App, Action: proto.ActionType, Index: in.Index, Text: in.Text, Foreground: in.Foreground})
}

func (s *Server) handlePressKey(ctx context.Context, req *mcp.CallToolRequest, in pressKeyInput) (*mcp.CallToolResult, any, error) {
	s.logger.Info("mcp tool call", "tool", "press_key", "app", in.App, "keys", in.Keys)
	return s.action(ctx, req, proto.ActionParams{BundleID: in.App, Action: proto.ActionKey, Index: in.Index, Keys: in.Keys, Foreground: in.Foreground})
}

func (s *Server) handleScroll(ctx context.Context, req *mcp.CallToolRequest, in scrollInput) (*mcp.CallToolResult, any, error) {
	s.logger.Info("mcp tool call", "tool", "scroll", "app", in.App, "dx", in.DX, "dy", in.DY)
	return s.action(ctx, req, proto.ActionParams{BundleID: in.App, Action: proto.ActionScroll, DX: in.DX, DY: in.DY, Index: in.Index, X: in.X, Y: in.Y})
}

func (s *Server) handleDrag(ctx context.Context, req *mcp.CallToolRequest, in dragInput) (*mcp.CallToolResult, any, error) {
	s.logger.Info("mcp tool call", "tool", "drag", "app", in.App)
	return s.action(ctx, req, proto.ActionParams{BundleID: in.App, Action: proto.ActionDrag, X: &in.X, Y: &in.Y, ToX: &in.ToX, ToY: &in.ToY})
}

func (s *Server) handleSetValue(ctx context.Context, req *mcp.CallToolRequest, in setValueInput) (*mcp.CallToolResult, any, error) {
	s.logger.Info("mcp tool call", "tool", "set_value", "app", in.App, "index", in.Index)
	return s.action(ctx, req, proto.ActionParams{BundleID: in.App, Action: proto.ActionSetValue, Index: in.Index, Value: in.Value})
}

// action performs one action and returns the engine's message.
func (s *Server) action(ctx context.Context, req *mcp.CallToolRequest, p proto.ActionParams) (*mcp.CallToolResult, any, error) {
	res, err := s.api.Action(named(ctx, req), p)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	if res.Message == "" {
		return textResult("Done."), nil, nil
	}
	return textResult(res.Message), nil, nil
}
