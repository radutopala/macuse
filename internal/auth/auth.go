// Package auth keeps the API token and checks it on requests.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/radutopala/mac-use/internal/proto"
)

// Tokens creates and reads token files. Rand is the entropy source.
type Tokens struct {
	Rand io.Reader
}

// New returns random hex: 32 bytes of entropy.
func (t Tokens) New() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(t.Rand, b); err != nil {
		return "", fmt.Errorf("token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Ensure returns the token at path, creating it (0600, in a 0700 dir) the
// first time.
func (t Tokens) Ensure(path string) (string, error) {
	tok, err := Read(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return tok, err
	}
	tok, err = t.New()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// DefaultTokens draws from crypto/rand.
var DefaultTokens = Tokens{Rand: rand.Reader}

// Read returns the token at path.
func Read(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		return "", fmt.Errorf("token file %s is empty", path)
	}
	return tok, nil
}

// Bearer serves next only for requests carrying "Authorization: Bearer
// <token>".
func Bearer(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || !Equal(got, token) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(proto.ErrorBody{Error: &proto.Error{
				Code:    proto.CodeUnauthorized,
				Message: "missing or wrong token; run `mac-use token` on the Mac to get it",
			}})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Equal compares secrets in constant time.
func Equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
