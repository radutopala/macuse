package native

import (
	"context"
	"errors"
	"os"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/radutopala/mac-use/internal/proto"
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
