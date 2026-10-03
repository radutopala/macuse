package server

import (
	"context"
	"sort"
	"time"

	"github.com/radutopala/macuse/internal/approval"
	"github.com/radutopala/macuse/internal/proto"
)

// activeFor is how long after its last call a session counts as active.
const activeFor = 5 * time.Second

// RuleUser marks an audited stop or resume, which the user did from the
// popup.
const RuleUser = "user"

// keepActivity is how long a session's last call stays in the popup.
const keepActivity = 30 * time.Minute

// track registers an in-flight call so pause and stop can cancel it. It
// refuses calls while paused and from a stopped session.
func (s *Server) track(ctx context.Context, c approval.Caller) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paused {
		return nil, nil, fail(proto.CodePaused, "the user paused macuse; try again once they resume it")
	}
	if s.stopped[c.Session] {
		return nil, nil, fail(proto.CodeStopped, "the user stopped this session from controlling the Mac")
	}
	ctx, cancel := context.WithCancel(ctx)
	s.nextCall++
	id := s.nextCall
	s.inflight[id] = inflight{session: c.Session, cancel: cancel}
	return ctx, func() {
		s.mu.Lock()
		delete(s.inflight, id)
		s.mu.Unlock()
		cancel()
	}, nil
}

// interrupted says why an in-flight call of the session was cancelled.
func (s *Server) interrupted(session string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped[session] {
		return fail(proto.CodeStopped, "the user stopped this session from controlling the Mac")
	}
	return fail(proto.CodePaused, "the user paused macuse; try again once they resume it")
}

// SetPaused pauses or resumes every agent; pausing cancels the calls in
// flight.
func (s *Server) SetPaused(paused bool) {
	s.mu.Lock()
	s.paused = paused
	if paused {
		for _, c := range s.inflight {
			c.cancel()
		}
	}
	s.mu.Unlock()
	s.notify()
}

// Stop refuses the session from now on and cancels its calls in flight.
func (s *Server) Stop(session string) { s.setStopped(session, true) }

// Resume lets a stopped session control the Mac again.
func (s *Server) Resume(session string) { s.setStopped(session, false) }

// setStopped stops or resumes the session, auditing it, since only the
// user does either.
func (s *Server) setStopped(session string, stopped bool) {
	s.mu.Lock()
	if stopped {
		s.stopped[session] = true
		for _, c := range s.inflight {
			if c.session == session {
				c.cancel()
			}
		}
	} else {
		delete(s.stopped, session)
	}
	a, ok := s.activity[session]
	if ok {
		a.Stopped = stopped
		s.activity[session] = a
	}
	s.mu.Unlock()
	e := Entry{Client: a.Client, Session: session, App: a.App, BundleID: a.BundleID,
		TeamID: a.TeamID, Action: "resume", Decision: "allow", Rule: RuleUser}
	if stopped {
		e.Action, e.Decision = "stop", "deny"
	}
	s.audit(e)
	s.notify()
}

func (s *Server) recordActivity(c approval.Caller, app proto.App, action string) {
	s.mu.Lock()
	s.activity[c.Session] = Activity{
		Client: c.Client, Session: c.Session, App: app.Name, BundleID: app.BundleID,
		TeamID: app.TeamID, Action: action, At: s.Now().UTC(),
	}
	s.mu.Unlock()
	s.notify()
}

// Activities lists the sessions' latest calls, newest first, dropping
// those older than keepActivity.
func (s *Server) Activities() []Activity {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	out := []Activity{}
	for k, a := range s.activity {
		if now.Sub(a.At) > keepActivity {
			delete(s.activity, k)
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// Summary is what the menu bar icon shows.
type Summary struct {
	Pending int
	Active  bool
	Paused  bool
}

// Summary reports the requests waiting on the user and whether an agent is
// driving an app right now.
func (s *Server) Summary() Summary {
	pending := len(s.Broker.Pending())
	s.mu.Lock()
	defer s.mu.Unlock()
	active := len(s.inflight) > 0
	now := s.Now()
	for _, a := range s.activity {
		if now.Sub(a.At) < activeFor {
			active = true
		}
	}
	return Summary{Pending: pending, Active: active, Paused: s.paused}
}
