package approval

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/radutopala/macuse/internal/proto"
)

type GateSuite struct {
	suite.Suite
	store  *Store
	broker *Broker
	gate   *Gate
}

func TestGateSuite(t *testing.T) { suite.Run(t, new(GateSuite)) }

func (s *GateSuite) SetupTest() {
	var err error
	s.store, err = OpenStore(filepath.Join(s.T().TempDir(), "approvals.json"), fixedNow)
	require.NoError(s.T(), err)
	s.broker = NewBroker(time.Minute, fixedNow, nil)
	s.gate = NewGate(s.store, s.broker)
}

var caller = Caller{Client: "claude", Session: "s1"}

type check struct {
	ok   bool
	rule string
	err  error
}

// checkAnswering runs Check, answering the prompt it raises with a.
func (s *GateSuite) checkAnswering(ctx context.Context, c Caller, app proto.App, running bool, a Answer) check {
	ch := make(chan check, 1)
	go func() {
		ok, rule, err := s.gate.Check(ctx, c, app, running, "click")
		ch <- check{ok, rule, err}
	}()
	require.Eventually(s.T(), func() bool { return len(s.broker.Pending()) == 1 }, time.Second, time.Millisecond)
	_, err := s.broker.Answer(s.broker.Pending()[0].ID, a)
	require.NoError(s.T(), err)
	return <-ch
}

func (s *GateSuite) TestSavedDecisions() {
	require.NoError(s.T(), s.store.Save(Saved{BundleID: notes.BundleID, TeamID: notes.TeamID, Decision: Allow}))
	require.NoError(s.T(), s.store.Save(Saved{BundleID: "com.deny", Decision: Deny}))

	tests := []struct {
		name    string
		app     proto.App
		running bool
		ok      bool
	}{
		{"allowed app", notes, true, true},
		{"allowed bundle before launch", proto.App{BundleID: notes.BundleID}, false, true},
		{"denied app", proto.App{BundleID: "com.deny"}, true, false},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			ok, rule, err := s.gate.Check(context.Background(), caller, tt.app, tt.running, "click")
			require.NoError(s.T(), err)
			require.Equal(s.T(), tt.ok, ok)
			require.Equal(s.T(), RuleSaved, rule)
		})
	}
}

func (s *GateSuite) TestSessionAnswerAllowsOnlyThatSession() {
	got := s.checkAnswering(context.Background(), caller, notes, true, AnswerSession)
	require.Equal(s.T(), check{true, RulePrompt, nil}, got)

	ok, rule, err := s.gate.Check(context.Background(), caller, notes, true, "type")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)
	require.Equal(s.T(), RuleSession, rule)

	// A different team under the same bundle id asks again.
	got = s.checkAnswering(context.Background(), caller, proto.App{BundleID: notes.BundleID, TeamID: "X"}, true, AnswerDeny)
	require.Equal(s.T(), check{false, RulePrompt, nil}, got)
	// So does another session.
	got = s.checkAnswering(context.Background(), Caller{Session: "s2"}, notes, true, AnswerDeny)
	require.Equal(s.T(), check{false, RulePrompt, nil}, got)
	require.Empty(s.T(), s.store.List())
}

func (s *GateSuite) TestAlwaysSavesRunningApp() {
	got := s.checkAnswering(context.Background(), caller, notes, true, AnswerAlways)
	require.Equal(s.T(), check{true, RulePrompt, nil}, got)
	saved, ok := s.store.Lookup(notes.BundleID, notes.TeamID)
	require.True(s.T(), ok)
	require.Equal(s.T(), Allow, saved.Decision)
	require.Equal(s.T(), "Notes", saved.Name)
}

func (s *GateSuite) TestAlwaysBeforeLaunchWaitsForLaunched() {
	pre := proto.App{BundleID: notes.BundleID, Name: "Notes"}
	got := s.checkAnswering(context.Background(), caller, pre, false, AnswerAlways)
	require.Equal(s.T(), check{true, RulePrompt, nil}, got)
	require.Empty(s.T(), s.store.List())

	// The session's allow covers the not-yet-running app.
	ok, rule, err := s.gate.Check(context.Background(), caller, pre, false, "start_app")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)
	require.Equal(s.T(), RuleSession, rule)

	require.NoError(s.T(), s.gate.Launched(caller, notes))
	_, saved := s.store.Lookup(notes.BundleID, notes.TeamID)
	require.True(s.T(), saved)
	// The pending "always" is used up.
	require.NoError(s.T(), s.store.Delete(notes.BundleID, notes.TeamID))
	require.NoError(s.T(), s.gate.Launched(caller, notes))
	require.Empty(s.T(), s.store.List())

	// A session-only launch allows the running app for that session alone.
	s2 := Caller{Session: "s2"}
	require.NoError(s.T(), s.gate.Launched(s2, notes))
	require.Empty(s.T(), s.store.List())
	ok, rule, err = s.gate.Check(context.Background(), s2, notes, true, "click")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)
	require.Equal(s.T(), RuleSession, rule)
}

func (s *GateSuite) TestTimeoutAndCancel() {
	s.broker.timeout = 5 * time.Millisecond
	ok, rule, err := s.gate.Check(context.Background(), caller, notes, true, "click")
	require.NoError(s.T(), err)
	require.False(s.T(), ok)
	require.Equal(s.T(), RuleTimeout, rule)

	s.broker.timeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = s.gate.Check(ctx, caller, notes, true, "click")
	require.ErrorIs(s.T(), err, context.Canceled)
}

func (s *GateSuite) TestSaveErrors() {
	dir := s.T().TempDir()
	require.NoError(s.T(), os.MkdirAll(filepath.Join(dir, "approvals.json", "x"), 0o700))
	s.store.path = filepath.Join(dir, "approvals.json")

	got := s.checkAnswering(context.Background(), caller, notes, true, AnswerAlways)
	require.False(s.T(), got.ok)
	require.Error(s.T(), got.err)
	got = s.checkAnswering(context.Background(), caller, proto.App{BundleID: notes.BundleID}, false, AnswerAlways)
	require.True(s.T(), got.ok)
	require.Error(s.T(), s.gate.Launched(caller, notes))
}
