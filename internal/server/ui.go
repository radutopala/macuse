package server

import (
	_ "embed"
	"errors"
	"net/http"

	"github.com/radutopala/mac-use/internal/approval"
	"github.com/radutopala/mac-use/internal/auth"
	"github.com/radutopala/mac-use/internal/proto"
)

// HeaderUIKey carries the popup's key on its API calls. The page itself is
// loaded with the key in the query, which only the app knows.
const HeaderUIKey = "X-Mac-Use-UI-Key"

//go:embed ui/index.html
var indexHTML []byte

// uiAuth serves next only for requests carrying the UI key. A custom
// header can't be sent cross-origin without a CORS preflight, which is
// never answered, so other pages can't drive the popup's API.
func (s *Server) uiAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(HeaderUIKey)
		if r.Method == http.MethodGet && r.URL.Path == "/ui" {
			key = r.URL.Query().Get("key")
		}
		if s.UIKey == "" || !auth.Equal(key, s.UIKey) {
			writeError(w, fail(proto.CodeUnauthorized, "the popup is served only to the mac-use app"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) registerUI(mux *http.ServeMux) {
	mux.HandleFunc("GET /ui", s.handleIndex)
	mux.HandleFunc("GET /ui/api/state", s.handleUIState)
	mux.HandleFunc("POST /ui/api/answer", s.handleAnswer)
	mux.HandleFunc("POST /ui/api/pause", s.handlePause)
	mux.HandleFunc("POST /ui/api/stop", s.handleStop)
	mux.HandleFunc("DELETE /ui/api/approvals", s.handleForget)
	mux.HandleFunc("POST /ui/api/permissions", s.handleRequestPermissions)
	mux.HandleFunc("POST /ui/api/login-item", s.handleLoginItem)
	mux.HandleFunc("POST /ui/api/quit", s.handleQuit)
}

func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'")
	_, _ = w.Write(indexHTML)
}

// UIState is everything the popup shows.
type UIState struct {
	Version     string             `json:"version"`
	Paused      bool               `json:"paused"`
	Pending     []approval.Request `json:"pending"`
	Approvals   []approval.Saved   `json:"approvals"`
	Activity    []Activity         `json:"activity"`
	Permissions *proto.Permissions `json:"permissions,omitempty"`
	LoginItem   bool               `json:"login_item"`
}

func (s *Server) handleUIState(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	paused := s.paused
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, UIState{
		Version:     s.Version,
		Paused:      paused,
		Pending:     s.Broker.Pending(),
		Approvals:   s.Store.List(),
		Activity:    s.Activities(),
		Permissions: s.permissions(r.Context()),
		LoginItem:   s.Host.LoginItem(),
	})
}

type answerRequest struct {
	ID     string          `json:"id"`
	Answer approval.Answer `json:"answer"`
}

func (s *Server) handleAnswer(w http.ResponseWriter, r *http.Request) {
	var req answerRequest
	if !decode(w, r, &req) {
		return
	}
	if !req.Answer.Valid() {
		writeError(w, fail(proto.CodeInvalidParams, "answer must be deny, session or always"))
		return
	}
	if _, err := s.Broker.Answer(req.ID, req.Answer); err != nil {
		writeError(w, fail(proto.CodeInvalidParams, err.Error()))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type pauseRequest struct {
	Paused bool `json:"paused"`
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	var req pauseRequest
	if !decode(w, r, &req) {
		return
	}
	s.SetPaused(req.Paused)
	w.WriteHeader(http.StatusNoContent)
}

type stopRequest struct {
	Session string `json:"session"`
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	var req stopRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Session == "" {
		writeError(w, fail(proto.CodeInvalidParams, "session is required"))
		return
	}
	s.Stop(req.Session)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleForget(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	bundleID := q.Get("bundle_id")
	if bundleID == "" {
		writeError(w, fail(proto.CodeInvalidParams, "bundle_id is required"))
		return
	}
	if err := s.Store.Delete(bundleID, q.Get("team_id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRequestPermissions asks macOS for the grants mac-use lacks, which
// shows the system prompts or opens their Settings panes.
func (s *Server) handleRequestPermissions(w http.ResponseWriter, r *http.Request) {
	var p proto.Permissions
	if err := s.call(r.Context(), proto.MethodRequestPermissions, nil, &p); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

type loginItemRequest struct {
	Enabled bool `json:"enabled"`
}

func (s *Server) handleLoginItem(w http.ResponseWriter, r *http.Request) {
	var req loginItemRequest
	if !decode(w, r, &req) {
		return
	}
	if err := s.Host.SetLoginItem(req.Enabled); err != nil {
		writeError(w, errors.New("changing the login item failed: "+err.Error()))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleQuit(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
	s.Host.Quit()
}
