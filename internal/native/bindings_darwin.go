package native

import (
	"fmt"
	"unsafe"

	"github.com/ebitengine/purego"
)

// CoreGraphics geometry, laid out as in C (all fields are CGFloat = double).
type cgPoint struct{ X, Y float64 }
type cgSize struct{ W, H float64 }
type cgRect struct {
	Origin cgPoint
	Size   cgSize
}

// cfRange is a CFRange: a location and length in UTF-16 units.
type cfRange struct{ Location, Length int64 }

const (
	kCFStringEncodingUTF8 = 0x08000100
	kCFNumberDoubleType   = 13

	kAXValueCGPointType = 1
	kAXValueCGSizeType  = 2
	kAXValueCFRangeType = 4

	kAXErrorSuccess           = 0
	kAXErrorIllegalArgument   = -25201
	kAXErrorInvalidElement    = -25202
	kAXErrorAPIDisabled       = -25211
	kAXErrorNoValue           = -25212
	kAXErrorAttrUnsupported   = -25205
	kAXErrorCannotComplete    = -25204
	kAXErrorActionUnsupported = -25206

	kCGEventLeftMouseDown    = 1
	kCGEventLeftMouseUp      = 2
	kCGEventRightMouseDown   = 3
	kCGEventRightMouseUp     = 4
	kCGEventMouseMoved       = 5
	kCGEventLeftMouseDragged = 6
	kCGMouseButtonLeft       = 0
	kCGMouseButtonRight      = 1
	kCGMouseEventClickState  = 1
	kCGHIDEventTap           = 0
	kCGScrollEventUnitLine   = 1

	kCGEventFlagMaskShift     = 0x20000
	kCGEventFlagMaskControl   = 0x40000
	kCGEventFlagMaskAlternate = 0x80000
	kCGEventFlagMaskCommand   = 0x100000

	kCGWindowListOptionOnScreenOnly    = 1
	kCGWindowListOptionIncludingWindow = 8
	kCGNullWindowID                    = 0
	kCGWindowImageDefault              = 0

	kCGEventSourceStateHIDSystemState = 1
	kCGAnyInputEventType              = 0xFFFFFFFF

	kCGImageAlphaPremultipliedLast = 1
	kCGBitmapByteOrder32Big        = 4 << 12

	kSecCSSigningInformation = 1 << 1
)

const (
	frameworkCF       = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	frameworkAS       = "/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices"
	frameworkCG       = "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics"
	frameworkSecurity = "/System/Library/Frameworks/Security.framework/Security"
	frameworkAppKit   = "/System/Library/Frameworks/AppKit.framework/AppKit"
	frameworkWebKit   = "/System/Library/Frameworks/WebKit.framework/WebKit"
)

