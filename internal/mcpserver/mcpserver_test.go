package mcpserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/radutopala/macuse/internal/client"
	"github.com/radutopala/macuse/internal/proto"
)

type mockAPI struct{ mock.Mock }

func (m *mockAPI) ListApps(ctx context.Context) ([]proto.App, error) {
	args := m.Called(ctx)
	return args.Get(0).([]proto.App), args.Error(1)
}

func (m *mockAPI) StartApp(ctx context.Context, bundleID string) (proto.App, error) {
	args := m.Called(ctx, bundleID)
	return args.Get(0).(proto.App), args.Error(1)
}

func (m *mockAPI) GetState(ctx context.Context, p proto.GetStateParams) (proto.State, error) {
	args := m.Called(ctx, p)
	return args.Get(0).(proto.State), args.Error(1)
}

func (m *mockAPI) Action(ctx context.Context, p proto.ActionParams) (proto.ActionResult, error) {
	args := m.Called(ctx, p)
	return args.Get(0).(proto.ActionResult), args.Error(1)
}

type MCPSuite struct {
	suite.Suite
	api     *mockAPI
	session *mcp.ClientSession
	cancel  context.CancelFunc
}

func TestMCPSuite(t *testing.T) { suite.Run(t, new(MCPSuite)) }

func (s *MCPSuite) connect(clientName string) {
	s.api = new(mockAPI)
	srv := New(s.api, "v1", slog.New(slog.NewTextHandler(io.Discard, nil)))
	st, ct := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go func() { _ = srv.Run(ctx, st) }()
	c := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: "1"}, nil)
	var err error
	s.session, err = c.Connect(ctx, ct, nil)
	require.NoError(s.T(), err)
}

func (s *MCPSuite) SetupTest() { s.connect("test-agent") }

func (s *MCPSuite) TearDownTest() {
	_ = s.session.Close()
	s.cancel()
	s.api.AssertExpectations(s.T())
}

// agent matches a context naming the MCP client.
func agent(name string) any {
	return mock.MatchedBy(func(ctx context.Context) bool { return client.NameFrom(ctx) == name })
}

var anyCtx = mock.Anything

func (s *MCPSuite) call(name string, args map[string]any) *mcp.CallToolResult {
	res, err := s.session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(s.T(), err)
	return res
}

func text(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (s *MCPSuite) TestTools() {
	res, err := s.session.ListTools(context.Background(), nil)
	require.NoError(s.T(), err)
	var names []string
	for _, t := range res.Tools {
		names = append(names, t.Name)
	}
	require.ElementsMatch(s.T(), []string{"list_apps", "start_app", "get_state", "click", "type", "press_key", "scroll", "drag", "set_value"}, names)
}

func (s *MCPSuite) TestListApps() {
	s.api.On("ListApps", agent("test-agent")).Return([]proto.App{
		{Name: "Notes", BundleID: "com.apple.Notes", PID: 3, Active: true},
		{Name: "Mail", BundleID: "com.apple.mail", PID: 4},
	}, nil).Once()
	require.Equal(s.T(), "Notes (com.apple.Notes) pid 3 [active]\nMail (com.apple.mail) pid 4", text(s.call("list_apps", nil)))

	s.api.On("ListApps", anyCtx).Return([]proto.App{}, nil).Once()
	require.Equal(s.T(), "No apps are running.", text(s.call("list_apps", nil)))

	s.api.On("ListApps", anyCtx).Return([]proto.App(nil), &proto.Error{Code: proto.CodePaused, Message: "paused"}).Once()
	res := s.call("list_apps", nil)
	require.True(s.T(), res.IsError)
	require.Equal(s.T(), "paused: paused", text(res))
}

func (s *MCPSuite) TestStartApp() {
	s.api.On("StartApp", anyCtx, "com.apple.Notes").Return(proto.App{Name: "Notes", BundleID: "com.apple.Notes", PID: 9}, nil).Once()
	require.Equal(s.T(), "Started Notes (com.apple.Notes), pid 9.", text(s.call("start_app", map[string]any{"app": "com.apple.Notes"})))

	s.api.On("StartApp", anyCtx, "x").Return(proto.App{}, errors.New("nope")).Once()
	require.True(s.T(), s.call("start_app", map[string]any{"app": "x"}).IsError)
}

func (s *MCPSuite) TestGetState() {
	tests := []struct {
		name  string
		state proto.State
		text  string
		mime  string
	}{
		{"tree only", proto.State{App: proto.App{Name: "Notes", BundleID: "n"}, Window: "W", Elements: 2, Tree: "[1] AXButton \"OK\""},
			"App: Notes (n)\nWindow: \"W\"\nElements: 2\n\n[1] AXButton \"OK\"", ""},
		{"truncated with image", proto.State{App: proto.App{Name: "Notes", BundleID: "n"}, Window: "W", Truncated: true, Image: []byte{1}, Width: 10, Height: 20},
			"App: Notes (n)\nWindow: \"W\"\nElements: 0\nNote: the tree was truncated; act on what's shown or narrow the window's content.\nScreenshot: 10x20 px. x/y coordinates for actions are pixels in this screenshot.\n", "image/jpeg"},
		{"png", proto.State{Image: []byte{1}, MIMEType: "image/png"}, "", "image/png"},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.api.On("GetState", anyCtx, proto.GetStateParams{BundleID: "n", Capture: "both", Projection: "full"}).Return(tt.state, nil).Once()
			res := s.call("get_state", map[string]any{"app": "n", "capture": "both", "projection": "full"})
			if tt.text != "" {
				require.Equal(s.T(), tt.text, text(res))
			}
			if tt.mime == "" {
				require.Len(s.T(), res.Content, 1)
				return
			}
			require.Len(s.T(), res.Content, 2)
			img := res.Content[1].(*mcp.ImageContent)
			require.Equal(s.T(), tt.mime, img.MIMEType)
			require.Equal(s.T(), []byte{1}, img.Data)
		})
	}
	s.api.On("GetState", anyCtx, mock.Anything).Return(proto.State{}, errors.New("no window")).Once()
	require.True(s.T(), s.call("get_state", map[string]any{"app": "n"}).IsError)
}

