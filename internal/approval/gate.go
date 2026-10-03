package approval

import (
	"context"
	"sync"

	"github.com/radutopala/macuse/internal/proto"
)

// Rules name where a decision came from, for the audit log.
const (
	RuleSaved   = "saved"
	RuleSession = "session"
	RulePrompt  = "prompt"
	RuleTimeout = "timeout"
)

// Caller is who asks: the agent's client and its session.
type Caller struct {
	Client  string
	Session string
}

// Gate answers whether a caller may control an app: a saved decision, then
// an allow the session already has, then the user.
type Gate struct {
	store  *Store
	broker *Broker

	mu       sync.Mutex
	sessions map[sessionKey]struct{}
	// launches holds the "always" answers given before the app ran, keyed
	// without a team, until Launched saves them under the real identity.
	launches map[sessionKey]string
}

type sessionKey struct {
	session, bundleID, teamID string
}

// NewGate returns a Gate over the saved decisions and the broker.
func NewGate(store *Store, broker *Broker) *Gate {
	return &Gate{store: store, broker: broker, sessions: map[sessionKey]struct{}{}, launches: map[sessionKey]string{}}
}

// Check decides on app for the caller, asking the user when nothing
// answers it yet. running is false for an app about to be launched, whose
// team isn't known: any decision on its bundle id answers, and an answer
// the user gives is recorded under its real identity once it runs (see
// Launched). A timeout is a refusal with RuleTimeout; only a cancelled ctx
// is an error.
func (g *Gate) Check(ctx context.Context, c Caller, app proto.App, running bool, action string) (bool, string, error) {
	saved, ok := g.store.Lookup(app.BundleID, app.TeamID)
	if !running {
		saved, ok = g.store.LookupBundle(app.BundleID)
	}
	if ok {
		return saved.Decision == Allow, RuleSaved, nil
	}
	if g.sessionAllows(c.Session, app, running) {
		return true, RuleSession, nil
	}
	answer, err := g.broker.Ask(ctx, Request{Client: c.Client, Session: c.Session, App: app, Action: action})
	if err == ErrTimeout {
		return false, RuleTimeout, nil
	}
	if err != nil {
		return false, "", err
	}
	switch answer {
	case AnswerAlways:
		if running {
			if err := g.store.Save(Saved{BundleID: app.BundleID, TeamID: app.TeamID, Name: app.Name, Decision: Allow}); err != nil {
				return false, "", err
			}
		} else {
			g.allowSession(c.Session, app)
			g.mu.Lock()
			g.launches[sessionKey{c.Session, app.BundleID, ""}] = app.Name
			g.mu.Unlock()
		}
		return true, RulePrompt, nil
	case AnswerSession:
		g.allowSession(c.Session, app)
		return true, RulePrompt, nil
	default:
		return false, RulePrompt, nil
	}
}

// Launched records the user's answer to a launch under the identity the
// app runs with: saved when they chose always, for the session otherwise.
// Call it only after a launch the user answered (RulePrompt or
// RuleSession), never after one a saved decision allowed, so a decision on
// one team never extends to another.
func (g *Gate) Launched(c Caller, app proto.App) error {
	g.allowSession(c.Session, app)
	key := sessionKey{c.Session, app.BundleID, ""}
	g.mu.Lock()
	_, always := g.launches[key]
	delete(g.launches, key)
	g.mu.Unlock()
	if !always {
		return nil
	}
	return g.store.Save(Saved{BundleID: app.BundleID, TeamID: app.TeamID, Name: app.Name, Decision: Allow})
}

func (g *Gate) allowSession(session string, app proto.App) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sessions[sessionKey{session, app.BundleID, app.TeamID}] = struct{}{}
}

// sessionAllows looks the app up among the session's allows; an app not
// yet running matches on its bundle id alone.
func (g *Gate) sessionAllows(session string, app proto.App, running bool) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for k := range g.sessions {
		if k.session == session && k.bundleID == app.BundleID && (!running || k.teamID == app.TeamID) {
			return true
		}
	}
	return false
}
