package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/radutopala/macuse/internal/proto"
)

type ClientSuite struct {
	suite.Suite
	srv     *httptest.Server
	handler http.HandlerFunc
	c       *Client
}

func TestClientSuite(t *testing.T) { suite.Run(t, new(ClientSuite)) }

func (s *ClientSuite) SetupTest() {
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.handler(w, r) }))
	s.T().Cleanup(s.srv.Close)
	s.c = &Client{BaseURL: s.srv.URL + "/", Token: "tok", Name: "claude-code", Session: "s1", HTTP: s.srv.Client()}
}

// serve answers path with status and body, checking the request.
func (s *ClientSuite) serve(verb, path string, status int, body string, check func(r *http.Request)) {
	s.handler = func(w http.ResponseWriter, r *http.Request) {
		require.Equal(s.T(), verb, r.Method)
		require.Equal(s.T(), path, r.URL.Path)
		require.Equal(s.T(), "Bearer tok", r.Header.Get("Authorization"))
		require.Equal(s.T(), "claude-code", r.Header.Get(proto.HeaderClient))
		require.Equal(s.T(), "s1", r.Header.Get(proto.HeaderSession))
		if check != nil {
			check(r)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func jsonBody(s *ClientSuite, r *http.Request, v any) {
	require.NoError(s.T(), json.NewDecoder(r.Body).Decode(v))
}

func (s *ClientSuite) TestCalls() {
	ctx := context.Background()

	s.serve(http.MethodGet, "/v1/status", 200, `{"version":"v1","paused":true}`, nil)
	st, err := s.c.Status(ctx)
	require.NoError(s.T(), err)
	require.Equal(s.T(), proto.Status{Version: "v1", Paused: true}, st)

	s.serve(http.MethodGet, "/v1/apps", 200, `{"apps":[{"bundle_id":"a","name":"A","pid":1}]}`, nil)
	apps, err := s.c.ListApps(ctx)
	require.NoError(s.T(), err)
	require.Equal(s.T(), []proto.App{{BundleID: "a", Name: "A", PID: 1}}, apps)

	s.serve(http.MethodPost, "/v1/apps/start", 200, `{"bundle_id":"a","name":"A","pid":2}`, func(r *http.Request) {
		var p proto.StartAppParams
		jsonBody(s, r, &p)
		require.Equal(s.T(), "a", p.BundleID)
	})
	app, err := s.c.StartApp(ctx, "a")
	require.NoError(s.T(), err)
	require.Equal(s.T(), 2, app.PID)

	s.serve(http.MethodPost, "/v1/state", 200, `{"app":{"bundle_id":"a","name":"A","pid":2},"window":"W","elements":3}`, func(r *http.Request) {
		var p proto.GetStateParams
		jsonBody(s, r, &p)
		require.Equal(s.T(), proto.CaptureText, p.Capture)
	})
	state, err := s.c.GetState(ctx, proto.GetStateParams{BundleID: "a", Capture: proto.CaptureText})
	require.NoError(s.T(), err)
	require.Equal(s.T(), 3, state.Elements)

	s.serve(http.MethodPost, "/v1/action", 200, `{"message":"clicked"}`, func(r *http.Request) {
		var p proto.ActionParams
		jsonBody(s, r, &p)
		require.Equal(s.T(), 4, p.Index)
	})
	res, err := s.c.Action(ctx, proto.ActionParams{BundleID: "a", Action: proto.ActionClick, Index: 4})
	require.NoError(s.T(), err)
	require.Equal(s.T(), "clicked", res.Message)

	s.serve(http.MethodPost, "/v1/batch", 200, `{"message":"2 of 2 actions done in A"}`, func(r *http.Request) {
		var p proto.BatchParams
		jsonBody(s, r, &p)
		require.Equal(s.T(), "a", p.BundleID)
		require.Len(s.T(), p.Actions, 2)
	})
	res, err = s.c.Batch(ctx, proto.BatchParams{BundleID: "a", Actions: []proto.ActionParams{{Action: proto.ActionClick, Index: 1}, {Action: proto.ActionKey, Keys: "a"}}})
	require.NoError(s.T(), err)
	require.Equal(s.T(), "2 of 2 actions done in A", res.Message)
}

func (s *ClientSuite) TestNameFromContext() {
	s.c.Name = "fallback"
	s.handler = func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"name":"`+r.Header.Get(proto.HeaderClient)+`"}`)
	}
	app, err := s.c.StartApp(WithName(context.Background(), "Claude Code"), "a")
	require.NoError(s.T(), err)
	require.Equal(s.T(), "Claude Code", app.Name)
	app, err = s.c.StartApp(context.Background(), "a")
	require.NoError(s.T(), err)
	require.Equal(s.T(), "fallback", app.Name)
	require.Empty(s.T(), NameFrom(context.Background()))
}

func (s *ClientSuite) TestErrors() {
	tests := []struct {
		name   string
		status int
		body   string
		code   string
		msg    string
	}{
		{"API error", 403, `{"error":{"code":"denied","message":"no"}}`, proto.CodeDenied, "no"},
		{"plain error", 502, "bad gateway\n", proto.CodeInternal, "macuse answered 502 Bad Gateway: bad gateway"},
		{"bad JSON", 200, `[`, proto.CodeInternal, "bad macuse answer"},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.serve(http.MethodGet, "/v1/apps", tt.status, tt.body, nil)
			_, err := s.c.ListApps(context.Background())
			var pe *proto.Error
			require.ErrorAs(s.T(), err, &pe)
			require.Equal(s.T(), tt.code, pe.Code)
			require.Contains(s.T(), pe.Message, tt.msg)
		})
	}
}

func (s *ClientSuite) TestTruncatedBody() {
	s.handler = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, "{")
	}
	_, err := s.c.ListApps(context.Background())
	require.ErrorContains(s.T(), err, "reading the macuse answer failed")
}

func (s *ClientSuite) TestUnreachableAndBadURL() {
	s.srv.Close()
	_, err := s.c.ListApps(context.Background())
	var pe *proto.Error
	require.ErrorAs(s.T(), err, &pe)
	require.Equal(s.T(), proto.CodeHelperUnavailable, pe.Code)
	require.Contains(s.T(), pe.Message, "(from a container, use "+ContainerURL+")")

	// Already the container URL: no point suggesting it.
	s.c.BaseURL = ContainerURL
	s.c.HTTP = &http.Client{Transport: failTransport{}}
	_, err = s.c.ListApps(context.Background())
	require.ErrorAs(s.T(), err, &pe)
	require.Equal(s.T(), proto.CodeHelperUnavailable, pe.Code)
	require.NotContains(s.T(), pe.Message, "from a container")
	require.Contains(s.T(), pe.Message, "listens on that port")

	s.c.BaseURL = "http://bad host"
	_, err = s.c.ListApps(context.Background())
	require.ErrorAs(s.T(), err, &pe)
	require.Equal(s.T(), proto.CodeInvalidParams, pe.Code)
}

func (s *ClientSuite) TestDefaultURL() {
	tests := []struct {
		name  string
		files []string
		want  string
	}{
		{"on the Mac", nil, LocalURL},
		{"docker", []string{"/.dockerenv"}, ContainerURL},
		{"podman", []string{"/run/.containerenv"}, ContainerURL},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			got := DefaultURL(func(p string) bool {
				for _, f := range tt.files {
					if f == p {
						return true
					}
				}
				return false
			})
			require.Equal(s.T(), tt.want, got)
		})
	}
}

// failTransport fails every request, as when nothing listens.
type failTransport struct{}

func (failTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}
