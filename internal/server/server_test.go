package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/radutopala/macuse/internal/approval"
	"github.com/radutopala/macuse/internal/policy"
	"github.com/radutopala/macuse/internal/proto"
	"github.com/radutopala/macuse/internal/update"
)

type mockEngine struct{ mock.Mock }

func (m *mockEngine) Handle(_ context.Context, req proto.Request) proto.Response {
	return m.Called(req).Get(0).(proto.Response)
}

type mockHost struct{ mock.Mock }

func (m *mockHost) LoginItem() bool            { return m.Called().Bool(0) }
func (m *mockHost) SetLoginItem(on bool) error { return m.Called(on).Error(0) }
func (m *mockHost) CLI() string                { return m.Called().String(0) }
func (m *mockHost) InstallCLI() error          { return m.Called().Error(0) }
func (m *mockHost) Quit()                      { m.Called() }

type mockUpdater struct{ mock.Mock }

func (m *mockUpdater) Status() update.Status { return m.Called().Get(0).(update.Status) }
func (m *mockUpdater) CheckNow(ctx context.Context) update.Status {
	return m.Called(ctx).Get(0).(update.Status)
}
func (m *mockUpdater) InstallNow(ctx context.Context) error { return m.Called(ctx).Error(0) }

func method(name string) any {
	return mock.MatchedBy(func(r proto.Request) bool { return r.Method == name })
}

func ok(v any) proto.Response {
	data, _ := json.Marshal(v)
	return proto.Response{Result: data}
}

func failed(code, msg string) proto.Response {
	return proto.Response{Error: &proto.Error{Code: code, Message: msg}}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

var (
	t0    = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	notes = proto.App{BundleID: "com.apple.Notes", Name: "Notes", PID: 7, TeamID: "APPLE"}
)

type ServerSuite struct {
	suite.Suite
	engine    *mockEngine
	host      *mockHost
	store     *approval.Store
	storePath string
	broker    *approval.Broker
	audit     bytes.Buffer
	logs      bytes.Buffer
	now       time.Time
	notified  atomic.Int32
	srv       *Server
	h         http.Handler
}

func TestServerSuite(t *testing.T) { suite.Run(t, new(ServerSuite)) }

func (s *ServerSuite) SetupTest() {
	s.engine = new(mockEngine)
	s.host = new(mockHost)
	s.storePath = filepath.Join(s.T().TempDir(), "approvals.json")
	var err error
	s.store, err = approval.OpenStore(s.storePath, func() time.Time { return t0 })
	require.NoError(s.T(), err)
	s.broker = approval.NewBroker(time.Minute, func() time.Time { return t0 }, nil)
	s.audit.Reset()
	s.logs.Reset()
	s.now = t0
	s.notified.Store(0)
	s.srv = New(Deps{
		Engine:  s.engine,
		Gate:    approval.NewGate(s.store, s.broker),
		Broker:  s.broker,
		Store:   s.store,
		Policy:  policy.NewPolicy([]string{"com.example.secret"}),
		Audit:   NewAudit(&s.audit, func() time.Time { return t0 }),
		Host:    s.host,
		Logger:  slog.New(slog.NewTextHandler(&s.logs, nil)),
		Now:     func() time.Time { return s.now },
		Token:   "tok",
		UIKey:   "uikey",
		Version: "v1.2.3",
		Notify:  func() { s.notified.Add(1) },
	})
	s.h = s.srv.Handler()
}

func (s *ServerSuite) TearDownTest() {
	s.engine.AssertExpectations(s.T())
	s.host.AssertExpectations(s.T())
}

// do sends an agent request.
func (s *ServerSuite) do(ctx context.Context, verb, path, body string, headers ...string) *httptest.ResponseRecorder {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(verb, path, r).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer tok")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	return rec
}

func (s *ServerSuite) agent(verb, path, body string) *httptest.ResponseRecorder {
	return s.do(context.Background(), verb, path, body, proto.HeaderClient, "claude-code", proto.HeaderSession, "s1")
}

// async sends an agent request in the background.
func (s *ServerSuite) async(ctx context.Context, verb, path, body string) <-chan *httptest.ResponseRecorder {
	ch := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		ch <- s.do(ctx, verb, path, body, proto.HeaderClient, "claude-code", proto.HeaderSession, "s1")
	}()
	return ch
}

