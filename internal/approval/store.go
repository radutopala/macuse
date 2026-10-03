// Package approval decides which apps agents may control: the user's saved
// decisions, the session allows, and the requests waiting on the user.
package approval

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Saved decisions.
const (
	Allow = "allow"
	Deny  = "deny"
)

// Saved is the user's lasting decision on an app. An app is its bundle id
// and code-signing team together, so a re-signed app reusing an approved
// bundle id doesn't inherit the decision. TeamID is empty for unsigned apps.
type Saved struct {
	BundleID  string    `json:"bundle_id"`
	TeamID    string    `json:"team_id"`
	Name      string    `json:"name"`
	Decision  string    `json:"decision"`
	CreatedAt time.Time `json:"created_at"`
}

// Store keeps the saved decisions in a JSON file.
type Store struct {
	path string
	now  func() time.Time

	mu    sync.Mutex
	saved []Saved
}

// OpenStore loads the decisions at path; a missing file is none.
func OpenStore(path string, now func() time.Time) (*Store, error) {
	s := &Store{path: path, now: now}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.saved); err != nil {
		return nil, fmt.Errorf("approvals %s: %w", path, err)
	}
	return s, nil
}

// Lookup finds the decision on exactly this app.
func (s *Store) Lookup(bundleID, teamID string) (Saved, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.saved {
		if a.BundleID == bundleID && a.TeamID == teamID {
			return a, true
		}
	}
	return Saved{}, false
}

// LookupBundle finds a decision on the bundle id under any team, for an app
// that isn't running yet and so has no known team.
func (s *Store) LookupBundle(bundleID string) (Saved, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.saved {
		if a.BundleID == bundleID {
			return a, true
		}
	}
	return Saved{}, false
}

// Save records the decision, replacing any on the same app.
func (s *Store) Save(a Saved) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a.CreatedAt = s.now().UTC()
	next := make([]Saved, 0, len(s.saved)+1)
	for _, old := range s.saved {
		if old.BundleID != a.BundleID || old.TeamID != a.TeamID {
			next = append(next, old)
		}
	}
	next = append(next, a)
	return s.write(next)
}

// Delete forgets the decision on the app; a missing one is no error.
func (s *Store) Delete(bundleID, teamID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make([]Saved, 0, len(s.saved))
	for _, a := range s.saved {
		if a.BundleID != bundleID || a.TeamID != teamID {
			next = append(next, a)
		}
	}
	return s.write(next)
}

// List returns the decisions sorted by app name.
func (s *Store) List() []Saved {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]Saved(nil), s.saved...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].BundleID < out[j].BundleID
	})
	return out
}

// write replaces the file atomically, then the in-memory list.
func (s *Store) write(next []Saved) error {
	data, _ := json.MarshalIndent(next, "", "  ")
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.saved = next
	return nil
}
