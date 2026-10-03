// Package server is the macuse HTTP API: the agent routes under /v1,
// behind the bearer token, and the menu bar popup's routes under /ui,
// behind a key only the running app knows.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/radutopala/macuse/internal/approval"
	"github.com/radutopala/macuse/internal/auth"
	"github.com/radutopala/macuse/internal/policy"
	"github.com/radutopala/macuse/internal/proto"
	"github.com/radutopala/macuse/internal/update"
)

// Engine serves desktop requests. Satisfied by *core.Service.
type Engine interface {
	Handle(req proto.Request) proto.Response
}

// Host is what the popup controls beyond the API: the login item, the CLI
// link and the app itself.
type Host interface {
	LoginItem() bool
	SetLoginItem(on bool) error
	// CLI is "installed" or "missing", or "" where this copy can't link
	// the CLI because it doesn't run from the app.
	CLI() string
	InstallCLI() error
	Quit()
}

// Updater keeps the app current. Satisfied by *update.Manager.
type Updater interface {
	Status() update.Status
	CheckNow(ctx context.Context) update.Status
	InstallNow(ctx context.Context) error
}

// Deps wires a Server.
type Deps struct {
	Engine Engine
	Gate   *approval.Gate
	Broker *approval.Broker
	Store  *approval.Store
	Policy *policy.Policy
	Audit  *Audit
	Host   Host
	// Updater is nil where the app can't update itself: builds between
	// releases, and the binary run outside the app.
	Updater Updater
	Logger  *slog.Logger
	Now     func() time.Time
	Token   string
	UIKey   string
	Version string
	// Notify, if set, runs whenever what the menu bar shows may have
	// changed: activity, pause, a stopped session.
	Notify func()
}

// Server serves the API.
type Server struct {
	Deps

	mu       sync.Mutex
	paused   bool
	stopped  map[string]bool
	inflight map[int]inflight
	activity map[string]Activity
	nextCall int
}

type inflight struct {
	session string
	cancel  context.CancelFunc
}

// Activity is the latest thing a session did.
type Activity struct {
	Client   string    `json:"client"`
	Session  string    `json:"session"`
	App      string    `json:"app"`
	BundleID string    `json:"bundle_id"`
	Action   string    `json:"action"`
	At       time.Time `json:"at"`
	Stopped  bool      `json:"stopped,omitempty"`
}

// New returns a Server over d.
func New(d Deps) *Server {
	return &Server{
		Deps:     d,
		stopped:  map[string]bool{},
		inflight: map[int]inflight{},
		activity: map[string]Activity{},
	}
}

// Handler routes the API.
func (s *Server) Handler() http.Handler {
	agent := http.NewServeMux()
	agent.HandleFunc("GET /v1/status", s.handleStatus)
	agent.HandleFunc("GET /v1/apps", s.handleListApps)
	agent.HandleFunc("POST /v1/apps/start", s.handleStartApp)
	agent.HandleFunc("POST /v1/state", s.handleState)
	agent.HandleFunc("POST /v1/action", s.handleAction)

	ui := http.NewServeMux()
	s.registerUI(ui)

	mux := http.NewServeMux()
	mux.Handle("/v1/", auth.Bearer(s.Token, agent))
	mux.Handle("/ui", s.uiAuth(ui))
	mux.Handle("/ui/", s.uiAuth(ui))
	return mux
}

// call runs one engine request. The engine can't be interrupted, so a
// cancelled ctx only stops the wait; the engine's answer is dropped.
func (s *Server) call(ctx context.Context, method string, params, out any) error {
	var raw json.RawMessage
	if params != nil {
		raw, _ = json.Marshal(params)
	}
	done := make(chan proto.Response, 1)
	go func() { done <- s.Engine.Handle(proto.Request{Method: method, Params: raw}) }()
	select {
	case resp := <-done:
		if resp.Error != nil {
			return resp.Error
		}
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return &proto.Error{Code: proto.CodeInternal, Message: "bad engine result: " + err.Error()}
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// errorStatus maps error codes to HTTP statuses; unlisted codes are 500.
var errorStatus = map[string]int{
	proto.CodeInvalidParams:     http.StatusBadRequest,
	proto.CodeUnknownMethod:     http.StatusBadRequest,
	proto.CodeUnsupported:       http.StatusNotImplemented,
	proto.CodePermission:        http.StatusForbidden,
	proto.CodeDenied:            http.StatusForbidden,
	proto.CodeAppNotFound:       http.StatusNotFound,
	proto.CodeElementNotFound:   http.StatusNotFound,
	proto.CodeNotRunning:        http.StatusNotFound,
	proto.CodeNoState:           http.StatusConflict,
	proto.CodeUserActive:        http.StatusConflict,
	proto.CodeNotFrontmost:      http.StatusConflict,
	proto.CodePaused:            http.StatusConflict,
	proto.CodeStopped:           http.StatusConflict,
	proto.CodeHelperUnavailable: http.StatusServiceUnavailable,
	proto.CodeUnauthorized:      http.StatusUnauthorized,
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError answers with err's message, written for the agent to act on.
func writeError(w http.ResponseWriter, err error) {
	var pe *proto.Error
	if !errors.As(err, &pe) {
		pe = &proto.Error{Code: proto.CodeInternal, Message: err.Error()}
	}
	status, ok := errorStatus[pe.Code]
	if !ok {
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, proto.ErrorBody{Error: pe})
}

func fail(code, msg string) error { return &proto.Error{Code: code, Message: msg} }

// maxBody caps request bodies; actions carry at most some typed text.
const maxBody = 1 << 20

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(v); err != nil {
		writeError(w, fail(proto.CodeInvalidParams, "bad JSON body: "+err.Error()))
		return false
	}
	return true
}

func (s *Server) notify() {
	if s.Notify != nil {
		s.Notify()
	}
}
