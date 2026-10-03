package core

import (
	"fmt"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// Point is a position in screen points.
type Point struct{ X, Y float64 }

// Rect is a frame in screen points.
type Rect struct{ X, Y, W, H float64 }

// Center is the middle of the frame.
func (r Rect) Center() Point { return Point{r.X + r.W/2, r.Y + r.H/2} }

// Intersect is the part of r inside o; ok is false when they don't overlap.
func (r Rect) Intersect(o Rect) (Rect, bool) {
	x1, y1 := max(r.X, o.X), max(r.Y, o.Y)
	x2, y2 := min(r.X+r.W, o.X+o.W), min(r.Y+r.H, o.Y+o.H)
	if x2 <= x1 || y2 <= y1 {
		return Rect{}, false
	}
	return Rect{x1, y1, x2 - x1, y2 - y1}, true
}

// Union is the smallest frame holding both; an empty frame adds nothing.
func (r Rect) Union(o Rect) Rect {
	if r.W <= 0 || r.H <= 0 {
		return o
	}
	if o.W <= 0 || o.H <= 0 {
		return r
	}
	x1, y1 := min(r.X, o.X), min(r.Y, o.Y)
	x2, y2 := max(r.X+r.W, o.X+o.W), max(r.Y+r.H, o.Y+o.H)
	return Rect{x1, y1, x2 - x1, y2 - y1}
}

// Node is one accessibility element as the platform reports it. Ref is the
// platform's handle for the element, valid until the platform releases it.
type Node struct {
	Role     string
	Name     string
	Value    string
	Frame    Rect
	Actions  []string
	Ref      uintptr
	Children []*Node
}

// Limits bound an accessibility tree walk.
type Limits struct {
	MaxDepth int
	MaxNodes int
}

// DefaultLimits keep a window's tree within what a model can read.
var DefaultLimits = Limits{MaxDepth: 40, MaxNodes: 800}

// Element is a node with its 1-based index and depth in the flattened tree.
type Element struct {
	Index int
	Depth int
	Node  *Node
}

// HasAction reports whether the element supports the named action.
func (e Element) HasAction(name string) bool {
	for _, a := range e.Node.Actions {
		if a == name {
			return true
		}
	}
	return false
}

// Flatten lists the tree in pre-order, numbering elements from 1.
func Flatten(root *Node) []Element {
	var out []Element
	var walk func(n *Node, depth int)
	walk = func(n *Node, depth int) {
		out = append(out, Element{Index: len(out) + 1, Depth: depth, Node: n})
		for _, c := range n.Children {
			walk(c, depth+1)
		}
	}
	if root != nil {
		walk(root, 0)
	}
	return out
}

const rolePopover = "AXPopover"

// Clips finds where each element can show: inside its popover, which may
// hang past the window's edge, or else inside the window. bounds holds the
// window and every popover, the area a screenshot needs.
func Clips(elems []Element, window Rect) (clips []Rect, bounds Rect) {
	clips = make([]Rect, len(elems))
	bounds = window
	// open holds the popovers enclosing the current element, innermost last.
	var open []Element
	for i, e := range elems {
		for len(open) > 0 && open[len(open)-1].Depth >= e.Depth {
			open = open[:len(open)-1]
		}
		if f := e.Node.Frame; e.Node.Role == rolePopover && f.W > 0 && f.H > 0 {
			open = append(open, e)
			bounds = bounds.Union(f)
		}
		clips[i] = window
		if len(open) > 0 {
			clips[i] = open[len(open)-1].Node.Frame
		}
	}
	return clips, bounds
}

// Refs collects every element's platform handle.
func Refs(elems []Element) []uintptr {
	refs := make([]uintptr, 0, len(elems))
	for _, e := range elems {
		refs = append(refs, e.Node.Ref)
	}
	return refs
}

const maxValueLen = 200

// Render formats elements one per line: `[N] role "name" (value: v)`,
// indented by depth.
func Render(elems []Element) string {
	var b strings.Builder
	for _, e := range elems {
		b.WriteString(strings.Repeat("  ", e.Depth))
		fmt.Fprintf(&b, "[%d] %s", e.Index, strings.TrimPrefix(e.Node.Role, "AX"))
		if name := oneLine(e.Node.Name); name != "" {
			fmt.Fprintf(&b, " %q", name)
		}
		if value := oneLine(e.Node.Value); value != "" {
			fmt.Fprintf(&b, " (value: %s)", value)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// oneLine flattens newlines and caps the length so one element stays on one
// line.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxValueLen {
		s = string(r[:maxValueLen]) + "…"
	}
	return s
}

// Diff returns a unified diff from the previous rendering to the current one,
// or a note that nothing changed.
func Diff(prev, cur string) string {
	if prev == cur {
		return "(no changes since the last read)\n"
	}
	// difflib only fails when writing to its buffer, which cannot fail.
	out, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(prev),
		B:        difflib.SplitLines(cur),
		FromFile: "previous",
		ToFile:   "current",
		Context:  1,
	})
	return out
}
