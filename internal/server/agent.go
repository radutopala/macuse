package server

import (
	"context"
	"net/http"
	"time"

	"github.com/radutopala/macuse/internal/approval"
	"github.com/radutopala/macuse/internal/proto"
)

// maxName caps the client-supplied names shown to the user.
const maxName = 64

func headerName(r *http.Request, key, fallback string) string {
	v := r.Header.Get(key)
	if v == "" {
		return fallback
	}
	if r := []rune(v); len(r) > maxName {
		v = string(r[:maxName])
	}
	return v
}

func callerFrom(r *http.Request) approval.Caller {
	return approval.Caller{
		Client:  headerName(r, proto.HeaderClient, "unknown client"),
		Session: headerName(r, proto.HeaderSession, "default"),
	}
}

// permissionsWait caps how long a status read waits behind a running
// action for the engine to report its grants.
const permissionsWait = 2 * time.Second

func (s *Server) permissions(ctx context.Context) *proto.Permissions {
	ctx, cancel := context.WithTimeout(ctx, permissionsWait)
	defer cancel()
	var p proto.Permissions
	if err := s.call(ctx, proto.MethodPermissions, nil, &p); err != nil {
		return nil
	}
	return &p
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	paused := s.paused
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, proto.Status{Version: s.Version, Paused: paused, Permissions: s.permissions(r.Context())})
}

func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	ctx, done, err := s.track(r.Context(), callerFrom(r))
	if err != nil {
		writeError(w, err)
		return
	}
	defer done()
	var list proto.AppList
	if err := s.call(ctx, proto.MethodListApps, nil, &list); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, proto.AppList{Apps: s.Policy.Filter(list.Apps)})
}

func (s *Server) handleStartApp(w http.ResponseWriter, r *http.Request) {
	var req proto.StartAppParams
	if !decode(w, r, &req) {
		return
	}
	s.gated(w, r, req.BundleID, proto.MethodStartApp, 0, func(ctx context.Context) (any, error) {
		var app proto.App
		err := s.call(ctx, proto.MethodStartApp, req, &app)
		return app, err
	})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	var req proto.GetStateParams
	if !decode(w, r, &req) {
		return
	}
	s.gated(w, r, req.BundleID, proto.MethodGetState, 0, func(ctx context.Context) (any, error) {
		var st proto.State
		err := s.call(ctx, proto.MethodGetState, req, &st)
		return st, err
	})
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	var req proto.ActionParams
	if !decode(w, r, &req) {
		return
	}
	typed := len([]rune(req.Text)) + len([]rune(req.Value))
	s.gated(w, r, req.BundleID, req.Action, typed, func(ctx context.Context) (any, error) {
		var res proto.ActionResult
		err := s.call(ctx, proto.MethodAction, req, &res)
		return res, err
	})
}

// gated runs one call on an app past the deny list and the user's
// approval, auditing the decision.
func (s *Server) gated(w http.ResponseWriter, r *http.Request, bundleID, action string, typed int, run func(context.Context) (any, error)) {
	if bundleID == "" {
		writeError(w, fail(proto.CodeInvalidParams, "bundle_id is required"))
		return
	}
	c := callerFrom(r)
	entry := Entry{Client: c.Client, Session: c.Session, BundleID: bundleID, Action: action, TypedChars: typed}
	if s.Policy.Denied(bundleID) {
		entry.Decision, entry.Rule = "deny", "deny-list"
		s.audit(entry)
		writeError(w, fail(proto.CodeDenied, bundleID+" is on the deny list; agents can never control it"))
		return
	}
	ctx, done, err := s.track(r.Context(), c)
	if err != nil {
		writeError(w, err)
		return
	}
	defer done()

	app, running, err := s.findApp(ctx, bundleID)
	if err != nil {
		s.writeCallError(ctx, w, r, c, err)
		return
	}
	if !running && action != proto.MethodStartApp {
		writeError(w, fail(proto.CodeNotRunning, bundleID+" isn't running; start it with start_app"))
		return
	}
	entry.App, entry.TeamID = app.Name, app.TeamID
	allowed, rule, err := s.Gate.Check(ctx, c, app, running, action)
	if err != nil {
		s.writeCallError(ctx, w, r, c, err)
		return
	}
	entry.Rule = rule
	if !allowed {
		entry.Decision = "deny"
		s.audit(entry)
		msg := "the user didn't allow controlling " + app.Name
		if rule == approval.RuleTimeout {
			msg = "the user didn't answer the request to control " + app.Name + " in time"
		}
		writeError(w, fail(proto.CodeDenied, msg))
		return
	}
	entry.Decision = "allow"
	s.audit(entry)
	s.recordActivity(c, app, action)

	out, err := run(ctx)
	if err != nil {
		s.writeCallError(ctx, w, r, c, err)
		return
	}
	if started, ok := out.(proto.App); ok && !running && rule != approval.RuleSaved {
		// The user answered before the app ran; record it under the
		// identity the launched app has.
		if err := s.Gate.Launched(c, started); err != nil {
			s.Logger.Error("saving approval failed", "bundle_id", started.BundleID, "error", err)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// writeCallError answers a failed call, telling a cancel by pause or stop
// apart from the engine's errors.
func (s *Server) writeCallError(ctx context.Context, w http.ResponseWriter, r *http.Request, c approval.Caller, err error) {
	if ctx.Err() != nil && r.Context().Err() == nil {
		err = s.interrupted(c.Session)
	}
	writeError(w, err)
}

// findApp looks bundleID up among the running apps. An app that isn't
// running gets its bundle id as its name.
func (s *Server) findApp(ctx context.Context, bundleID string) (proto.App, bool, error) {
	var list proto.AppList
	if err := s.call(ctx, proto.MethodListApps, nil, &list); err != nil {
		return proto.App{}, false, err
	}
	for _, a := range list.Apps {
		if a.BundleID == bundleID {
			return a, true, nil
		}
	}
	return proto.App{BundleID: bundleID, Name: bundleID}, false, nil
}

func (s *Server) audit(e Entry) {
	if err := s.Audit.Write(e); err != nil {
		s.Logger.Error("writing audit log failed", "error", err)
	}
}
