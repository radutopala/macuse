package native

import (
	"context"
	"errors"
	"os"
	"runtime"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/radutopala/macuse/internal/core"
	"github.com/radutopala/macuse/internal/proto"
)

func init() { runtime.LockOSThread() }

// platform is served by TestMain on the main thread, as the helper does.
var platform Runner

func TestMain(m *testing.M) {
	p, err := New(nil)
	if err != nil {
		panic(err)
	}
	platform = p
	ctx, cancel := context.WithCancel(context.Background())
	code := 0
	go func() {
		defer cancel()
		code = m.Run()
	}()
	_ = p.Run(ctx)
	os.Exit(code)
}

// DarwinSuite smoke-tests the bindings on a real Mac without privacy grants.
type DarwinSuite struct {
	suite.Suite
}

func TestDarwinSuite(t *testing.T) {
	suite.Run(t, new(DarwinSuite))
}

func (s *DarwinSuite) TestListApps() {
	apps, err := platform.ListApps()
	require.NoError(s.T(), err)
	for _, app := range apps {
		require.NotEmpty(s.T(), app.BundleID)
		require.Positive(s.T(), app.PID)
	}
}

func (s *DarwinSuite) TestPermissionsDoNotPrompt() {
	// The grants depend on the machine; the call must answer without a
	// prompt blocking it.
	require.NotPanics(s.T(), func() { platform.Permissions() })
}

func (s *DarwinSuite) TestStartUnknownApp() {
	err := platform.StartApp("invalid.macuse.no-such-app")
	var perr *proto.Error
	require.True(s.T(), errors.As(err, &perr), "got %v", err)
	require.Equal(s.T(), proto.CodeAppNotFound, perr.Code)
}

func (s *DarwinSuite) TestKeyEventsReleaseModifiers() {
	const n = 45
	cmd, shift := uint64(kCGEventFlagMaskCommand), uint64(kCGEventFlagMaskShift)
	tests := []struct {
		name string
		mods core.Modifier
		want []keyEvent
	}{
		{"plain key", 0, []keyEvent{{n, true, 0}, {n, false, 0}}},
		{"cmd", core.ModCmd, []keyEvent{
			{55, true, cmd},
			{n, true, cmd}, {n, false, cmd},
			{55, false, 0},
		}},
		{"cmd+shift", core.ModCmd | core.ModShift, []keyEvent{
			{55, true, cmd}, {56, true, cmd | shift},
			{n, true, cmd | shift}, {n, false, cmd | shift},
			{56, false, cmd}, {55, false, 0},
		}},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			got := keyEvents(n, tt.mods)
			require.Equal(s.T(), tt.want, got)
			require.Zero(s.T(), got[len(got)-1].flags, "the last event must leave no modifier held")
		})
	}
}

func (s *DarwinSuite) TestTypeChunksPressReturnForLineBreaks() {
	u := func(s string) []uint16 { return utf16.Encode([]rune(s)) }
	tests := []struct {
		name  string
		text  string
		chunk int
		want  [][]uint16
	}{
		{"one line", "abc", 20, [][]uint16{u("abc")}},
		{"split in chunks", "abcde", 2, [][]uint16{u("ab"), u("cd"), u("e")}},
		{"line breaks", "a\nb", 20, [][]uint16{u("a"), nil, u("b")}},
		{"blank line and trailing break", "a\n\nb\n", 20, [][]uint16{u("a"), nil, nil, u("b"), nil}},
		{"crlf and cr", "a\r\nb\rc", 20, [][]uint16{u("a"), nil, u("b"), nil, u("c")}},
		{"surrogate pair", "😀x", 1, [][]uint16{u("😀"), u("x")}},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			require.Equal(s.T(), tt.want, typeChunks(tt.text, tt.chunk))
		})
	}
}