// lib holds the C entry points the platform calls.
type lib struct {
	// CoreFoundation
	CFRelease                         func(uintptr)
	CFRetain                          func(uintptr) uintptr
	CFGetTypeID                       func(uintptr) uint64
	CFStringGetTypeID                 func() uint64
	CFArrayGetTypeID                  func() uint64
	CFBooleanGetTypeID                func() uint64
	CFNumberGetTypeID                 func() uint64
	CFStringCreateWithCString         func(alloc uintptr, s string, enc uint32) uintptr
	CFStringGetLength                 func(uintptr) int64
	CFStringGetMaximumSizeForEncoding func(n int64, enc uint32) int64
	CFStringGetCString                func(s uintptr, buf *byte, size int64, enc uint32) bool
	CFArrayGetCount                   func(uintptr) int64
	CFArrayGetValueAtIndex            func(uintptr, int64) uintptr
	CFBooleanGetValue                 func(uintptr) bool
	CFNumberGetValue                  func(n uintptr, typ int64, out *float64) bool
	CFNumberCreate                    func(alloc uintptr, typ int64, v *float64) uintptr
	CFDictionaryCreate                func(alloc uintptr, keys, values *uintptr, n int64, keyCB, valueCB uintptr) uintptr
	CFDictionaryGetValue              func(dict, key uintptr) uintptr
	CFRunLoopRunInMode                func(mode uintptr, seconds float64, returnAfterSource bool) int32

	// Accessibility (HIServices, re-exported by ApplicationServices)
	AXIsProcessTrusted             func() bool
	AXIsProcessTrustedWithOptions  func(options uintptr) bool
	AXUIElementCreateApplication   func(pid int32) uintptr
	AXUIElementCreateSystemWide    func() uintptr
	AXUIElementCopyAttributeValue  func(el, attr uintptr, out *uintptr) int32
	AXUIElementCopyActionNames     func(el uintptr, out *uintptr) int32
	AXUIElementPerformAction       func(el, action uintptr) int32
	AXUIElementSetAttributeValue   func(el, attr, value uintptr) int32
	AXUIElementSetMessagingTimeout func(el uintptr, seconds float32) int32
	AXUIElementIsAttributeSettable func(el, attr uintptr, out *bool) int32
	// axUIElementGetWindow is private API, nil when this macOS lacks it.
	axUIElementGetWindow func(el uintptr, out *uint32) int32
	AXValueGetTypeID     func() uint64
	axValueCreateRange   func(typ uint32, v *cfRange) uintptr
	axValueGetPoint      func(v uintptr, typ uint32, out *cgPoint) bool
	axValueGetSize       func(v uintptr, typ uint32, out *cgSize) bool

	// CoreGraphics
	CGEventCreateMouseEvent                func(src uintptr, typ uint32, at cgPoint, button uint32) uintptr
	CGEventCreateKeyboardEvent             func(src uintptr, key uint16, down bool) uintptr
	CGEventCreateScrollWheelEvent2         func(src uintptr, units, count uint32, w1, w2, w3 int32) uintptr
	CGEventSetFlags                        func(ev uintptr, flags uint64)
	CGEventSetLocation                     func(ev uintptr, at cgPoint)
	CGEventSetIntegerValueField            func(ev uintptr, field uint32, v int64)
	CGEventKeyboardSetUnicodeString        func(ev uintptr, n uint64, s *uint16)
	CGEventPost                            func(tap uint32, ev uintptr)
	CGEventPostToPid                       func(pid int32, ev uintptr)
	CGEventCreate                          func(src uintptr) uintptr
	CGEventGetLocation                     func(ev uintptr) cgPoint
	CGWarpMouseCursorPosition              func(at cgPoint) int32
	CGEventSourceSecondsSinceLastEventType func(state int32, typ uint32) float64
	CGPreflightScreenCaptureAccess         func() bool
	CGRequestScreenCaptureAccess           func() bool
	// CGWindowListCreateImage is nil when this macOS no longer exports it.
	CGWindowListCreateImage     func(r cgRect, opts, windowID, imageOpts uint32) uintptr
	CGImageGetWidth             func(uintptr) uint64
	CGImageGetHeight            func(uintptr) uint64
	CGImageRelease              func(uintptr)
	CGColorSpaceCreateDeviceRGB func() uintptr
	CGColorSpaceRelease         func(uintptr)
	CGBitmapContextCreate       func(data *byte, w, h, bitsPerComponent, bytesPerRow uint64, cs uintptr, info uint32) uintptr
	CGContextDrawImage          func(ctx uintptr, r cgRect, img uintptr)
	CGContextRelease            func(uintptr)

	// Security
	SecStaticCodeCreateWithPath   func(url uintptr, flags uint32, out *uintptr) int32
	SecCodeCopySigningInformation func(code uintptr, flags uint32, out *uintptr) int32

	// Constants exported as variables.
	kCFBooleanTrue                  uintptr
	kCFTypeDictionaryKeyCallBacks   uintptr // address of the struct
	kCFTypeDictionaryValueCallBacks uintptr // address of the struct
	kCFRunLoopDefaultMode           uintptr
	kAXTrustedCheckOptionPrompt     uintptr
	kSecCodeInfoTeamIdentifier      uintptr
}

