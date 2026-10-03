package launchagent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type AgentSuite struct {
	suite.Suite
	dir   string
	calls [][]string
	fail  map[string]error
	agent Agent
}

func TestAgentSuite(t *testing.T) {
	suite.Run(t, new(AgentSuite))
}

func (s *AgentSuite) SetupTest() {
	s.dir = s.T().TempDir()
	s.calls = nil
	s.fail = map[string]error{}
	s.agent = Agent{
		Path:  filepath.Join(s.dir, "LaunchAgents", "io.example.app.plist"),
		Label: "io.example.app",
		Exe:   "/Applications/A & B.app/Contents/MacOS/macuse",
		Log:   filepath.Join(s.dir, "Logs", "macuse", "macuse.log"),
		UID:   501,
		Run: func(name string, args ...string) error {
			s.calls = append(s.calls, append([]string{name}, args...))
			return s.fail[args[0]]
		},
	}
}

func (s *AgentSuite) TestPlist() {
	p := string(s.agent.Plist())
	require.Contains(s.T(), p, "<string>io.example.app</string>")
	require.Contains(s.T(), p, "<string>/Applications/A &amp; B.app/Contents/MacOS/macuse</string>\n\t\t<string>serve</string>")
	require.Contains(s.T(), p, "<key>SuccessfulExit</key>\n\t\t<false/>")
	require.Contains(s.T(), p, "<string>Aqua</string>")
	require.Contains(s.T(), p, "<key>StandardErrorPath</key>\n\t<string>"+s.agent.Log+"</string>")
}

func (s *AgentSuite) TestWriteRemove() {
	require.False(s.T(), s.agent.Installed())
	require.NoError(s.T(), s.agent.Write())
	require.True(s.T(), s.agent.Installed())
	data, err := os.ReadFile(s.agent.Path)
	require.NoError(s.T(), err)
	require.Equal(s.T(), s.agent.Plist(), data)
	require.DirExists(s.T(), filepath.Dir(s.agent.Log))

	require.NoError(s.T(), s.agent.Remove())
	require.False(s.T(), s.agent.Installed())
	require.NoError(s.T(), s.agent.Remove(), "already gone")
	require.Empty(s.T(), s.calls)
}

// blocker puts a file where a directory must go.
func (s *AgentSuite) blocker(path string) {
	require.NoError(s.T(), os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(s.T(), os.WriteFile(path, nil, 0o600))
}

func (s *AgentSuite) TestWriteFailures() {
	tests := []struct {
		name  string
		setup func()
	}{
		{"agents dir", func() { s.blocker(filepath.Dir(s.agent.Path)) }},
		{"log dir", func() { s.blocker(filepath.Dir(s.agent.Log)) }},
		{"plist", func() { require.NoError(s.T(), os.MkdirAll(filepath.Join(s.agent.Path, "x"), 0o755)) }},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			tc.setup()
			require.Error(s.T(), s.agent.Write())
			require.Error(s.T(), s.agent.Install())
		})
	}
}

func (s *AgentSuite) TestRemoveFailure() {
	// A non-empty directory in the plist's place can't be removed.
	require.NoError(s.T(), os.MkdirAll(filepath.Join(s.agent.Path, "x"), 0o755))
	require.Error(s.T(), s.agent.Remove())
	require.Error(s.T(), s.agent.Uninstall())
}

func (s *AgentSuite) TestInstall() {
	s.fail["bootout"] = errors.New("not loaded")
	require.NoError(s.T(), s.agent.Install())
	require.True(s.T(), s.agent.Installed())
	require.Equal(s.T(), [][]string{
		{"launchctl", "bootout", "gui/501/io.example.app"},
		{"launchctl", "bootstrap", "gui/501", s.agent.Path},
	}, s.calls)
}

func (s *AgentSuite) TestInstallBootstrapFails() {
	s.fail["bootstrap"] = errors.New("boom")
	require.EqualError(s.T(), s.agent.Install(), "launchctl bootstrap: boom")
}

func (s *AgentSuite) TestUninstall() {
	require.NoError(s.T(), s.agent.Write())
	s.fail["bootout"] = errors.New("not loaded")
	require.NoError(s.T(), s.agent.Uninstall())
	require.False(s.T(), s.agent.Installed())
	require.Equal(s.T(), [][]string{{"launchctl", "bootout", "gui/501/io.example.app"}}, s.calls)
}
