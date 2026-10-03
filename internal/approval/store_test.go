package approval

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

var t0 = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func fixedNow() time.Time { return t0 }

type StoreSuite struct {
	suite.Suite
	path string
}

func TestStoreSuite(t *testing.T) { suite.Run(t, new(StoreSuite)) }

func (s *StoreSuite) SetupTest() {
	s.path = filepath.Join(s.T().TempDir(), "sub", "approvals.json")
}

func (s *StoreSuite) TestMissingFileIsEmpty() {
	st, err := OpenStore(s.path, fixedNow)
	require.NoError(s.T(), err)
	require.Empty(s.T(), st.List())
	_, ok := st.Lookup("a", "")
	require.False(s.T(), ok)
	_, ok = st.LookupBundle("a")
	require.False(s.T(), ok)
}

func (s *StoreSuite) TestOpenErrors() {
	dir := s.T().TempDir()
	_, err := OpenStore(dir, fixedNow)
	require.Error(s.T(), err)

	bad := filepath.Join(dir, "bad.json")
	require.NoError(s.T(), os.WriteFile(bad, []byte("{"), 0o600))
	_, err = OpenStore(bad, fixedNow)
	require.ErrorContains(s.T(), err, "approvals")
}

func (s *StoreSuite) TestSaveLookupDeleteAndReload() {
	st, err := OpenStore(s.path, fixedNow)
	require.NoError(s.T(), err)
	require.NoError(s.T(), st.Save(Saved{BundleID: "com.b", TeamID: "T1", Name: "Bee", Decision: Allow}))
	require.NoError(s.T(), st.Save(Saved{BundleID: "com.a", TeamID: "T2", Name: "Ant", Decision: Deny}))
	require.NoError(s.T(), st.Save(Saved{BundleID: "com.c", Name: "Ant", Decision: Allow}))
	// Replaces the earlier decision on the same app.
	require.NoError(s.T(), st.Save(Saved{BundleID: "com.b", TeamID: "T1", Name: "Bee", Decision: Deny}))

	got, ok := st.Lookup("com.b", "T1")
	require.True(s.T(), ok)
	require.Equal(s.T(), Deny, got.Decision)
	require.Equal(s.T(), t0, got.CreatedAt)
	_, ok = st.Lookup("com.b", "T9")
	require.False(s.T(), ok)
	got, ok = st.LookupBundle("com.a")
	require.True(s.T(), ok)
	require.Equal(s.T(), "T2", got.TeamID)

	list := st.List()
	require.Len(s.T(), list, 3)
	require.Equal(s.T(), []string{"com.a", "com.c", "com.b"}, []string{list[0].BundleID, list[1].BundleID, list[2].BundleID})

	info, err := os.Stat(s.path)
	require.NoError(s.T(), err)
	require.Equal(s.T(), os.FileMode(0o600), info.Mode().Perm())

	again, err := OpenStore(s.path, fixedNow)
	require.NoError(s.T(), err)
	require.Equal(s.T(), list, again.List())

	require.NoError(s.T(), st.Delete("com.b", "T1"))
	require.NoError(s.T(), st.Delete("com.none", ""))
	require.Len(s.T(), st.List(), 2)
}

func (s *StoreSuite) TestWriteErrors() {
	dir := s.T().TempDir()
	file := filepath.Join(dir, "file")
	require.NoError(s.T(), os.WriteFile(file, nil, 0o600))

	tests := []struct {
		name string
		path string
	}{
		{"dir can't be created", filepath.Join(file, "sub", "approvals.json")},
		{"temp file can't be written", filepath.Join(dir, "blocked", "approvals.json")},
		{"rename onto a directory", filepath.Join(dir, "isdir")},
	}
	// A non-empty directory where the temp file goes; permission bits
	// wouldn't stop root.
	require.NoError(s.T(), os.MkdirAll(filepath.Join(dir, "blocked", "approvals.json.tmp", "x"), 0o700))
	require.NoError(s.T(), os.MkdirAll(filepath.Join(dir, "isdir", "x"), 0o700))
	for _, tt := range tests {
		s.Run(tt.name, func() {
			st := &Store{path: tt.path, now: fixedNow}
			require.Error(s.T(), st.Save(Saved{BundleID: "a"}))
			require.Error(s.T(), st.Delete("a", ""))
			require.Empty(s.T(), st.List())
		})
	}
}