// loadLib opens the frameworks and binds every function. RegisterLibFunc
// panics on a missing symbol; that is recovered into an error.
func loadLib() (l *lib, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("bind macOS frameworks: %v", r)
		}
	}()
	open := func(path string) uintptr {
		h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			panic(fmt.Sprintf("dlopen %s: %v", path, err))
		}
		return h
	}
	cf := open(frameworkCF)
	as := open(frameworkAS)
	cg := open(frameworkCG)
	sec := open(frameworkSecurity)
	// AppKit registers NSWorkspace and the menu bar classes with the
	// Objective-C runtime, WebKit the popover's web view.
	open(frameworkAppKit)
	open(frameworkWebKit)

	l = &lib{}
	bind := func(fptr any, h uintptr, name string) { purego.RegisterLibFunc(fptr, h, name) }

	bind(&l.CFRelease, cf, "CFRelease")
	bind(&l.CFRetain, cf, "CFRetain")
	bind(&l.CFGetTypeID, cf, "CFGetTypeID")
	bind(&l.CFStringGetTypeID, cf, "CFStringGetTypeID")
	bind(&l.CFArrayGetTypeID, cf, "CFArrayGetTypeID")
	bind(&l.CFBooleanGetTypeID, cf, "CFBooleanGetTypeID")
	bind(&l.CFNumberGetTypeID, cf, "CFNumberGetTypeID")
	bind(&l.CFStringCreateWithCString, cf, "CFStringCreateWithCString")
	bind(&l.CFStringGetLength, cf, "CFStringGetLength")
	bind(&l.CFStringGetMaximumSizeForEncoding, cf, "CFStringGetMaximumSizeForEncoding")
	bind(&l.CFStringGetCString, cf, "CFStringGetCString")
	bind(&l.CFArrayGetCount, cf, "CFArrayGetCount")
	bind(&l.CFArrayGetValueAtIndex, cf, "CFArrayGetValueAtIndex")
	bind(&l.CFBooleanGetValue, cf, "CFBooleanGetValue")
	bind(&l.CFNumberGetValue, cf, "CFNumberGetValue")
	bind(&l.CFNumberCreate, cf, "CFNumberCreate")
	bind(&l.CFDictionaryCreate, cf, "CFDictionaryCreate")
	bind(&l.CFDictionaryGetValue, cf, "CFDictionaryGetValue")
	bind(&l.CFRunLoopRunInMode, cf, "CFRunLoopRunInMode")

	bind(&l.AXIsProcessTrusted, as, "AXIsProcessTrusted")
	bind(&l.AXIsProcessTrustedWithOptions, as, "AXIsProcessTrustedWithOptions")
	bind(&l.AXUIElementCreateApplication, as, "AXUIElementCreateApplication")
	bind(&l.AXUIElementCreateSystemWide, as, "AXUIElementCreateSystemWide")
	bind(&l.AXUIElementCopyAttributeValue, as, "AXUIElementCopyAttributeValue")
	bind(&l.AXUIElementCopyActionNames, as, "AXUIElementCopyActionNames")
	bind(&l.AXUIElementPerformAction, as, "AXUIElementPerformAction")
	bind(&l.AXUIElementSetAttributeValue, as, "AXUIElementSetAttributeValue")
	bind(&l.AXUIElementSetMessagingTimeout, as, "AXUIElementSetMessagingTimeout")
	bind(&l.AXUIElementIsAttributeSettable, as, "AXUIElementIsAttributeSettable")
	if sym, err := purego.Dlsym(as, "_AXUIElementGetWindow"); err == nil {
		purego.RegisterFunc(&l.axUIElementGetWindow, sym)
	}
	bind(&l.AXValueGetTypeID, as, "AXValueGetTypeID")
	bind(&l.axValueCreateRange, as, "AXValueCreate")
	bind(&l.axValueGetPoint, as, "AXValueGetValue")
	bind(&l.axValueGetSize, as, "AXValueGetValue")

	bind(&l.CGEventCreateMouseEvent, cg, "CGEventCreateMouseEvent")
	bind(&l.CGEventCreateKeyboardEvent, cg, "CGEventCreateKeyboardEvent")
	bind(&l.CGEventCreateScrollWheelEvent2, cg, "CGEventCreateScrollWheelEvent2")
	bind(&l.CGEventSetFlags, cg, "CGEventSetFlags")
	bind(&l.CGEventSetLocation, cg, "CGEventSetLocation")
	bind(&l.CGEventSetIntegerValueField, cg, "CGEventSetIntegerValueField")
	bind(&l.CGEventKeyboardSetUnicodeString, cg, "CGEventKeyboardSetUnicodeString")
	bind(&l.CGEventPost, cg, "CGEventPost")
	bind(&l.CGEventPostToPid, cg, "CGEventPostToPid")
	bind(&l.CGEventCreate, cg, "CGEventCreate")
	bind(&l.CGEventGetLocation, cg, "CGEventGetLocation")
	bind(&l.CGWarpMouseCursorPosition, cg, "CGWarpMouseCursorPosition")
	bind(&l.CGEventSourceSecondsSinceLastEventType, cg, "CGEventSourceSecondsSinceLastEventType")
	bind(&l.CGPreflightScreenCaptureAccess, cg, "CGPreflightScreenCaptureAccess")
	bind(&l.CGRequestScreenCaptureAccess, cg, "CGRequestScreenCaptureAccess")
	if sym, err := purego.Dlsym(cg, "CGWindowListCreateImage"); err == nil {
		purego.RegisterFunc(&l.CGWindowListCreateImage, sym)
	}
	bind(&l.CGImageGetWidth, cg, "CGImageGetWidth")
	bind(&l.CGImageGetHeight, cg, "CGImageGetHeight")
	bind(&l.CGImageRelease, cg, "CGImageRelease")
	bind(&l.CGColorSpaceCreateDeviceRGB, cg, "CGColorSpaceCreateDeviceRGB")
	bind(&l.CGColorSpaceRelease, cg, "CGColorSpaceRelease")
	bind(&l.CGBitmapContextCreate, cg, "CGBitmapContextCreate")
	bind(&l.CGContextDrawImage, cg, "CGContextDrawImage")
	bind(&l.CGContextRelease, cg, "CGContextRelease")

	bind(&l.SecStaticCodeCreateWithPath, sec, "SecStaticCodeCreateWithPath")
	bind(&l.SecCodeCopySigningInformation, sec, "SecCodeCopySigningInformation")

	l.kCFBooleanTrue = loadVar(cf, "kCFBooleanTrue")
	l.kCFTypeDictionaryKeyCallBacks = symbol(cf, "kCFTypeDictionaryKeyCallBacks")
	l.kCFTypeDictionaryValueCallBacks = symbol(cf, "kCFTypeDictionaryValueCallBacks")
	l.kCFRunLoopDefaultMode = loadVar(cf, "kCFRunLoopDefaultMode")
	l.kAXTrustedCheckOptionPrompt = loadVar(as, "kAXTrustedCheckOptionPrompt")
	l.kSecCodeInfoTeamIdentifier = loadVar(sec, "kSecCodeInfoTeamIdentifier")
	return l, nil
}

func symbol(h uintptr, name string) uintptr {
	sym, err := purego.Dlsym(h, name)
	if err != nil {
		panic(fmt.Sprintf("dlsym %s: %v", name, err))
	}
	return sym
}

// loadVar reads a pointer-sized exported variable such as a CFStringRef
// constant.
func loadVar(h uintptr, name string) uintptr {
	addr := symbol(h, name)
	return **(**uintptr)(unsafe.Pointer(&addr))
}

// cfString creates a CFString; the caller releases it.
func (l *lib) cfString(s string) uintptr {
	return l.CFStringCreateWithCString(0, s, kCFStringEncodingUTF8)
}

// goString copies a CFString (or toll-free bridged NSString) into Go.
func (l *lib) goString(ref uintptr) string {
	if ref == 0 {
		return ""
	}
	size := l.CFStringGetMaximumSizeForEncoding(l.CFStringGetLength(ref), kCFStringEncodingUTF8) + 1
	buf := make([]byte, size)
	if !l.CFStringGetCString(ref, &buf[0], size, kCFStringEncodingUTF8) {
		return ""
	}
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i])
		}
	}
	return string(buf)
}
