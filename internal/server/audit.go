package server

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

// Entry is one audited call. Typed text is kept only as its length.
type Entry struct {
	Time       time.Time `json:"time"`
	Client     string    `json:"client"`
	Session    string    `json:"session"`
	App        string    `json:"app,omitempty"`
	BundleID   string    `json:"bundle_id"`
	TeamID     string    `json:"team_id,omitempty"`
	Action     string    `json:"action"`
	Decision   string    `json:"decision"`
	Rule       string    `json:"rule"`
	TypedChars int       `json:"typed_chars,omitempty"`
}

// Audit appends entries as JSON lines.
type Audit struct {
	mu  sync.Mutex
	w   io.Writer
	now func() time.Time
}

// NewAudit writes to w, stamping entries with now.
func NewAudit(w io.Writer, now func() time.Time) *Audit {
	return &Audit{w: w, now: now}
}

// Write appends e.
func (a *Audit) Write(e Entry) error {
	e.Time = a.now().UTC()
	line, _ := json.Marshal(e)
	a.mu.Lock()
	defer a.mu.Unlock()
	_, err := a.w.Write(append(line, '\n'))
	return err
}
