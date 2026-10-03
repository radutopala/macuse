package main

import "runtime"

// AppKit and the run loop the native layer pumps belong to the main thread;
// keep the main goroutine on it.
func init() {
	runtime.LockOSThread()
}
