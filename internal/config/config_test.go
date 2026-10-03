package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type ConfigSuite struct {
	suite.Suite
	dir string
}

func TestConfigSuite(t *testing.T) {
	suite.Run(t, new(ConfigSuite))
}

func (s *ConfigSuite) SetupTest() {
	s.dir = s.T().TempDir()
}

func (s *ConfigSuite) TestNewPaths() {
	p := NewPaths("/Users/u")
	require.Equal(s.T(), Paths{
		Dir:         "/Users/u/.macuse",
		Config:      "/Users/u/.macuse/config.json",
		Token:       "/Users/u/.macuse/token",
		Approvals:   "/Users/u/.macuse/approvals.json",
		Audit:       "/Users/u/.macuse/logs/audit.jsonl",
		Log:         "/Users/u/.macuse/logs/macuse.log",
		LaunchAgent: "/Users/u/Library/LaunchAgents/io.github.radutopala.macuse.plist",
	}, p)
}

func (s *ConfigSuite) TestLoad() {
	tests := []struct {
		name    string
		content *string
		want    Config
		wantErr string
	}{
		{name: "missing file", want: Config{Listen: DefaultListen}},
		{name: "empty object", content: ptr(`{}`), want: Config{Listen: DefaultListen}},
		{
			name:    "all fields",
			content: ptr(`{"listen":"0.0.0.0:9000","deny_apps":["com.example.App"],"approval_timeout_sec":30}`),
			want:    Config{Listen: "0.0.0.0:9000", DenyApps: []string{"com.example.App"}, ApprovalTimeoutSec: 30},
		},
		{name: "bad json", content: ptr(`{`), wantErr: "unexpected end of JSON input"},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			path := filepath.Join(s.T().TempDir(), "config.json")
			if tc.content != nil {
				require.NoError(s.T(), os.WriteFile(path, []byte(*tc.content), 0o600))
			}
			got, err := Load(path)
			if tc.wantErr != "" {
				require.ErrorContains(s.T(), err, tc.wantErr)
				return
			}
			require.NoError(s.T(), err)
			require.Equal(s.T(), tc.want, got)
		})
	}
}

func (s *ConfigSuite) TestLoadUnreadable() {
	// A directory can't be read as a file.
	_, err := Load(s.dir)
	require.Error(s.T(), err)
}

func (s *ConfigSuite) TestApprovalTimeout() {
	require.Equal(s.T(), DefaultApprovalTimeout, Config{}.ApprovalTimeout())
	require.Equal(s.T(), DefaultApprovalTimeout, Config{ApprovalTimeoutSec: -1}.ApprovalTimeout())
	require.Equal(s.T(), 30*time.Second, Config{ApprovalTimeoutSec: 30}.ApprovalTimeout())
}

func ptr(s string) *string { return &s }
