// Package update keeps MacUse.app current from its GitHub releases: it
// finds the latest release, then downloads, verifies and swaps in the new
// app bundle.
package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/radutopala/macuse/internal/buildinfo"
)

// DefaultFeed serves the latest release's files.
const DefaultFeed = "https://github.com/radutopala/macuse/releases/latest/download"

// Asset names: macuse_2026.10.1_macos.zip, listed in checksums.txt.
const (
	assetPrefix = "macuse_"
	assetSuffix = "_macos.zip"
	checksums   = "checksums.txt"
	appName     = "MacUse.app"
)

// Release is a published version of the app.
type Release struct {
	Version string
	URL     string
	SHA256  string
}

// Latest reads the latest release from feed's checksums.txt, which names
// the app zip and its hash.
func Latest(ctx context.Context, hc *http.Client, feed string) (Release, error) {
	body, err := get(ctx, hc, feed+"/"+checksums)
	if err != nil {
		return Release{}, err
	}
	defer func() { _ = body.Close() }()
	sc := bufio.NewScanner(body)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		name := fields[1]
		if !strings.HasPrefix(name, assetPrefix) || !strings.HasSuffix(name, assetSuffix) {
			continue
		}
		return Release{
			Version: strings.TrimSuffix(strings.TrimPrefix(name, assetPrefix), assetSuffix),
			URL:     feed + "/" + name,
			SHA256:  fields[0],
		}, nil
	}
	if err := sc.Err(); err != nil {
		return Release{}, fmt.Errorf("reading %s: %w", checksums, err)
	}
	return Release{}, fmt.Errorf("the latest release has no %s", assetPrefix+"<version>"+assetSuffix)
}

func get(ctx context.Context, hc *http.Client, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		_ = res.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, res.Status)
	}
	return res.Body, nil
}

// parse splits a release version (v2026.10.1 or 2026.10.1) into numbers.
// Builds between releases (v2026.10.1-3-gabc, dev) don't parse.
func parse(v string) ([]int, bool) {
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	nums := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		nums[i] = n
	}
	return nums, true
}

// Valid reports whether v is a release version, which can update itself.
func Valid(v string) bool {
	_, ok := parse(v)
	return ok
}

// Newer reports whether latest is a later release than current.
func Newer(current, latest string) bool {
	c, ok := parse(current)
	if !ok {
		return false
	}
	l, ok := parse(latest)
	if !ok {
		return false
	}
	for i := 0; i < max(len(c), len(l)); i++ {
		var a, b int
		if i < len(c) {
			a = c[i]
		}
		if i < len(l) {
			b = l[i]
		}
		if a != b {
			return b > a
		}
	}
	return false
}

// Installer replaces the running app with a release.
type Installer struct {
	// App is the running MacUse.app.
	App  string
	HTTP *http.Client
	// Run runs a command (codesign, ditto), returning its combined output.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// errUnsigned is why a build without a Developer ID can't update: there is
// no team to hold the download to.
var errUnsigned = errors.New("this copy of macuse isn't signed with a Developer ID, so it can't update itself")

// TeamID is the signing team of the running app.
func (i Installer) TeamID(ctx context.Context) (string, error) {
	out, err := i.Run(ctx, "codesign", "-dv", "--verbose=2", i.App)
	if err != nil {
		return "", fmt.Errorf("reading the app's signature: %w", err)
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if team, ok := strings.CutPrefix(strings.TrimSpace(line), "TeamIdentifier="); ok && team != "not set" {
			return team, nil
		}
	}
	return "", errUnsigned
}

// Install downloads rel, checks its hash and that the same team signed it,
// and swaps it in for the app. The running process keeps its own files
// until it exits, so a restart then runs the new version.
func (i Installer) Install(ctx context.Context, rel Release) error {
	team, err := i.TeamID(ctx)
	if err != nil {
		return err
	}
	// Staged next to the app, so the swap is a rename on one volume.
	staging, err := os.MkdirTemp(filepath.Dir(i.App), ".macuse-update-")
	if err != nil {
		return fmt.Errorf("can't stage the update: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	zip := filepath.Join(staging, "update.zip")
	if err := i.download(ctx, rel, zip); err != nil {
		return err
	}
	unpacked := filepath.Join(staging, "new")
	if out, err := i.Run(ctx, "ditto", "-x", "-k", zip, unpacked); err != nil {
		return fmt.Errorf("unpacking the update: %w: %s", err, strings.TrimSpace(string(out)))
	}
	next := filepath.Join(unpacked, appName)
	if _, err := os.Stat(next); err != nil {
		return fmt.Errorf("the update has no %s", appName)
	}
	req := fmt.Sprintf(`-R=identifier %q and anchor apple generic and certificate leaf[subject.OU] = %q`, buildinfo.BundleID, team)
	if out, err := i.Run(ctx, "codesign", "--verify", "--deep", "--strict", req, next); err != nil {
		return fmt.Errorf("the update isn't signed by team %s: %s", team, strings.TrimSpace(string(out)))
	}

	old := filepath.Join(staging, "old.app")
	if err := os.Rename(i.App, old); err != nil {
		return fmt.Errorf("can't replace %s: %w", i.App, err)
	}
	if err := os.Rename(next, i.App); err != nil {
		_ = os.Rename(old, i.App)
		return fmt.Errorf("can't replace %s: %w", i.App, err)
	}
	takeName(i.App, os.Rename)
	return nil
}

// takeName gives the app the release's name where that is the same path,
// as macuse.app became MacUse.app on a volume that ignores case. Elsewhere
// a rename would break the paths that point at the app, so it keeps its
// name.
func takeName(app string, rename func(from, to string) error) {
	want := filepath.Join(filepath.Dir(app), appName)
	a, errA := os.Stat(app)
	b, errB := os.Stat(want)
	if want != app && errA == nil && errB == nil && os.SameFile(a, b) {
		_ = rename(app, want)
	}
}

func (i Installer) download(ctx context.Context, rel Release, path string) error {
	body, err := get(ctx, i.HTTP, rel.URL)
	if err != nil {
		return fmt.Errorf("downloading the update: %w", err)
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("downloading the update: %w", err)
	}
	h := sha256.Sum256(data)
	if sum := hex.EncodeToString(h[:]); !strings.EqualFold(sum, rel.SHA256) {
		return fmt.Errorf("the download's SHA-256 is %s, the release lists %s", sum, rel.SHA256)
	}
	return os.WriteFile(path, data, 0o600)
}