func (s *ServerSuite) ui(verb, path, body string) *httptest.ResponseRecorder {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(verb, path, r)
	req.Header.Set(HeaderUIKey, "uikey")
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	return rec
}

func errCode(s *ServerSuite, rec *httptest.ResponseRecorder) string {
	var body proto.ErrorBody
	require.NoError(s.T(), json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	require.NotNil(s.T(), body.Error)
	return body.Error.Code
}

func (s *ServerSuite) apps(apps ...proto.App) {
	s.engine.On("Handle", method(proto.MethodListApps)).Return(ok(proto.AppList{Apps: apps}))
}

func (s *ServerSuite) waitPending() approval.Request {
	require.Eventually(s.T(), func() bool { return len(s.broker.Pending()) == 1 }, time.Second, time.Millisecond)
	return s.broker.Pending()[0]
}

func (s *ServerSuite) auditLines() []Entry {
	var out []Entry
	for _, line := range strings.Split(strings.TrimSpace(s.audit.String()), "\n") {
		if line == "" {
			continue
		}
		var e Entry
		require.NoError(s.T(), json.Unmarshal([]byte(line), &e))
		out = append(out, e)
	}
	return out
}

func (s *ServerSuite) TestAgentRoutesNeedToken() {
	req := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	require.Equal(s.T(), http.StatusUnauthorized, rec.Code)
}

func (s *ServerSuite) TestStatus() {
	s.engine.On("Handle", method(proto.MethodPermissions)).Return(ok(proto.Permissions{Accessibility: true})).Once()
	rec := s.agent(http.MethodGet, "/v1/status", "")
	require.Equal(s.T(), http.StatusOK, rec.Code)
	require.JSONEq(s.T(), `{"version":"v1.2.3","paused":false,"permissions":{"accessibility":true,"screen_recording":false}}`, rec.Body.String())

	s.engine.On("Handle", method(proto.MethodPermissions)).Return(failed(proto.CodeInternal, "x")).Once()
	rec = s.agent(http.MethodGet, "/v1/status", "")
	require.JSONEq(s.T(), `{"version":"v1.2.3","paused":false}`, rec.Body.String())
}

func (s *ServerSuite) TestListApps() {
	s.apps(notes, proto.App{BundleID: "com.example.secret", Name: "Secret"}, proto.App{BundleID: "com.apple.Terminal"})
	rec := s.agent(http.MethodGet, "/v1/apps", "")
	require.Equal(s.T(), http.StatusOK, rec.Code)
	var list proto.AppList
	require.NoError(s.T(), json.Unmarshal(rec.Body.Bytes(), &list))
	require.Equal(s.T(), []proto.App{notes}, list.Apps)
}

func (s *ServerSuite) TestListAppsErrors() {
	s.engine.On("Handle", method(proto.MethodListApps)).Return(failed(proto.CodePermission, "grant it")).Once()
	rec := s.agent(http.MethodGet, "/v1/apps", "")
	require.Equal(s.T(), http.StatusForbidden, rec.Code)

	s.srv.SetPaused(true)
	rec = s.agent(http.MethodGet, "/v1/apps", "")
	require.Equal(s.T(), http.StatusConflict, rec.Code)
	require.Equal(s.T(), proto.CodePaused, errCode(s, rec))
}

func (s *ServerSuite) TestActionPromptsThenUsesSessionAllow() {
	s.apps(notes)
	s.engine.On("Handle", mock.MatchedBy(func(r proto.Request) bool {
		var p proto.ActionParams
		return r.Method == proto.MethodAction && json.Unmarshal(r.Params, &p) == nil && p.Text == "héllo"
	})).Return(ok(proto.ActionResult{Message: "typed"}))

	ch := s.async(context.Background(), http.MethodPost, "/v1/action", `{"bundle_id":"com.apple.Notes","action":"type","text":"héllo"}`)
	req := s.waitPending()
	require.Equal(s.T(), "claude-code", req.Client)
	require.Equal(s.T(), "s1", req.Session)
	require.Equal(s.T(), "type", req.Action)
	_, err := s.broker.Answer(req.ID, approval.AnswerSession)
	require.NoError(s.T(), err)
	rec := <-ch
	require.Equal(s.T(), http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(s.T(), `{"message":"typed"}`, rec.Body.String())

	rec = s.agent(http.MethodPost, "/v1/action", `{"bundle_id":"com.apple.Notes","action":"type","text":"héllo"}`)
	require.Equal(s.T(), http.StatusOK, rec.Code)

	lines := s.auditLines()
	require.Len(s.T(), lines, 2)
	require.Equal(s.T(), Entry{
		Time: t0, Client: "claude-code", Session: "s1", App: "Notes", BundleID: "com.apple.Notes",
		TeamID: "APPLE", Action: "type", Decision: "allow", Rule: approval.RulePrompt, TypedChars: 5,
	}, lines[0])
	require.Equal(s.T(), approval.RuleSession, lines[1].Rule)
	require.NotContains(s.T(), s.audit.String(), "héllo")

	acts := s.srv.Activities()
	require.Len(s.T(), acts, 1)
	require.Equal(s.T(), "Notes", acts[0].App)
}

func (s *ServerSuite) TestStateDeniedAndTimedOut() {
	s.apps(notes)
	ch := s.async(context.Background(), http.MethodPost, "/v1/state", `{"bundle_id":"com.apple.Notes"}`)
	_, err := s.broker.Answer(s.waitPending().ID, approval.AnswerDeny)
	require.NoError(s.T(), err)
	rec := <-ch
	require.Equal(s.T(), http.StatusForbidden, rec.Code)
	require.Contains(s.T(), rec.Body.String(), "didn't allow controlling Notes")

	s.srv.Gate = approval.NewGate(s.store, approval.NewBroker(time.Millisecond, time.Now, nil))
	rec = s.agent(http.MethodPost, "/v1/state", `{"bundle_id":"com.apple.Notes"}`)
	require.Equal(s.T(), http.StatusForbidden, rec.Code)
	require.Contains(s.T(), rec.Body.String(), "didn't answer")
	require.Equal(s.T(), approval.RuleTimeout, s.auditLines()[1].Rule)
}

func (s *ServerSuite) TestStateSavedAllow() {
	require.NoError(s.T(), s.store.Save(approval.Saved{BundleID: notes.BundleID, TeamID: notes.TeamID, Decision: approval.Allow}))
	s.apps(notes)
	s.engine.On("Handle", method(proto.MethodGetState)).Return(ok(proto.State{App: notes, Window: "Notes"}))
	rec := s.agent(http.MethodPost, "/v1/state", `{"bundle_id":"com.apple.Notes","capture":"text"}`)
	require.Equal(s.T(), http.StatusOK, rec.Code)
	var st proto.State
	require.NoError(s.T(), json.Unmarshal(rec.Body.Bytes(), &st))
	require.Equal(s.T(), "Notes", st.Window)
}

func (s *ServerSuite) TestBatchIsGatedAndAuditedAsOneCall() {
	require.NoError(s.T(), s.store.Save(approval.Saved{BundleID: notes.BundleID, TeamID: notes.TeamID, Decision: approval.Allow}))
	s.apps(notes)
	s.engine.On("Handle", mock.MatchedBy(func(r proto.Request) bool {
		var p proto.BatchParams
		return r.Method == proto.MethodBatch && json.Unmarshal(r.Params, &p) == nil && len(p.Actions) == 3
	})).Return(ok(proto.ActionResult{Message: "3 of 3 actions done in Notes"})).Once()
	rec := s.agent(http.MethodPost, "/v1/batch", `{"bundle_id":"com.apple.Notes","actions":[
		{"action":"type","text":"hé"},{"action":"set_value","index":2,"value":"v"},{"action":"click","index":1}]}`)
	require.Equal(s.T(), http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(s.T(), `{"message":"3 of 3 actions done in Notes"}`, rec.Body.String())

	lines := s.auditLines()
	require.Len(s.T(), lines, 1)
	require.Equal(s.T(), proto.MethodBatch, lines[0].Action)
	require.Equal(s.T(), 3, lines[0].TypedChars)
	require.Equal(s.T(), proto.MethodBatch, s.srv.Activities()[0].Action)

	rec = s.agent(http.MethodPost, "/v1/batch", `{`)
	require.Equal(s.T(), http.StatusBadRequest, rec.Code)
}

func (s *ServerSuite) TestGatedRefusals() {
	tests := []struct {
		name   string
		setup  func()
		path   string
		body   string
		status int
		code   string
	}{
		{"bad JSON", func() {}, "/v1/action", `{`, http.StatusBadRequest, proto.CodeInvalidParams},
		{"bad JSON state", func() {}, "/v1/state", `{`, http.StatusBadRequest, proto.CodeInvalidParams},
		{"bad JSON start", func() {}, "/v1/apps/start", `{`, http.StatusBadRequest, proto.CodeInvalidParams},
		{"no bundle id", func() {}, "/v1/action", `{"action":"click"}`, http.StatusBadRequest, proto.CodeInvalidParams},
		{"deny list", func() {}, "/v1/action", `{"bundle_id":"com.apple.Terminal","action":"type","text":"rm"}`, http.StatusForbidden, proto.CodeDenied},
		{"paused", func() { s.srv.SetPaused(true) }, "/v1/state", `{"bundle_id":"com.apple.Notes"}`, http.StatusConflict, proto.CodePaused},
		{"stopped session", func() { s.srv.Stop("s1") }, "/v1/state", `{"bundle_id":"com.apple.Notes"}`, http.StatusConflict, proto.CodeStopped},
		{"list fails", func() {
			s.engine.On("Handle", method(proto.MethodListApps)).Return(failed(proto.CodeHelperUnavailable, "down")).Once()
		}, "/v1/state", `{"bundle_id":"com.apple.Notes"}`, http.StatusServiceUnavailable, proto.CodeHelperUnavailable},
		{"not running", func() { s.apps() }, "/v1/state", `{"bundle_id":"com.apple.Notes"}`, http.StatusNotFound, proto.CodeNotRunning},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.SetupTest()
			tt.setup()
			rec := s.agent(http.MethodPost, tt.path, tt.body)
			require.Equal(s.T(), tt.status, rec.Code, rec.Body.String())
			require.Equal(s.T(), tt.code, errCode(s, rec))
		})
	}
	s.Run("deny list is audited", func() {
		s.SetupTest()
		s.agent(http.MethodPost, "/v1/action", `{"bundle_id":"com.apple.Terminal","action":"type","text":"rm -rf"}`)
		lines := s.auditLines()
		require.Len(s.T(), lines, 1)
		require.Equal(s.T(), "deny-list", lines[0].Rule)
		require.Equal(s.T(), 6, lines[0].TypedChars)
	})
}

func (s *ServerSuite) TestStartAppAlwaysSavesLaunchedIdentity() {
	s.engine.On("Handle", method(proto.MethodListApps)).Return(ok(proto.AppList{})).Once()
	s.engine.On("Handle", method(proto.MethodStartApp)).Return(ok(notes)).Once()

	ch := s.async(context.Background(), http.MethodPost, "/v1/apps/start", `{"bundle_id":"com.apple.Notes"}`)
	req := s.waitPending()
	require.Equal(s.T(), "com.apple.Notes", req.App.Name)
	_, err := s.broker.Answer(req.ID, approval.AnswerAlways)
	require.NoError(s.T(), err)
	rec := <-ch
	require.Equal(s.T(), http.StatusOK, rec.Code, rec.Body.String())

	saved, found := s.store.Lookup(notes.BundleID, notes.TeamID)
	require.True(s.T(), found)
	require.Equal(s.T(), approval.Allow, saved.Decision)
}

func (s *ServerSuite) TestStartAppSavedDecisionDoesNotExtendToNewTeam() {
	require.NoError(s.T(), s.store.Save(approval.Saved{BundleID: notes.BundleID, TeamID: "OTHER", Decision: approval.Allow}))
	s.engine.On("Handle", method(proto.MethodListApps)).Return(ok(proto.AppList{})).Once()
	s.engine.On("Handle", method(proto.MethodStartApp)).Return(ok(notes)).Once()
	rec := s.agent(http.MethodPost, "/v1/apps/start", `{"bundle_id":"com.apple.Notes"}`)
	require.Equal(s.T(), http.StatusOK, rec.Code)

	// Acting on the launched app, signed by another team, asks again.
	s.apps(notes)
	ch := s.async(context.Background(), http.MethodPost, "/v1/state", `{"bundle_id":"com.apple.Notes"}`)
	_, err := s.broker.Answer(s.waitPending().ID, approval.AnswerDeny)
	require.NoError(s.T(), err)
	require.Equal(s.T(), http.StatusForbidden, (<-ch).Code)
}

func (s *ServerSuite) TestStartAppSaveFailureIsLogged() {
	require.NoError(s.T(), os.MkdirAll(filepath.Join(s.storePath, "x"), 0o700))
	s.engine.On("Handle", method(proto.MethodListApps)).Return(ok(proto.AppList{})).Once()
	s.engine.On("Handle", method(proto.MethodStartApp)).Return(ok(notes)).Once()
	ch := s.async(context.Background(), http.MethodPost, "/v1/apps/start", `{"bundle_id":"com.apple.Notes"}`)
	_, err := s.broker.Answer(s.waitPending().ID, approval.AnswerAlways)
	require.NoError(s.T(), err)
	require.Equal(s.T(), http.StatusOK, (<-ch).Code)
	require.Contains(s.T(), s.logs.String(), "saving approval failed")
}

func (s *ServerSuite) TestRunErrors() {
	require.NoError(s.T(), s.store.Save(approval.Saved{BundleID: notes.BundleID, TeamID: notes.TeamID, Decision: approval.Allow}))
	s.apps(notes)
	tests := []struct {
		name   string
		resp   proto.Response
		status int
		code   string
	}{
		{"element missing", failed(proto.CodeElementNotFound, "no [9]"), http.StatusNotFound, proto.CodeElementNotFound},
		{"unknown code", failed("weird", "?"), http.StatusInternalServerError, "weird"},
		{"bad result", proto.Response{Result: json.RawMessage(`[`)}, http.StatusInternalServerError, proto.CodeInternal},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.engine.On("Handle", method(proto.MethodAction)).Return(tt.resp).Once()
			rec := s.agent(http.MethodPost, "/v1/action", `{"bundle_id":"com.apple.Notes","action":"click","index":9}`)
			require.Equal(s.T(), tt.status, rec.Code)
			require.Equal(s.T(), tt.code, errCode(s, rec))
		})
	}
}

