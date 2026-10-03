//go:build !darwin

package native

import "log/slog"

// New always fails off macOS.
func New(*slog.Logger) (Runner, error) {
	return nil, ErrUnsupported
}
