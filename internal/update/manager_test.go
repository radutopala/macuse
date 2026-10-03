package update

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type ManagerSuite struct {
	suite.Suite
	checkErr   error
	installErr error
	checks     atomic.Int32
	installed  []Release
	restarts   int
}

func TestManager(t *testing.T) { suite.Run(t, new(ManagerSuite)) }

func (s *ManagerSuite) SetupTest() {
	s.checkErr, s.installErr = nil, nil
	s.checks.Store(0)
	s.installed, s.restarts = nil, 0
}

var rel2 = Release{Version: "2026.10.2", URL: "u", SHA256: "h"}

func (s *ManagerSuite) manager(current string) *Manager {
	return &Manager{
		Current: current,
		Check: func(context.Context) (Release, error) {
			s.checks.Add(1)
			return rel2, s.checkErr
		},
		Install: func(_ context.Context, r Release) error {
			s.installed = append(s.installed, r)
			return s.installErr
		},
		Restart:  func() { s.restarts++ },
		Interval: time.Millisecond,
	}
}

func (s *ManagerSuite) TestCheckNow() {
	m := s.manager("v2026.10.1")
	require.Equal(s.T(), Status{Current: "v2026.10.1"}, m.Status())
	require.Equal(s.T(), Status{Current: "v2026.10.1", Latest: "2026.10.2", Available: true}, m.CheckNow(context.Background()))

	s.checkErr = errors.New("offline")
	require.Equal(s.T(), Status{
		Current: "v2026.10.1", Latest: "2026.10.2", Available: true,
		Error: "checking for updates failed: offline",
	}, m.CheckNow(context.Background()), "a failed check keeps what the last one found")

	up := s.manager("v2026.10.2")
	require.False(s.T(), up.CheckNow(context.Background()).Available)
}

func (s *ManagerSuite) TestRun() {
	m := s.manager("v2026.10.1")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	require.Eventually(s.T(), func() bool { return s.checks.Load() >= 3 }, time.Second, time.Millisecond)
	cancel()
	<-done
}

func (s *ManagerSuite) TestInstallNow() {
	m := s.manager("v2026.10.1")
	require.ErrorIs(s.T(), m.InstallNow(context.Background()), errNoUpdate)

	m.CheckNow(context.Background())
	s.installErr = errors.New("disk full")
	require.EqualError(s.T(), m.InstallNow(context.Background()), "disk full")
	require.Equal(s.T(), "installing the update failed: disk full", m.Status().Error)
	require.False(s.T(), m.Status().Installing)
	require.Zero(s.T(), s.restarts)

	s.installErr = nil
	require.NoError(s.T(), m.InstallNow(context.Background()))
	require.Equal(s.T(), []Release{rel2, rel2}, s.installed)
	require.Equal(s.T(), 1, s.restarts)
	require.True(s.T(), m.Status().Installing, "stays installing until the restart")
	require.Empty(s.T(), m.Status().Error)
	require.ErrorIs(s.T(), m.InstallNow(context.Background()), errInstalling)
}
