// Package client calls the macuse HTTP API, from the Mac itself or from a
// container on it.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/radutopala/macuse/internal/proto"
)

// Base URLs of the API: on the Mac, and from a container, where the
// container runtime routes host.docker.internal to the Mac's loopback.
const (
	LocalURL     = "http://127.0.0.1:7710"
	ContainerURL = "http://host.docker.internal:7710"
)

// containerMarkers are files container runtimes create in every container.
var containerMarkers = []string{"/.dockerenv", "/run/.containerenv"}

// DefaultURL is ContainerURL inside a container and LocalURL elsewhere.
// exists reports whether a file exists.
func DefaultURL(exists func(path string) bool) string {
	for _, m := range containerMarkers {
		if exists(m) {
			return ContainerURL
		}
	}
	return LocalURL
}

type nameKey struct{}

// WithName names the client for the calls made with ctx, overriding
// Client.Name; an MCP server learns its client's name per session.
func WithName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, nameKey{}, name)
}

// NameFrom returns the name WithName set, or "".
func NameFrom(ctx context.Context) string {
	name, _ := ctx.Value(nameKey{}).(string)
	return name
}

// Client calls the API as one named client session.
type Client struct {
	BaseURL string
	Token   string
	// Name and Session tell the user who asks, in the approval prompt.
	Name    string
	Session string
	HTTP    *http.Client
}

// Status reports the server's version, pause state and grants.
func (c *Client) Status(ctx context.Context) (proto.Status, error) {
	var st proto.Status
	err := c.do(ctx, http.MethodGet, "/v1/status", nil, &st)
	return st, err
}

// ListApps lists the running apps agents may control.
func (c *Client) ListApps(ctx context.Context) ([]proto.App, error) {
	var list proto.AppList
	err := c.do(ctx, http.MethodGet, "/v1/apps", nil, &list)
	return list.Apps, err
}

// StartApp launches or activates the app.
func (c *Client) StartApp(ctx context.Context, bundleID string) (proto.App, error) {
	var app proto.App
	err := c.do(ctx, http.MethodPost, "/v1/apps/start", proto.StartAppParams{BundleID: bundleID}, &app)
	return app, err
}

// GetState reads the app's focused window.
func (c *Client) GetState(ctx context.Context, p proto.GetStateParams) (proto.State, error) {
	var st proto.State
	err := c.do(ctx, http.MethodPost, "/v1/state", p, &st)
	return st, err
}

// Action performs one input action.
func (c *Client) Action(ctx context.Context, p proto.ActionParams) (proto.ActionResult, error) {
	var res proto.ActionResult
	err := c.do(ctx, http.MethodPost, "/v1/action", p, &res)
	return res, err
}

// Batch performs several input actions on one app, in order.
func (c *Client) Batch(ctx context.Context, p proto.BatchParams) (proto.ActionResult, error) {
	var res proto.ActionResult
	err := c.do(ctx, http.MethodPost, "/v1/batch", p, &res)
	return res, err
}

// do sends one call. Failures come back as *proto.Error, with the server's
// message when it answered.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, _ := json.Marshal(in)
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
	if err != nil {
		return &proto.Error{Code: proto.CodeInvalidParams, Message: "bad macuse URL: " + err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	name := c.Name
	if n := NameFrom(ctx); n != "" {
		name = n
	}
	req.Header.Set(proto.HeaderClient, name)
	req.Header.Set(proto.HeaderSession, c.Session)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		hint := "check it's running on the Mac and listens on that port"
		if !strings.Contains(c.BaseURL, "host.docker.internal") {
			hint += " (from a container, use " + ContainerURL + ")"
		}
		return &proto.Error{Code: proto.CodeHelperUnavailable, Message: fmt.Sprintf(
			"can't reach macuse at %s (%v); %s", c.BaseURL, err, hint)}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return &proto.Error{Code: proto.CodeHelperUnavailable, Message: "reading the macuse answer failed: " + err.Error()}
	}
	if resp.StatusCode >= 300 {
		var eb proto.ErrorBody
		if json.Unmarshal(data, &eb) == nil && eb.Error != nil {
			return eb.Error
		}
		return &proto.Error{Code: proto.CodeInternal, Message: fmt.Sprintf("macuse answered %s: %s", resp.Status, strings.TrimSpace(string(data)))}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &proto.Error{Code: proto.CodeInternal, Message: "bad macuse answer: " + err.Error()}
	}
	return nil
}
