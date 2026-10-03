package fsmigrate

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// migrations holds all migrations in order. Position in the slice is the
// version (index 0 is the bootstrap placeholder, never applied). Append a
// new entry to ship a change; never reorder or delete entries.
var migrations = []Migration{
	{Description: "bootstrap"},
	{Description: "move files from ~/Library to ~/.macuse", Apply: moveFromLibrary},
}

// moveFromLibrary moves the files releases up to 2026.10.3 kept in
// ~/Library, leaving any file ~/.macuse already has, and points the
// LaunchAgent's log at the new place. A running agent keeps writing to its
// open log, which moves with it.
func moveFromLibrary(c Ctx) error {
	support := filepath.Join(c.Home, "Library", "Application Support", "macuse")
	logs := filepath.Join(c.Home, "Library", "Logs", "macuse")
	oldLog := filepath.Join(logs, "macuse.log")
	for _, m := range [][2]string{
		{filepath.Join(support, "config.json"), c.Paths.Config},
		{filepath.Join(support, "token"), c.Paths.Token},
		{filepath.Join(support, "approvals.json"), c.Paths.Approvals},
		{filepath.Join(logs, "audit.jsonl"), c.Paths.Audit},
		{oldLog, c.Paths.Log},
	} {
		if err := move(m[0], m[1]); err != nil {
			return err
		}
	}
	// Only removes them once empty.
	_ = os.Remove(support)
	_ = os.Remove(logs)

	plist, err := os.ReadFile(c.Paths.LaunchAgent)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	fixed := bytes.ReplaceAll(plist, xmlText(oldLog), xmlText(c.Paths.Log))
	if bytes.Equal(fixed, plist) {
		return nil
	}
	return os.WriteFile(c.Paths.LaunchAgent, fixed, 0o644)
}

// move renames from to to, unless from is gone or to is there already.
func move(from, to string) error {
	if _, err := os.Lstat(from); err != nil {
		return nil
	}
	if _, err := os.Lstat(to); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	// Another macuse may have just moved it.
	if err := os.Rename(from, to); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("moving %s: %w", from, err)
	}
	return nil
}

func xmlText(s string) []byte {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s)) // a bytes.Buffer never fails
	return b.Bytes()
}
