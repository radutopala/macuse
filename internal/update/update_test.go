package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type UpdateSuite struct {
	suite.Suite
	dir  string
	srv  *httptest.Server
	zip  []byte
	sums string
}

func TestUpdate(t *testing.T) { suite.Run(t, new(UpdateSuite)) }

func (s *UpdateSuite) SetupTest() {
	s.dir = s.T().TempDir()
	s.zip = []byte("zip bytes")
	sum := sha256.Sum256(s.zip)
	s.sums = "deadbeef  macuse_2026.10.2_linux_arm64.tar.gz\nmalformed line here\n" +
		hex.EncodeToString(sum[:]) + "  macuse_2026.10.2_macos.zip\n"
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/checksums.txt":
			_, _ = w.Write([]byte(s.sums))
		case "/macuse_2026.10.2_macos.zip":
			_, _ = w.Write(s.zip)
		case "/short.zip":
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte("short"))
		default:
			http.NotFound(w, r)
		}
	}))
	s.T().Cleanup(s.srv.Close)
}

func (s *UpdateSuite) release() Release {
	rel, err := Latest(context.Background(), s.srv.Client(), s.srv.URL)
	s.Require().NoError(err)
	return rel
}

func (s *UpdateSuite) TestLatest() {
	sum := sha256.Sum256(s.zip)
	require.Equal(s.T(), Release{
		Version: "2026.10.2",
		URL:     s.srv.URL + "/macuse_2026.10.2_macos.zip",
		SHA256:  hex.EncodeToString(sum[:]),
	}, s.release())
}

func (s *UpdateSuite) TestLatestFailures() {
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	tests := []struct {
		name string
		sums string
		feed string
		want string
	}{
		{name: "no app zip", sums: "abc  macuse_1_linux_amd64.tar.gz\n", want: "the latest release has no macuse_<version>_macos.zip"},
		{name: "line too long", sums: strings.Repeat("x", 70*1024), want: "reading checksums.txt"},
		{name: "not found", feed: s.srv.URL + "/missing", want: "404 Not Found"},
		{name: "bad url", feed: "://bad", want: "missing protocol scheme"},
		{name: "unreachable", feed: closed.URL, want: "connection refused"},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.sums = tt.sums
			feed := tt.feed
			if feed == "" {
				feed = s.srv.URL
			}
			_, err := Latest(context.Background(), s.srv.Client(), feed)
			require.ErrorContains(s.T(), err, tt.want)
		})
	}
}

func (s *UpdateSuite) TestVersions() {
	tests := []struct {
		current, latest string
		newer           bool
	}{
		{"v2026.10.1", "2026.10.2", true},
		{"v2026.10.1", "2026.11.1", true},
		{"2026.10.1", "2026.10", false},
		{"2026.10", "2026.10.1", true},
		{"v2026.10.2", "2026.10.2", false},
		{"v2026.10.3", "2026.10.2", false},
		{"dev", "2026.10.2", false},
		{"v2026.10.1-3-gabc", "2026.10.2", false},
		{"v2026.10.1", "garbage", false},
		{"v2026.-1.1", "2026.10.2", false},
	}
	for _, tt := range tests {
		s.Run(tt.current+" vs "+tt.latest, func() {
			require.Equal(s.T(), tt.newer, Newer(tt.current, tt.latest))
		})
	}
	require.True(s.T(), Valid("v2026.10.1"))
	require.False(s.T(), Valid("dev"))
}

// fakeRun plays codesign and ditto. ditto unpacks an app unless noApp;
// verify, when set, answers codesign --verify.
type fakeRun struct {
	team    string
	readErr error
	dittoOK bool
	noApp   bool
	verify  func(app string) ([]byte, error)
	calls   []string
}

func (f *fakeRun) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	switch {
	case name == "codesign" && args[0] == "-dv":
		if f.readErr != nil {
			return []byte("not signed"), f.readErr
		}
		return []byte("Executable=x\nTeamIdentifier=" + f.team + "\n"), nil
	case name == "ditto":
		if !f.dittoOK {
			return []byte("ditto: bad zip"), errors.New("exit status 1")
		}
		dest := args[len(args)-1]
		if f.noApp {
			return nil, os.MkdirAll(dest, 0o755)
		}
		return nil, os.MkdirAll(filepath.Join(dest, appName, "Contents"), 0o755)
	case name == "codesign" && args[0] == "--verify":
		return f.verify(args[len(args)-1])
	}
	return nil, fmt.Errorf("unexpected %s", name)
}

func (s *UpdateSuite) installer(f *fakeRun) Installer {
	app := filepath.Join(s.dir, appName)
	s.Require().NoError(os.MkdirAll(filepath.Join(app, "Contents"), 0o755))
	s.Require().NoError(os.WriteFile(filepath.Join(app, "Contents", "old"), nil, 0o600))
	return Installer{App: app, HTTP: s.srv.Client(), Run: f.run}
}

