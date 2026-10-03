// Command mac-use lets AI agents drive macOS apps, with the user's
// approval. "mac-use serve" is the menu bar app and its REST API; "mac-use
// mcp" is an MCP server, on the Mac or in a container, that calls the API.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// main is all that's left untested: the process's signals, args and exit.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := newApp().run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}
