package approval

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/radutopala/macuse/internal/proto"
)

type BrokerSuite struct {
	suite.Suite
	changes atomic.Int32
	broker  *Broker
}

func TestBrokerSuite(t *testing.T) { suite.Run(t, new(BrokerSuite)) }

func (s *BrokerSuite) SetupTest() {
	s.changes.Store(0)
	s.broker = NewBroker(time.Minute, fixedNow, func() { s.changes.Add(1) })
}

var notes = proto.App{BundleID: "com.apple.Notes", Name: "Notes", TeamID: "T"}

type result struct {
	answer Answer
	err    error
}

func (s *BrokerSuite) ask(ctx context.Context, req Request) <-chan result {
	ch := make(chan result, 1)
	go func() {
		a, err := s.broker.Ask(ctx, req)
		ch <- result{a, err}
	}()
	return ch
}

func (s *BrokerSuite) waitPending(n int) []Request {
	var p []Request
	require.Eventually(s.T(), func() bool {
		p = s.broker.Pending()
		return len(p) == n
	}, time.Second, time.Millisecond)
	return p
}

func (s *BrokerSuite) TestAnswerReleasesJoinedWaiters() {
	a := s.ask(context.Background(), Request{Client: "claude", Session: "s1", App: notes, Action: "click"})
	p := s.waitPending(1)
	b := s.ask(context.Background(), Request{Client: "claude", Session: "s1", App: notes, Action: "type"})
	// Another session gets its own request.
	c := s.ask(context.Background(), Request{Client: "claude", Session: "s2", App: notes})
	s.waitPending(2)
	require.Eventually(s.T(), func() bool {
		s.broker.mu.Lock()
		defer s.broker.mu.Unlock()
		return s.broker.pending[p[0].ID].waiters == 2
	}, time.Second, time.Millisecond)

	require.Equal(s.T(), "1", p[0].ID)
	require.Equal(s.T(), t0, p[0].CreatedAt)
	require.Equal(s.T(), "click", p[0].Action)

	req, err := s.broker.Answer("1", AnswerSession)
	require.NoError(s.T(), err)
	require.Equal(s.T(), "s1", req.Session)
	require.Equal(s.T(), result{AnswerSession, nil}, <-a)
	require.Equal(s.T(), result{AnswerSession, nil}, <-b)

	_, err = s.broker.Answer("1", AnswerDeny)
	require.ErrorIs(s.T(), err, ErrUnknown)

	_, err = s.broker.Answer("2", AnswerDeny)
	require.NoError(s.T(), err)
	require.Equal(s.T(), result{AnswerDeny, nil}, <-c)
	require.Empty(s.T(), s.broker.Pending())
	require.Equal(s.T(), int32(4), s.changes.Load())
}

func (s *BrokerSuite) TestTimeout() {
	s.broker = NewBroker(10*time.Millisecond, fixedNow, nil)
	a, err := s.broker.Ask(context.Background(), Request{Session: "s", App: notes})
	require.ErrorIs(s.T(), err, ErrTimeout)
	require.Equal(s.T(), AnswerDeny, a)
	require.Empty(s.T(), s.broker.Pending())
}

func (s *BrokerSuite) TestCancelKeepsRequestWhileOthersWait() {
	ctx, cancel := context.WithCancel(context.Background())
	a := s.ask(ctx, Request{Session: "s", App: notes})
	s.waitPending(1)
	b := s.ask(context.Background(), Request{Session: "s", App: notes})
	require.Eventually(s.T(), func() bool {
		s.broker.mu.Lock()
		defer s.broker.mu.Unlock()
		return s.broker.pending["1"].waiters == 2
	}, time.Second, time.Millisecond)

	cancel()
	require.ErrorIs(s.T(), (<-a).err, context.Canceled)
	require.Len(s.T(), s.broker.Pending(), 1)

	_, err := s.broker.Answer("1", AnswerAlways)
	require.NoError(s.T(), err)
	require.Equal(s.T(), result{AnswerAlways, nil}, <-b)
}

func (s *BrokerSuite) TestRemoveUnknown() {
	require.False(s.T(), s.broker.remove("nope"))
}

func (s *BrokerSuite) TestAnswerValid() {
	for _, a := range []Answer{AnswerDeny, AnswerSession, AnswerAlways} {
		require.True(s.T(), a.Valid(), a)
	}
	require.False(s.T(), Answer("maybe").Valid())
}
