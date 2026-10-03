package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/radutopala/macuse/internal/approval"
	"github.com/radutopala/macuse/internal/proto"
	"github.com/radutopala/macuse/internal/update"
)

func (s *ServerSuite) TestUIAuth() {
	tests := []struct {
		name   string
		uiKey  string
		target string
		header string
		status int
	}{
		{"page with key", "uikey", "/ui?key=uikey", "", http.StatusOK},
		{"page with wrong key", "uikey", "/ui?key=nope", "", http.StatusUnauthorized},
		{"page ignores header", "uikey", "/ui", "uikey", http.StatusUnauthorized},
		{"api with header", "uikey", "/ui/api/state", "uikey", http.StatusOK},
		{"api ignores query", "uikey", "/ui/api/state?key=uikey", "", http.StatusUnauthorized},
		{"no key configured", "", "/ui?key=", "", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.SetupTest()
			s.srv.UIKey = tt.uiKey
			if tt.status == http.StatusOK && tt.target != "/ui?key=uikey" {
				s.host.On("LoginItem").Return(false)
				s.host.On("CLI").Return("")
				s.engine.On("Handle", method(proto.MethodPermissions)).Return(ok(proto.Permissions{}))
			}
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			if tt.header != "" {
				req.Header.Set(HeaderUIKey, tt.header)
			}
			rec := httptest.NewRecorder()
			s.h.ServeHTTP(rec, req)
			require.Equal(s.T(), tt.status, rec.Code)
		})
	}
}

func (s *ServerSuite) TestIndex() {
	req := httptest.NewRequest(http.MethodGet, "/ui?key=uikey", nil)
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	require.Equal(s.T(), "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Contains(s.T(), rec.Body.String(), "<title>macuse</title>")
	require.NotEmpty(s.T(), rec.Header().Get("Content-Security-Policy"))
}

func (s *ServerSuite) TestUIState() {
	require.NoError(s.T(), s.store.Save(approval.Saved{BundleID: "com.a", Name: "A", Decision: approval.Deny}))
	s.srv.recordActivity(approval.Caller{Client: "c", Session: "s"}, notes, "click")
	s.host.On("LoginItem").Return(true)
	s.host.On("CLI").Return("missing")
	s.engine.On("Handle", method(proto.MethodPermissions)).Return(ok(proto.Permissions{Accessibility: true, ScreenRecording: true}))
	up := new(mockUpdater)
	up.On("Status").Return(update.Status{Current: "v1.2.3", Latest: "1.2.4", Available: true})
	s.srv.Updater = up
	rec := s.ui(http.MethodGet, "/ui/api/state", "")
	require.Equal(s.T(), http.StatusOK, rec.Code)
	var st UIState
	require.NoError(s.T(), json.Unmarshal(rec.Body.Bytes(), &st))
	require.Equal(s.T(), "v1.2.3", st.Version)
	require.True(s.T(), st.LoginItem)
	require.Len(s.T(), st.Approvals, 1)
	require.Len(s.T(), st.Activity, 1)
	require.Empty(s.T(), st.Pending)
	require.True(s.T(), st.Permissions.ScreenRecording)
	require.Equal(s.T(), "missing", st.CLI)
	require.Equal(s.T(), &update.Status{Current: "v1.2.3", Latest: "1.2.4", Available: true}, st.Update)
	up.AssertExpectations(s.T())
}

func (s *ServerSuite) TestUIStateWithoutUpdater() {
	s.host.On("LoginItem").Return(false)
	s.host.On("CLI").Return("")
	s.engine.On("Handle", method(proto.MethodPermissions)).Return(ok(proto.Permissions{}))
	rec := s.ui(http.MethodGet, "/ui/api/state", "")
	require.NotContains(s.T(), rec.Body.String(), `"update"`)
	require.NotContains(s.T(), rec.Body.String(), `"cli"`)
}

func (s *ServerSuite) TestInstallCLI() {
	s.host.On("CLI").Return("").Once()
	require.Equal(s.T(), http.StatusNotImplemented, s.ui(http.MethodPost, "/ui/api/cli", "").Code)

	s.host.On("CLI").Return("missing")
	s.host.On("InstallCLI").Return(nil).Once()
	require.Equal(s.T(), http.StatusNoContent, s.ui(http.MethodPost, "/ui/api/cli", "").Code)

	s.host.On("InstallCLI").Return(errors.New("User canceled.")).Once()
	rec := s.ui(http.MethodPost, "/ui/api/cli", "")
	require.Equal(s.T(), http.StatusInternalServerError, rec.Code)
	require.Contains(s.T(), rec.Body.String(), "installing the CLI failed: User canceled.")
}

func (s *ServerSuite) TestUpdates() {
	require.Equal(s.T(), http.StatusNotImplemented, s.ui(http.MethodPost, "/ui/api/update/check", "").Code)
	require.Equal(s.T(), http.StatusNotImplemented, s.ui(http.MethodPost, "/ui/api/update/install", "").Code)

	up := new(mockUpdater)
	s.srv.Updater = up
	up.On("CheckNow", mock.Anything).Return(update.Status{Current: "v1.2.3", Latest: "1.2.3"}).Once()
	rec := s.ui(http.MethodPost, "/ui/api/update/check", "")
	require.Equal(s.T(), http.StatusOK, rec.Code)
	require.JSONEq(s.T(), `{"current":"v1.2.3","latest":"1.2.3","available":false}`, rec.Body.String())

	up.On("InstallNow", mock.Anything).Return(errors.New("there is no update to install")).Once()
	rec = s.ui(http.MethodPost, "/ui/api/update/install", "")
	require.Equal(s.T(), http.StatusInternalServerError, rec.Code)
	require.Contains(s.T(), rec.Body.String(), "there is no update to install")

	up.On("InstallNow", mock.Anything).Return(nil).Once()
	require.Equal(s.T(), http.StatusNoContent, s.ui(http.MethodPost, "/ui/api/update/install", "").Code)
	up.AssertExpectations(s.T())
}

func (s *ServerSuite) TestAnswer() {
	s.apps(notes)
	s.engine.On("Handle", method(proto.MethodGetState)).Return(ok(proto.State{}))
	ch := s.async(s.T().Context(), http.MethodPost, "/v1/state", `{"bundle_id":"com.apple.Notes"}`)
	req := s.waitPending()

	tests := []struct {
		name   string
		body   string
		status int
	}{
		{"bad JSON", `{`, http.StatusBadRequest},
		{"bad answer", `{"id":"` + req.ID + `","answer":"maybe"}`, http.StatusBadRequest},
		{"unknown id", `{"id":"99","answer":"deny"}`, http.StatusBadRequest},
		{"answered", `{"id":"` + req.ID + `","answer":"session"}`, http.StatusNoContent},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			require.Equal(s.T(), tt.status, s.ui(http.MethodPost, "/ui/api/answer", tt.body).Code)
		})
	}
	require.Equal(s.T(), http.StatusOK, (<-ch).Code)
}

