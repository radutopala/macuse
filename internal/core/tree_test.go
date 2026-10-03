package core

import (
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type TreeSuite struct {
	suite.Suite
}

func TestTreeSuite(t *testing.T) {
	suite.Run(t, new(TreeSuite))
}

func sampleTree() *Node {
	return &Node{Role: "AXWindow", Name: "Untitled", Ref: 1, Children: []*Node{
		{Role: "AXTextArea", Value: "hello\n  world", Ref: 2, Frame: Rect{150, 60, 100, 200}},
		{Role: "AXButton", Name: "OK", Actions: []string{"AXPress"}, Ref: 3, Children: []*Node{
			{Role: "AXStaticText", Value: "OK", Ref: 4, Frame: Rect{0, 0, 10, 10}},
		}},
	}}
}

func (s *TreeSuite) TestFlattenAndRender() {
	elems := Flatten(sampleTree())
	require.Len(s.T(), elems, 4)
	require.Equal(s.T(), []uintptr{1, 2, 3, 4}, Refs(elems))
	require.Equal(s.T(), 2, elems[3].Depth)
	require.True(s.T(), elems[2].HasAction("AXPress"))
	require.False(s.T(), elems[1].HasAction("AXPress"))

	require.Equal(s.T(), `[1] Window "Untitled"
  [2] TextArea (value: hello world)
  [3] Button "OK"
    [4] StaticText (value: OK)
`, Render(elems))
}

func (s *TreeSuite) TestFlattenNil() {
	require.Empty(s.T(), Flatten(nil))
}

func (s *TreeSuite) TestRenderCapsLongValues() {
	out := Render([]Element{{Index: 1, Node: &Node{Role: "AXTextArea", Value: strings.Repeat("é", 300)}}})
	require.Contains(s.T(), out, strings.Repeat("é", maxValueLen)+"…)")
	require.NotContains(s.T(), out, strings.Repeat("é", maxValueLen+1))
}

func (s *TreeSuite) TestCenter() {
	require.Equal(s.T(), Point{60, 45}, Rect{10, 20, 100, 50}.Center())
}

func (s *TreeSuite) TestIntersect() {
	win := Rect{100, 50, 100, 50}
	tests := []struct {
		name string
		r    Rect
		want Rect
		ok   bool
	}{
		{name: "inside", r: Rect{110, 60, 10, 10}, want: Rect{110, 60, 10, 10}, ok: true},
		{name: "runs past the bottom", r: Rect{150, 60, 100, 200}, want: Rect{150, 60, 50, 40}, ok: true},
		{name: "covers the window", r: Rect{0, 0, 500, 500}, want: win, ok: true},
		{name: "left of it", r: Rect{0, 60, 10, 10}},
		{name: "below it", r: Rect{110, 100, 10, 10}},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			got, ok := tc.r.Intersect(win)
			require.Equal(s.T(), tc.ok, ok)
			require.Equal(s.T(), tc.want, got)
		})
	}
}

func (s *TreeSuite) TestDiff() {
	require.Equal(s.T(), "(no changes since the last read)\n", Diff("a\n", "a\n"))

	out := Diff("a\nb\n", "a\nc\n")
	require.Contains(s.T(), out, "-b")
	require.Contains(s.T(), out, "+c")
}

func (s *TreeSuite) TestEncodeJPEGKeepsSmallImages() {
	enc, err := EncodeJPEG(solid(40, 20), 100)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 40, enc.Width)
	require.Equal(s.T(), 20, enc.Height)
	require.Equal(s.T(), []byte{0xff, 0xd8}, enc.Data[:2])
}

func (s *TreeSuite) TestEncodeJPEGScalesDown() {
	enc, err := EncodeJPEG(solid(400, 100), 200)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 200, enc.Width)
	require.Equal(s.T(), 50, enc.Height)

	img, _, err := image.Decode(strings.NewReader(string(enc.Data)))
	require.NoError(s.T(), err)
	require.Equal(s.T(), image.Rect(0, 0, 200, 50), img.Bounds())

	thin, err := EncodeJPEG(solid(1000, 1), 10)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 1, thin.Height)
}

func (s *TreeSuite) TestEncodeJPEGError() {
	// JPEG cannot encode a side over 65535 pixels.
	_, err := EncodeJPEG(solid(70000, 1), 100000)
	require.Error(s.T(), err)
}

func solid(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{200, 10, 10, 255})
		}
	}
	return img
}
