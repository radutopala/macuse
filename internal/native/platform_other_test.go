//go:build !darwin

package native

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnsupported(t *testing.T) {
	p, err := New(nil)
	require.Nil(t, p)
	require.ErrorIs(t, err, ErrUnsupported)
}