func (s *ServerSuite) TestPauseAndStop() {
	require.Equal(s.T(), http.StatusBadRequest, s.ui(http.MethodPost, "/ui/api/pause", `x`).Code)
	require.Equal(s.T(), http.StatusNoContent, s.ui(http.MethodPost, "/ui/api/pause", `{"paused":true}`).Code)
	require.True(s.T(), s.srv.Summary().Paused)

	require.Equal(s.T(), http.StatusBadRequest, s.ui(http.MethodPost, "/ui/api/stop", `x`).Code)
	require.Equal(s.T(), http.StatusBadRequest, s.ui(http.MethodPost, "/ui/api/stop", `{}`).Code)
	require.Equal(s.T(), http.StatusNoContent, s.ui(http.MethodPost, "/ui/api/stop", `{"session":"s9"}`).Code)
	require.True(s.T(), s.srv.stopped["s9"])
}

func (s *ServerSuite) TestForget() {
	require.NoError(s.T(), s.store.Save(approval.Saved{BundleID: "com.a", TeamID: "T", Decision: approval.Allow}))
	require.Equal(s.T(), http.StatusBadRequest, s.ui(http.MethodDelete, "/ui/api/approvals", "").Code)
	require.Equal(s.T(), http.StatusNoContent, s.ui(http.MethodDelete, "/ui/api/approvals?bundle_id=com.a&team_id=T", "").Code)
	require.Empty(s.T(), s.store.List())

	// A non-empty directory in the file's place fails the rename, even as root.
	require.NoError(s.T(), os.Remove(s.storePath))
	require.NoError(s.T(), os.MkdirAll(filepath.Join(s.storePath, "x"), 0o700))
	require.Equal(s.T(), http.StatusInternalServerError, s.ui(http.MethodDelete, "/ui/api/approvals?bundle_id=com.a", "").Code)
}

func (s *ServerSuite) TestRequestPermissions() {
	s.engine.On("Handle", method(proto.MethodRequestPermissions)).Return(ok(proto.Permissions{Accessibility: true})).Once()
	rec := s.ui(http.MethodPost, "/ui/api/permissions", "")
	require.Equal(s.T(), http.StatusOK, rec.Code)
	require.JSONEq(s.T(), `{"accessibility":true,"screen_recording":false}`, rec.Body.String())

	s.engine.On("Handle", method(proto.MethodRequestPermissions)).Return(failed(proto.CodeUnsupported, "no")).Once()
	require.Equal(s.T(), http.StatusNotImplemented, s.ui(http.MethodPost, "/ui/api/permissions", "").Code)
}

func (s *ServerSuite) TestLoginItem() {
	require.Equal(s.T(), http.StatusBadRequest, s.ui(http.MethodPost, "/ui/api/login-item", `x`).Code)
	s.host.On("SetLoginItem", true).Return(nil).Once()
	require.Equal(s.T(), http.StatusNoContent, s.ui(http.MethodPost, "/ui/api/login-item", `{"enabled":true}`).Code)
	s.host.On("SetLoginItem", false).Return(errors.New("launchctl failed")).Once()
	rec := s.ui(http.MethodPost, "/ui/api/login-item", `{"enabled":false}`)
	require.Equal(s.T(), http.StatusInternalServerError, rec.Code)
	require.Contains(s.T(), rec.Body.String(), "launchctl failed")
}

func (s *ServerSuite) TestQuit() {
	s.host.On("Quit").Return().Once()
	require.Equal(s.T(), http.StatusNoContent, s.ui(http.MethodPost, "/ui/api/quit", "").Code)
}

func (s *ServerSuite) TestStatusWaitsBoundedForPermissions() {
	release := make(chan struct{})
	defer close(release)
	s.engine.On("Handle", method(proto.MethodPermissions)).Run(func(mock.Arguments) { <-release }).Return(ok(proto.Permissions{}))
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.Nil(s.T(), s.srv.permissions(ctx))
	require.Less(s.T(), time.Since(start), permissionsWait)
}