func (s *ServerSuite) TestPauseInterruptsRunningCall() {
	require.NoError(s.T(), s.store.Save(approval.Saved{BundleID: notes.BundleID, TeamID: notes.TeamID, Decision: approval.Allow}))
	s.apps(notes)
	release := make(chan struct{})
	defer close(release)
	started := make(chan struct{})
	s.engine.On("Handle", method(proto.MethodAction)).Run(func(mock.Arguments) {
		close(started)
		<-release
	}).Return(ok(proto.ActionResult{}))

	ch := s.async(context.Background(), http.MethodPost, "/v1/action", `{"bundle_id":"com.apple.Notes","action":"click","index":1}`)
	<-started
	require.True(s.T(), s.srv.Summary().Active)
	s.srv.SetPaused(true)
	rec := <-ch
	require.Equal(s.T(), http.StatusConflict, rec.Code)
	require.Equal(s.T(), proto.CodePaused, errCode(s, rec))
	require.True(s.T(), s.srv.Summary().Paused)
}

func (s *ServerSuite) TestStopInterruptsPrompt() {
	s.apps(notes)
	ch := s.async(context.Background(), http.MethodPost, "/v1/state", `{"bundle_id":"com.apple.Notes"}`)
	s.waitPending()
	s.srv.Stop("s1")
	rec := <-ch
	require.Equal(s.T(), proto.CodeStopped, errCode(s, rec))
	// It never got past the prompt, so it never acted.
	require.Empty(s.T(), s.srv.Activities())
}

