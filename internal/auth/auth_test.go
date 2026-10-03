package auth

import (
	"bytes"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type AuthSuite struct {
	suite.Suite
}

func TestAuthSuite(t *testing.T) {
	suite.Run(t, new(AuthSuite))
}

type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

func (s *AuthSuite) TestNew() {
	tok, err := Tokens{Rand: bytes.NewReader(bytes.Repeat([]byte{0xab}, 32))}.New()
	require.NoError(s.T(), err)
	require.Equal(s.T(), strings.Repeat("ab", 32), tok)

	_, err = Tokens{Rand: failReader{}}.New()
	require.EqualError(s.T(), err, "token: no entropy")
}

func (s *AuthSuite) TestEnsureCreatesThenReuses() {
	path := filepath.Join(s.T().TempDir(), "sub", "token")
	tok, err := DefaultTokens.Ensure(path)
	require.NoError(s.T(), err)
	require.Len(s.T(), tok, 64)

	info, err := os.Stat(path)
	require.NoError(s.T(), err)
	require.Equal(s.T(), fs.FileMode(0o600), info.Mode().Perm())
	dir, err := os.Stat(filepath.Dir(path))
	require.NoError(s.T(), err)
	require.Equal(s.T(), fs.FileMode(0o700), dir.Mode().Perm())

	again, err := DefaultTokens.Ensure(path)
	require.NoError(s.T(), err)
	require.Equal(s.T(), tok, again)
}

func (s *AuthSuite) TestEnsureErrors() {
	dir := s.T().TempDir()
	tests := []struct {
		name   string
		tokens Tokens
		path   func() string
	}{
		{"empty token file", DefaultTokens, func() string {
			p := filepath.Join(dir, "empty")
			require.NoError(s.T(), os.WriteFile(p, []byte("\n"), 0o600))
			return p
		}},
		{"no entropy", Tokens{Rand: failReader{}}, func() string { return filepath.Join(dir, "none") }},
		{"parent is a file", DefaultTokens, func() string {
			p := filepath.Join(dir, "file")
			require.NoError(s.T(), os.WriteFile(p, nil, 0o600))
			return filepath.Join(p, "token")
		}},
		// A dangling link reads as missing, but writing through it fails;
		// unlike permission bits, that holds for root too.
		{"dir can't be created", DefaultTokens, func() string {
			p := filepath.Join(dir, "dangling-dir")
			require.NoError(s.T(), os.Symlink(filepath.Join(dir, "gone-dir"), p))
			return filepath.Join(p, "token")
		}},
		{"can't be written", DefaultTokens, func() string {
			p := filepath.Join(dir, "dangling")
			require.NoError(s.T(), os.Symlink(filepath.Join(dir, "gone", "token"), p))
			return p
		}},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			_, err := tc.tokens.Ensure(tc.path())
			require.Error(s.T(), err)
		})
	}
}

func (s *AuthSuite) TestBearer() {
	h := Bearer("secret", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	tests := []struct {
		name   string
		header string
		want   int
	}{
		{"right token", "Bearer secret", http.StatusNoContent},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"no scheme", "secret", http.StatusUnauthorized},
		{"missing", "", http.StatusUnauthorized},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			require.Equal(s.T(), tc.want, rec.Code)
			if tc.want == http.StatusUnauthorized {
				require.Equal(s.T(), "Bearer", rec.Header().Get("WWW-Authenticate"))
				require.Contains(s.T(), rec.Body.String(), `"code":"unauthorized"`)
			}
		})
	}
}
