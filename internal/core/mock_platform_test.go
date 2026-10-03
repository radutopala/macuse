package core

import (
	"image"
	"time"

	"github.com/stretchr/testify/mock"

	"github.com/radutopala/mac-use/internal/proto"
)

type mockPlatform struct {
	mock.Mock
}

func (m *mockPlatform) ListApps() ([]proto.App, error) {
	args := m.Called()
	apps, _ := args.Get(0).([]proto.App)
	return apps, args.Error(1)
}

func (m *mockPlatform) StartApp(bundleID string) error { return m.Called(bundleID).Error(0) }

func (m *mockPlatform) Activate(app proto.App) error { return m.Called(app).Error(0) }

func (m *mockPlatform) Frontmost(app proto.App) bool { return m.Called(app).Bool(0) }

func (m *mockPlatform) FocusedWindow(app proto.App) (Window, error) {
	args := m.Called(app)
	return args.Get(0).(Window), args.Error(1)
}

func (m *mockPlatform) Tree(win Window, lim Limits) (*Node, bool, error) {
	args := m.Called(win, lim)
	root, _ := args.Get(0).(*Node)
	return root, args.Bool(1), args.Error(2)
}

func (m *mockPlatform) Capture(win Window) (image.Image, error) {
	args := m.Called(win)
	img, _ := args.Get(0).(image.Image)
	return img, args.Error(1)
}

func (m *mockPlatform) Press(ref uintptr) error { return m.Called(ref).Error(0) }

func (m *mockPlatform) SetValue(ref uintptr, value string) error {
	return m.Called(ref, value).Error(0)
}

func (m *mockPlatform) Focus(ref uintptr) error { return m.Called(ref).Error(0) }

func (m *mockPlatform) InsertText(app proto.App, ref uintptr, text string) error {
	return m.Called(app, ref, text).Error(0)
}

func (m *mockPlatform) UserIdle() time.Duration { return m.Called().Get(0).(time.Duration) }

func (m *mockPlatform) Click(p Point, button Button, count int) error {
	return m.Called(p, button, count).Error(0)
}

func (m *mockPlatform) Drag(from, to Point) error { return m.Called(from, to).Error(0) }

func (m *mockPlatform) Scroll(p Point, dx, dy int) error { return m.Called(p, dx, dy).Error(0) }

func (m *mockPlatform) Key(c Combo) error { return m.Called(c).Error(0) }

func (m *mockPlatform) KeyTo(app proto.App, c Combo) error { return m.Called(app, c).Error(0) }

func (m *mockPlatform) TypeTo(app proto.App, text string) error { return m.Called(app, text).Error(0) }

func (m *mockPlatform) Type(text string) error { return m.Called(text).Error(0) }

func (m *mockPlatform) Release(refs []uintptr) { m.Called(refs) }

func (m *mockPlatform) Permissions() proto.Permissions {
	return m.Called().Get(0).(proto.Permissions)
}

func (m *mockPlatform) RequestPermissions() proto.Permissions {
	return m.Called().Get(0).(proto.Permissions)
}