func (s *ServerSuite) TestClientGoneIsNotReportedAsStop() {
	s.apps(notes)
	ctx, cancel := context.WithCancel(context.Background())
	ch := s.async(ctx, http.MethodPost, "/v1/state", `{"bundle_id":"com.apple.Notes"}`)
	s.waitPending()
	cancel()
	rec := <-ch
	require.Equal(s.T(), proto.CodeInternal, errCode(s, rec))
}

func (s *ServerSuite) TestCallerHeaders() {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	require.Equal(s.T(), approval.Caller{Client: "unknown client", Session: "default"}, callerFrom(req))
	req.Header.Set(proto.HeaderClient, strings.Repeat("é", 70))
	require.Equal(s.T(), strings.Repeat("é", maxName), callerFrom(req).Client)
}

func (s *ServerSuite) TestActivityAndSummary() {
	s.srv.recordActivity(approval.Caller{Client: "a", Session: "old"}, notes, "click")
	s.now = t0.Add(time.Minute)
	s.srv.recordActivity(approval.Caller{Client: "b", Session: "new"}, notes, "type")
	acts := s.srv.Activities()
	require.Equal(s.T(), []string{"new", "old"}, []string{acts[0].Session, acts[1].Session})
	require.Equal(s.T(), Summary{Active: true}, s.srv.Summary())

	s.now = t0.Add(time.Minute + activeFor)
	require.Equal(s.T(), Summary{}, s.srv.Summary())

	s.now = t0.Add(keepActivity + 2*time.Second)
	require.Len(s.T(), s.srv.Activities(), 1)

	s.srv.Stop("new")
	require.True(s.T(), s.srv.Activities()[0].Stopped)
	s.srv.Stop("nobody")
	s.srv.Notify = nil
	s.srv.SetPaused(false)
	require.Equal(s.T(), int32(4), s.notified.Load())
}

