// Package buildinfo holds what identifies this build of macuse.
package buildinfo

// BundleID is the app bundle's identifier. macOS ties the Accessibility and
// Screen Recording grants to it, so it never changes.
const BundleID = "io.github.radutopala.macuse"

// Version is set at build time with -ldflags "-X ...buildinfo.Version=v1.2.3".
var Version = "dev"
