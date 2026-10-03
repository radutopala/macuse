package fsmigrate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/radutopala/macuse/internal/config"
)

type MigrateSuite struct {
	suite.Suite
	c       Ctx
	state   string
	support string
	logs    string
}

func TestMigrate(t *testing.T) { suite.Run(t, new(MigrateSuite)) }

func (s *MigrateSuite) SetupTest() {
	home := s.T().TempDir()
	s.c = Ctx{Home: home, Paths: config.NewPaths(home)}
	s.state = filepath.Join(s.c.Paths.Dir, "fs_migrations")
	s.support = filepath.Join(home, "Library", "Application Support", "macuse")
	s.logs = filepath.Join(home, "Library", "Logs", "macuse")
}

func (s *MigrateSuite) write(path, data string) {
	s.Require().NoError(os.MkdirAll(filepath.Dir(path), 0o700))
	s.Require().NoError(os.WriteFile(path, []byte(data), 0o600))
}

func (s *MigrateSuite) read(path string) string {
	data, err := os.ReadFile(path)
	s.Require().NoError(err)
	return string(data)
}

func (s *MigrateSuite) TestRunAppliesEachOnce() {
	var applied []int
	step := func(v int) Migration {
		return Migration{Description: "step", Apply: func(Ctx) error { applied = append(applied, v); return nil }}
	}
	ms := []Migration{{Description: "bootstrap"}, step(1), step(2)}
	require.NoError(s.T(), run(s.c, ms))
	require.Equal(s.T(), []int{1, 2}, applied)
	require.Equal(s.T(), "2\n", s.read(s.state))

	require.NoError(s.T(), run(s.c, ms))
	require.Equal(s.T(), []int{1, 2}, applied, "already applied")

	require.NoError(s.T(), run(s.c, append(ms, step(3))))
	require.Equal(s.T(), []int{1, 2, 3}, applied, "only the new one")
	require.Equal(s.T(), "3\n", s.read(s.state))
}

func (s *MigrateSuite) TestRunFails() {
	boom := []Migration{{}, {Description: "boom", Apply: func(Ctx) error { return errors.New("bad") }}}
	ok := []Migration{{}, {Description: "ok", Apply: func(Ctx) error { return nil }}}
	tests := []struct {
		name  string
		setup func()
		ms    []Migration
		err   string
	}{
		{"garbled state", func() { s.write(s.state, "x") }, ok, "fs_migrations: strconv.Atoi"},
		{"unreadable state", func() { s.Require().NoError(os.MkdirAll(s.state, 0o700)) }, ok, "is a directory"},
		{"migration", func() {}, boom, "applying fs migration 1 (boom): bad"},
		{"state dir", func() {
			// A dangling link: nothing to read, and no directory to make.
			s.Require().NoError(os.Symlink(filepath.Join(s.c.Home, "missing"), s.c.Paths.Dir))
		}, ok, "recording fs migration 1"},
		{"temp file", func() { s.Require().NoError(os.MkdirAll(s.state+".tmp", 0o700)) }, ok, "recording fs migration 1"},
		{"swap", func() {}, []Migration{{}, {Description: "dir", Apply: func(c Ctx) error {
			return os.MkdirAll(filepath.Join(c.Paths.Dir, "fs_migrations", "x"), 0o700)
		}}}, "recording fs migration 1"},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.SetupTest()
			tt.setup()
			err := run(s.c, tt.ms)
			require.Error(s.T(), err)
			require.Contains(s.T(), err.Error(), tt.err)
		})
	}
}

func (s *MigrateSuite) TestMoveFromLibrary() {
	s.write(filepath.Join(s.support, "config.json"), "cfg")
	s.write(filepath.Join(s.support, "token"), "tok")
	s.write(filepath.Join(s.support, "approvals.json"), "old approvals")
	s.write(s.c.Paths.Approvals, "new approvals")
	s.write(filepath.Join(s.logs, "audit.jsonl"), "audit")
	s.write(filepath.Join(s.logs, "macuse.log"), "log")
	oldLog := filepath.Join(s.logs, "macuse.log")
	s.write(s.c.Paths.LaunchAgent, "<string>"+oldLog+"</string><string>"+oldLog+"</string>")

	require.NoError(s.T(), Run(s.c))
	require.Equal(s.T(), "cfg", s.read(s.c.Paths.Config))
	require.Equal(s.T(), "tok", s.read(s.c.Paths.Token))
	require.Equal(s.T(), "new approvals", s.read(s.c.Paths.Approvals), "~/.macuse wins")
	require.Equal(s.T(), "audit", s.read(s.c.Paths.Audit))
	require.Equal(s.T(), "log", s.read(s.c.Paths.Log))
	require.Equal(s.T(), "<string>"+s.c.Paths.Log+"</string><string>"+s.c.Paths.Log+"</string>", s.read(s.c.Paths.LaunchAgent))
	require.NoDirExists(s.T(), s.logs)
	require.FileExists(s.T(), filepath.Join(s.support, "approvals.json"), "left, as ~/.macuse had one")
	require.Equal(s.T(), "1\n", s.read(s.state))
}

func (s *MigrateSuite) TestMoveFromLibraryNothingToMove() {
	require.NoError(s.T(), moveFromLibrary(s.c))
	require.NoFileExists(s.T(), s.c.Paths.LaunchAgent)
	require.NoDirExists(s.T(), s.c.Paths.Dir)

	// A plist with another log is left alone.
	s.write(s.c.Paths.LaunchAgent, "<string>/elsewhere.log</string>")
	require.NoError(s.T(), moveFromLibrary(s.c))
	require.Equal(s.T(), "<string>/elsewhere.log</string>", s.read(s.c.Paths.LaunchAgent))
}

func (s *MigrateSuite) TestMoveFromLibraryFails() {
	tests := []struct {
		name  string
		setup func()
		err   string
	}{
		{"new dir", func() {
			s.write(filepath.Join(s.support, "token"), "tok")
			s.write(s.c.Paths.Dir, "")
		}, "not a directory"},
		{"rename", func() {
			// ~/.macuse is inside the old token, which can't move into itself.
			old := filepath.Join(s.support, "token")
			s.Require().NoError(os.MkdirAll(old, 0o700))
			s.Require().NoError(os.Symlink(old, s.c.Paths.Dir))
		}, "Application Support/macuse/token: rename"},
		{"plist", func() { s.Require().NoError(os.MkdirAll(s.c.Paths.LaunchAgent, 0o700)) }, "is a directory"},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.SetupTest()
			tt.setup()
			err := moveFromLibrary(s.c)
			require.Error(s.T(), err)
			require.Contains(s.T(), err.Error(), tt.err)
		})
	}
}
