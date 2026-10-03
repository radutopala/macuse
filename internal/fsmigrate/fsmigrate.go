// Package fsmigrate runs versioned migrations of the files macuse keeps,
// each once. The version applied last is kept in the fs_migrations file;
// migrations are append-only, with no down migrations.
package fsmigrate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/radutopala/macuse/internal/config"
)

// Ctx is passed to every migration.
type Ctx struct {
	Home  string
	Paths config.Paths
}

// Migration is one migration step. Apply must be safe to run again, as
// two macuse processes may start at once.
type Migration struct {
	Description string
	Apply       func(c Ctx) error
}

// Run applies the pending migrations.
func Run(c Ctx) error { return run(c, migrations) }

func run(c Ctx, ms []Migration) error {
	state := filepath.Join(c.Paths.Dir, "fs_migrations")
	applied := 0
	data, err := os.ReadFile(state)
	switch {
	case err == nil:
		if applied, err = strconv.Atoi(strings.TrimSpace(string(data))); err != nil {
			return fmt.Errorf("reading %s: %w", state, err)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	for v := applied + 1; v < len(ms); v++ {
		if err := ms[v].Apply(c); err != nil {
			return fmt.Errorf("applying fs migration %d (%s): %w", v, ms[v].Description, err)
		}
		if err := writeState(state, v); err != nil {
			return fmt.Errorf("recording fs migration %d: %w", v, err)
		}
	}
	return nil
}

// writeState swaps the new version in, so a crash never leaves the file
// empty.
func writeState(path string, v int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(v)+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
