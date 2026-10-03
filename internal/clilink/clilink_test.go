package clilink

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type LinkSuite struct {
	suite.Suite
	dir    string
	target string
	runs   [][]string
	runErr error
}

func TestLink(t *testing.T) { suite.Run(t, new(LinkSuite)) }

func (s *LinkSuite) SetupTest() {
	s.dir = s.T().TempDir()
	s.target = filepath.Join(s.dir, "macuse.app", "Contents", "MacOS", "macuse")
	s.Require().NoError(os.MkdirAll(filepath.Dir(s.target), 0o755))
	s.Require().NoError(os.WriteFile(s.target, nil, 0o755))
	s.runs, s.runErr = nil, nil
}

// mkdirs makes directories under s.dir and returns their paths.
func (s *LinkSuite) mkdirs(names ...string) []string {
	var dirs []string
	for _, n := range names {
		d := filepath.Join(s.dir, n)
		s.Require().NoError(os.MkdirAll(d, 0o755))
		dirs = append(dirs, d)
	}
	return dirs
}

func (s *LinkSuite) link(dirs, writable []string) Link {
	return Link{
		Target:   s.target,
		Dirs:     func() []string { return dirs },
		Writable: writable,
		Fallback: filepath.Join(s.dir, "fallback", "macuse"),
		Run: func(name string, args ...string) error {
			s.runs = append(s.runs, append([]string{name}, args...))
			return s.runErr
		},
	}
}

func (s *LinkSuite) TestShellPath() {
	var got []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		got = append([]string{name}, args...)
		return []byte("motd\n" + marker + "/a:/b" + marker), nil
	}
	require.Equal(s.T(), []string{"/a", "/b"}, ShellPath(context.Background(), run, "/bin/zsh"))
	require.Equal(s.T(), []string{"/bin/zsh", "-ilc", `printf '` + marker + `%s` + marker + `' "$PATH"`}, got)

	for name, run := range map[string]func(context.Context, string, ...string) ([]byte, error){
		"fails":     func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("exit 1") },
		"no marker": func(context.Context, string, ...string) ([]byte, error) { return []byte("/a:/b"), nil },
		"one marker": func(context.Context, string, ...string) ([]byte, error) {
			return []byte(marker + "/a"), nil
		},
	} {
		require.Nil(s.T(), ShellPath(context.Background(), run, "/bin/zsh"), name)
	}
}

func (s *LinkSuite) TestWritable() {
	require.Equal(s.T(), []string{"/opt/homebrew/bin", "/usr/local/bin", "/h/.local/bin", "/h/bin"}, Writable("/h"))
}

func (s *LinkSuite) TestInstallInFirstWritableDirOnPath() {
	dirs := s.mkdirs("tool/bin", "local/bin", "bin")
	// tool/bin isn't one Install may write; "missing" isn't there; a
	// relative entry is skipped; a trailing slash still matches.
	path := []string{"rel", dirs[0], filepath.Join(s.dir, "missing"), dirs[1] + "/", dirs[2]}
	l := s.link(path, []string{filepath.Join(s.dir, "missing"), dirs[1], dirs[2]})
	require.False(s.T(), l.Installed())
	require.NoError(s.T(), l.Install())
	require.True(s.T(), l.Installed())
	got, err := os.Readlink(filepath.Join(dirs[1], "macuse"))
	require.NoError(s.T(), err)
	require.Equal(s.T(), s.target, got)
	require.NoFileExists(s.T(), filepath.Join(dirs[0], "macuse"))
	require.Empty(s.T(), s.runs)

	// A stale link is replaced.
	link := filepath.Join(dirs[1], "macuse")
	require.NoError(s.T(), os.Remove(link))
	require.NoError(s.T(), os.Symlink("/elsewhere", link))
	require.False(s.T(), l.Installed())
	require.NoError(s.T(), l.Install())
	require.True(s.T(), l.Installed())
}

func (s *LinkSuite) TestInstalledAnywhereOnPath() {
	dirs := s.mkdirs("go/bin")
	require.NoError(s.T(), os.Symlink(s.target, filepath.Join(dirs[0], "macuse")))
	l := s.link(dirs, nil)
	require.True(s.T(), l.Installed(), "a link in a directory Install wouldn't use still counts")

	l.Target = filepath.Join(s.dir, "missing")
	require.False(s.T(), l.Installed())
}

func (s *LinkSuite) TestInstallNeverReplacesAFile() {
	dirs := s.mkdirs("bin")
	file := filepath.Join(dirs[0], "macuse")
	s.Require().NoError(os.WriteFile(file, []byte("other"), 0o755))
	require.NoError(s.T(), s.link(dirs, dirs).Install(), "falls back to the admin prompt")
	require.Len(s.T(), s.runs, 1)
	data, err := os.ReadFile(file)
	require.NoError(s.T(), err)
	require.Equal(s.T(), "other", string(data))
}

func (s *LinkSuite) TestInstallBehindAFile() {
	// "bin" is a file, so bin/macuse can be neither checked nor removed.
	file := filepath.Join(s.dir, "bin")
	s.Require().NoError(os.WriteFile(file, nil, 0o600))
	require.NoError(s.T(), s.link([]string{file}, []string{file}).Install())
	require.Len(s.T(), s.runs, 1)
}

func (s *LinkSuite) TestInstallAsksForAdmin() {
	l := s.link(nil, nil)
	l.Fallback = filepath.Join(s.dir, "it's here", "macuse")
	l.Target = `/Apps/a "b"\c/macuse`
	require.NoError(s.T(), l.Install())
	require.Equal(s.T(), [][]string{{"osascript", "-e",
		`do shell script "mkdir -p '` + strings.ReplaceAll(filepath.Dir(l.Fallback), "'", `'\\''`) +
			`' && ln -sf '/Apps/a \"b\"\\c/macuse' '` + strings.ReplaceAll(l.Fallback, "'", `'\\''`) + `'" with administrator privileges`,
	}}, s.runs)

	s.runErr = errors.New("User canceled.")
	require.EqualError(s.T(), l.Install(), "linking "+l.Fallback+": User canceled.")
}