func (s *ServerSuite) TestStopAndResumeAreAudited() {
	s.srv.recordActivity(approval.Caller{Client: "claude", Session: "s1"}, notes, "click")
	s.srv.Stop("s1")
	require.True(s.T(), s.srv.Activities()[0].Stopped)
	s.srv.Resume("s1")
	require.False(s.T(), s.srv.Activities()[0].Stopped)
	s.srv.Stop("nobody")

	at := t0.UTC()
	require.Equal(s.T(), []Entry{
		{Time: at, Client: "claude", Session: "s1", App: notes.Name, BundleID: notes.BundleID, TeamID: notes.TeamID, Action: "stop", Decision: "deny", Rule: RuleUser},
		{Time: at, Client: "claude", Session: "s1", App: notes.Name, BundleID: notes.BundleID, TeamID: notes.TeamID, Action: "resume", Decision: "allow", Rule: RuleUser},
		{Time: at, Session: "nobody", Action: "stop", Decision: "deny", Rule: RuleUser},
	}, s.auditLines())
}

func (s *ServerSuite) TestResumedSessionIsServed() {
	s.srv.Stop("s1")
	require.Equal(s.T(), proto.CodeStopped, errCode(s, s.agent(http.MethodPost, "/v1/state", `{"bundle_id":"com.apple.Notes"}`)))
	s.srv.Resume("s1")
	_, done, err := s.srv.track(context.Background(), approval.Caller{Session: "s1"})
	require.NoError(s.T(), err)
	done()
}

func (s *ServerSuite) TestAuditWriteFailureIsLogged() {
	s.srv.Audit = NewAudit(failWriter{}, time.Now)
	rec := s.agent(http.MethodPost, "/v1/action", `{"bundle_id":"com.apple.Terminal","action":"key","keys":"cmd+q"}`)
	require.Equal(s.T(), http.StatusForbidden, rec.Code)
	require.Contains(s.T(), s.logs.String(), "writing audit log failed")
}

func (s *ServerSuite) TestWriteErrorWrapsPlainErrors() {
	rec := httptest.NewRecorder()
	writeError(rec, errors.New("boom"))
	require.Equal(s.T(), http.StatusInternalServerError, rec.Code)
	require.Equal(s.T(), proto.CodeInternal, errCode(s, rec))
}