func ok(string) ([]byte, error) { return nil, nil }

func (s *UpdateSuite) TestTeamID() {
	tests := []struct {
		name string
		f    *fakeRun
		team string
		want string
	}{
		{name: "developer id", f: &fakeRun{team: "TEAM123"}, team: "TEAM123"},
		{name: "ad hoc", f: &fakeRun{team: "not set"}, want: errUnsigned.Error()},
		{name: "codesign fails", f: &fakeRun{readErr: errors.New("exit status 1")}, want: "reading the app's signature: exit status 1"},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			team, err := s.installer(tt.f).TeamID(context.Background())
			if tt.want != "" {
				require.EqualError(s.T(), err, tt.want)
				return
			}
			require.NoError(s.T(), err)
			require.Equal(s.T(), tt.team, team)
		})
	}
}

func (s *UpdateSuite) TestInstall() {
	f := &fakeRun{team: "TEAM123", dittoOK: true, verify: ok}
	inst := s.installer(f)
	require.NoError(s.T(), inst.Install(context.Background(), s.release()))

	_, err := os.Stat(filepath.Join(inst.App, "Contents", "old"))
	require.ErrorIs(s.T(), err, os.ErrNotExist, "the old app is gone")
	require.DirExists(s.T(), filepath.Join(inst.App, "Contents"))
	entries, err := os.ReadDir(s.dir)
	require.NoError(s.T(), err)
	require.Len(s.T(), entries, 1, "staging is cleaned up")
	require.Contains(s.T(), f.calls[2],
		`-R=identifier "io.github.radutopala.macuse" and anchor apple generic and certificate leaf[subject.OU] = "TEAM123"`)
}

func (s *UpdateSuite) TestInstallFailures() {
	tests := []struct {
		name  string
		f     *fakeRun
		rel   func(Release) Release
		setup func(inst *Installer)
		want  string
	}{
		{name: "unsigned", f: &fakeRun{team: "not set"}, want: errUnsigned.Error()},
		{
			name:  "can't stage",
			f:     &fakeRun{team: "T"},
			setup: func(inst *Installer) { inst.App = filepath.Join(s.dir, "missing", appName) },
			want:  "can't stage the update",
		},
		{
			name: "download missing",
			f:    &fakeRun{team: "T"},
			rel:  func(r Release) Release { r.URL = s.srv.URL + "/gone.zip"; return r },
			want: "downloading the update: GET " + s.srv.URL + "/gone.zip: 404 Not Found",
		},
		{
			name: "download cut short",
			f:    &fakeRun{team: "T"},
			rel:  func(r Release) Release { r.URL = s.srv.URL + "/short.zip"; return r },
			want: "downloading the update: unexpected EOF",
		},
		{
			name: "hash mismatch",
			f:    &fakeRun{team: "T"},
			rel:  func(r Release) Release { r.SHA256 = "00"; return r },
			want: "the release lists 00",
		},
		{name: "unpack fails", f: &fakeRun{team: "T"}, want: "unpacking the update: exit status 1: ditto: bad zip"},
		{name: "no app inside", f: &fakeRun{team: "T", dittoOK: true, noApp: true}, want: "the update has no macuse.app"},
		{
			name: "other team",
			f: &fakeRun{team: "T", dittoOK: true, verify: func(string) ([]byte, error) {
				return []byte("code failed to satisfy specified code requirement(s)\n"), errors.New("exit status 3")
			}},
			want: "the update isn't signed by team T: code failed to satisfy specified code requirement(s)",
		},
		{
			name: "app can't move aside",
			f:    &fakeRun{team: "T", dittoOK: true, verify: ok},
			setup: func(inst *Installer) {
				s.Require().NoError(os.RemoveAll(inst.App))
			},
			want: "can't replace",
		},
		{
			name: "new app can't move in",
			f: &fakeRun{team: "T", dittoOK: true, verify: func(app string) ([]byte, error) {
				return nil, os.RemoveAll(app)
			}},
			want: "can't replace",
		},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.Require().NoError(os.RemoveAll(s.dir))
			inst := s.installer(tt.f)
			if tt.setup != nil {
				tt.setup(&inst)
			}
			rel := s.release()
			if tt.rel != nil {
				rel = tt.rel(rel)
			}
			require.ErrorContains(s.T(), inst.Install(context.Background(), rel), tt.want)
			if tt.name == "new app can't move in" {
				require.FileExists(s.T(), filepath.Join(inst.App, "Contents", "old"), "the old app is put back")
			}
		})
	}
}

func (s *UpdateSuite) TestDownloadCantWrite() {
	inst := s.installer(&fakeRun{})
	blocked := filepath.Join(s.dir, "blocked")
	s.Require().NoError(os.MkdirAll(filepath.Join(blocked, "x"), 0o755))
	require.Error(s.T(), inst.download(context.Background(), s.release(), blocked))
}
