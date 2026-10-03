package proto

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestErrorString(t *testing.T) {
	require.EqualError(t, &Error{Code: CodeAppNotFound, Message: "gone"}, "app_not_found: gone")
}