func ptr(v float64) *float64 { return &v }

func (s *MCPSuite) TestActions() {
	tests := []struct {
		name string
		tool string
		args map[string]any
		want proto.ActionParams
	}{
		{"click", "click", map[string]any{"app": "a", "index": 3}, proto.ActionParams{BundleID: "a", Action: proto.ActionClick, Index: 3}},
		{"left double", "click", map[string]any{"app": "a", "button": "left", "double": true}, proto.ActionParams{BundleID: "a", Action: proto.ActionDoubleClick}},
		{"right at point", "click", map[string]any{"app": "a", "button": "right", "x": 1.5, "y": 2}, proto.ActionParams{BundleID: "a", Action: proto.ActionRightClick, X: ptr(1.5), Y: ptr(2)}},
		{"type", "type", map[string]any{"app": "a", "text": "hi", "index": 2, "foreground": true}, proto.ActionParams{BundleID: "a", Action: proto.ActionType, Text: "hi", Index: 2, Foreground: true}},
		{"press_key", "press_key", map[string]any{"app": "a", "keys": "cmd+s"}, proto.ActionParams{BundleID: "a", Action: proto.ActionKey, Keys: "cmd+s"}},
		{"scroll", "scroll", map[string]any{"app": "a", "dy": 3, "x": 4, "y": 5}, proto.ActionParams{BundleID: "a", Action: proto.ActionScroll, DY: 3, X: ptr(4), Y: ptr(5)}},
		{"drag", "drag", map[string]any{"app": "a", "x": 1, "y": 2, "to_x": 3, "to_y": 4}, proto.ActionParams{BundleID: "a", Action: proto.ActionDrag, X: ptr(1), Y: ptr(2), ToX: ptr(3), ToY: ptr(4)}},
		{"set_value", "set_value", map[string]any{"app": "a", "index": 7, "value": "v"}, proto.ActionParams{BundleID: "a", Action: proto.ActionSetValue, Index: 7, Value: "v"}},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.api.On("Action", agent("test-agent"), tt.want).Return(proto.ActionResult{Message: "ok " + tt.name}, nil).Once()
			require.Equal(s.T(), "ok "+tt.name, text(s.call(tt.tool, tt.args)))
		})
	}

	s.api.On("Action", anyCtx, mock.Anything).Return(proto.ActionResult{}, nil).Once()
	require.Equal(s.T(), "Done.", text(s.call("click", map[string]any{"app": "a", "index": 1})))
	s.api.On("Action", anyCtx, mock.Anything).Return(proto.ActionResult{}, errors.New("element_not_found: no [1]")).Once()
	require.True(s.T(), s.call("click", map[string]any{"app": "a", "index": 1}).IsError)
}

func (s *MCPSuite) TestClickBadButtons() {
	res := s.call("click", map[string]any{"app": "a", "button": "right", "double": true})
	require.True(s.T(), res.IsError)
	require.Equal(s.T(), "double is only for the left button", text(res))
	res = s.call("click", map[string]any{"app": "a", "button": "middle"})
	require.True(s.T(), res.IsError)
	require.Contains(s.T(), text(res), `"middle"`)
}

func (s *MCPSuite) TestUnnamedClient() {
	_ = s.session.Close()
	s.cancel()
	s.connect("")
	s.api.On("ListApps", agent("")).Return([]proto.App{}, nil).Once()
	s.call("list_apps", nil)
}

func (s *MCPSuite) TestNamedWithoutSession() {
	ctx := context.Background()
	require.Equal(s.T(), ctx, named(ctx, &mcp.CallToolRequest{}))
}
