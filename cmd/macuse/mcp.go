package main

import (
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/radutopala/macuse/internal/auth"
	"github.com/radutopala/macuse/internal/buildinfo"
	"github.com/radutopala/macuse/internal/client"
	"github.com/radutopala/macuse/internal/mcpserver"
)

// Environment variables the client reads, for containers.
const (
	envURL       = "MACUSE_URL"
	envToken     = "MACUSE_TOKEN"
	envTokenFile = "MACUSE_TOKEN_FILE"
)

// clientFlags pick the API and its token.
type clientFlags struct {
	url, token, tokenFile string
}

func (f *clientFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.url, "url", "", "API base URL (env "+envURL+"; default "+client.LocalURL+", or "+client.ContainerURL+" in a container)")
	cmd.Flags().StringVar(&f.token, "token", "", "API token (env "+envToken+")")
	cmd.Flags().StringVar(&f.tokenFile, "token-file", "", "file holding the API token (env "+envTokenFile+"; default the Mac's token file)")
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// client resolves the flags, then the environment, then the defaults.
func (a *app) client(f clientFlags, name string) (*client.Client, error) {
	tok := first(f.token, a.getenv(envToken))
	if tok == "" {
		path := first(f.tokenFile, a.getenv(envTokenFile))
		if path == "" {
			p, err := a.paths()
			if err != nil {
				return nil, err
			}
			path = p.Token
		}
		var err error
		if tok, err = auth.Read(path); err != nil {
			return nil, fmt.Errorf("no API token: set %s (run `macuse token` on the Mac) or %s: %w", envToken, envTokenFile, err)
		}
	}
	session, err := a.tokens.New()
	if err != nil {
		return nil, err
	}
	return &client.Client{
		BaseURL: first(f.url, a.getenv(envURL), client.DefaultURL(a.exists)),
		Token:   tok,
		Name:    name,
		Session: session[:12],
		HTTP:    a.http,
	}, nil
}

func (a *app) mcpCommand() *cobra.Command {
	var f clientFlags
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the macuse tools over MCP on stdio, calling the API",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client(f, "mcp client")
			if err != nil {
				return err
			}
			// stdout carries the protocol; logs go to stderr.
			logger := slog.New(slog.NewTextHandler(a.stderr, nil))
			return mcpserver.New(c, buildinfo.Version, logger).Run(cmd.Context(), a.stdio)
		},
	}
	f.register(cmd)
	return cmd
}

func (a *app) statusCommand() *cobra.Command {
	var f clientFlags
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Check that the API answers, and show its state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client(f, "macuse status")
			if err != nil {
				return err
			}
			st, err := c.Status(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintf(a.stdout, "macuse %s at %s\n", st.Version, c.BaseURL)
			if st.Paused {
				fmt.Fprintln(a.stdout, "paused: agents can't use the Mac until you resume")
			}
			if p := st.Permissions; p != nil {
				fmt.Fprintf(a.stdout, "accessibility: %s\nscreen recording: %s\n", granted(p.Accessibility), granted(p.ScreenRecording))
			}
			return nil
		},
	}
	f.register(cmd)
	return cmd
}

func granted(ok bool) string {
	if ok {
		return "granted"
	}
	return "missing; grant it from the macuse menu bar"
}
