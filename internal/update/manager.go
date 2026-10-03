package update

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Status is what the popup shows about updates.
type Status struct {
	Current    string `json:"current"`
	Latest     string `json:"latest,omitempty"`
	Available  bool   `json:"available"`
	Installing bool   `json:"installing,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Manager checks for releases on a beat and installs one when asked.
type Manager struct {
	Current string
	Check   func(ctx context.Context) (Release, error)
	Install func(ctx context.Context, rel Release) error
	// Restart runs once an update is in place, to start the new version.
	Restart  func()
	Interval time.Duration

	mu     sync.Mutex
	status Status
	latest Release
}

// Status is the latest check's outcome.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.status
	s.Current = m.Current
	return s
}

// CheckNow looks for a newer release.
func (m *Manager) CheckNow(ctx context.Context) Status {
	rel, err := m.Check(ctx)
	m.mu.Lock()
	if err != nil {
		m.status.Error = "checking for updates failed: " + err.Error()
	} else {
		m.latest = rel
		m.status.Latest = rel.Version
		m.status.Available = Newer(m.Current, rel.Version)
		m.status.Error = ""
	}
	m.mu.Unlock()
	return m.Status()
}

// Run checks now and then every Interval until ctx is done.
func (m *Manager) Run(ctx context.Context) {
	m.CheckNow(ctx)
	t := time.NewTicker(m.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.CheckNow(ctx)
		}
	}
}

var (
	errNoUpdate   = errors.New("there is no update to install")
	errInstalling = errors.New("an update is already being installed")
)

// InstallNow installs the newer release found by the last check, then
// restarts into it.
func (m *Manager) InstallNow(ctx context.Context) error {
	m.mu.Lock()
	switch {
	case m.status.Installing:
		m.mu.Unlock()
		return errInstalling
	case !m.status.Available:
		m.mu.Unlock()
		return errNoUpdate
	}
	m.status.Installing = true
	m.status.Error = ""
	rel := m.latest
	m.mu.Unlock()

	if err := m.Install(ctx, rel); err != nil {
		m.mu.Lock()
		m.status.Installing = false
		m.status.Error = "installing the update failed: " + err.Error()
		m.mu.Unlock()
		return err
	}
	m.Restart()
	return nil
}
