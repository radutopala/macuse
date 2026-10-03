# macuse

macuse lets AI agents use the apps on your Mac: read a window's accessibility
tree or a screenshot, then click, type, press keys, scroll and drag. You
approve each app before an agent can touch it.

It is one binary with two roles:

- **`macuse serve`** runs on the Mac as a menu bar app. It holds the macOS
  Accessibility and Screen Recording grants, serves a REST API on
  `127.0.0.1:7710`, and shows approval requests in a popover under its menu
  bar icon.
- **`macuse mcp`** is an MCP server on stdio that calls that API. Run it on
  the Mac, or in a Linux container, where it reaches the Mac through
  `host.docker.internal`.

```
 agent ──stdio──> macuse mcp ──HTTP + token──> macuse serve ──> macOS apps
 (on the Mac or in a container)                (menu bar, on the Mac)
```

macuse runs on macOS 13 or later. The MCP client also ships for Linux,
for containers. Nothing runs on Windows.

## Install

With Homebrew:

```sh
brew install --cask radutopala/tap/macuse
```

The cask installs `macuse.app` and links its binary as the `macuse` CLI.
Or download `macuse_<version>_macos.dmg` from the
[releases](https://github.com/radutopala/macuse/releases), open it, and
drag `macuse.app` to `/Applications`. The app is signed and notarized, and
runs on Apple silicon and Intel Macs.

Then:

1. Open macuse. Its icon appears in the menu bar. Click the icon, choose
   **Grant**, and allow macuse under Accessibility and Screen Recording in
   System Settings.
2. Optional: tick **Open at login** in the popover, or run
   `macuse service install` to start it now and at every login.
3. If you didn't use Homebrew, click **Install CLI** in the popover to link
   `/usr/local/bin/macuse` to the app's binary. macOS asks for an
   administrator's password if you can't write there.

The CLI is the app's own binary, so it shares the app's privacy grants and
is updated with it.

### Updates

The app checks the latest release at start and every 30 minutes. When a
newer one is out, the popover offers **Install and restart**. The app
downloads the release, checks its SHA-256 against `checksums.txt`, checks
that it is signed by the same Developer ID team, then replaces itself and
starts again. Click the version in the popover to check now. Since the app
updates itself, `brew upgrade` skips it unless you pass `--greedy`.

## Connect an agent

### On the Mac

`macuse mcp` reads the API token from the Mac's token file, so it needs no
settings. For Claude Code:

```sh
claude mcp add macuse -- macuse mcp
```

Or, in any MCP client's config:

```json
{ "mcpServers": { "macuse": { "command": "macuse", "args": ["mcp"] } } }
```

### In a container

Put the Linux binary (`macuse_<version>_linux_<arch>.tar.gz`) in the image,
and give it the token. Run `macuse token` on the Mac to print it.

```json
{
  "mcpServers": {
    "macuse": {
      "command": "macuse",
      "args": ["mcp"],
      "env": { "MACUSE_TOKEN": "<the token>" }
    }
  }
}
```

Inside a container (`/.dockerenv` or `/run/.containerenv` exists), the client
calls `http://host.docker.internal:7710`. Docker Desktop routes that name to
the Mac's loopback, so `serve` doesn't need to listen on any other interface.
`MACUSE_URL` or `--url` overrides the address. `MACUSE_TOKEN_FILE` or
`--token-file` reads the token from a mounted file instead.

`macuse status` checks the connection from wherever it runs.

## Tools

| Tool        | What it does                                                     |
|-------------|------------------------------------------------------------------|
| `list_apps` | The running apps an agent may control                            |
| `start_app` | Launch an app by bundle id                                       |
| `get_state` | The focused window's element tree, a screenshot, or both          |
| `click`     | Click an element, or a point in the last screenshot              |
| `type`      | Type text into an element or the focused field                   |
| `press_key` | Press keys and shortcuts, such as `cmd+s`                        |
| `scroll`    | Scroll at an element or point                                    |
| `drag`      | Drag between two points                                          |
| `set_value` | Set an element's value directly                                  |

## Approvals and control

The first time an agent touches an app, its call waits and the popover opens
with the request. The request names the client, the app (bundle id and
signing team) and the action. The answers are:

- **Deny**: refuse this call.
- **This session**: allow the app for this agent session until it ends.
- **Always**: allow the app from now on, for this bundle id and signing team.
  A different team's build under the same bundle id asks again.

An unanswered request is refused after two minutes. The popover also lists
recent activity with a **Stop** button per session, **Pause all**, and the
apps you always allowed or denied, with **Forget** to remove one.

Some apps are never offered to agents, whatever you answer: terminals,
password managers, System Settings, Automator, Script Editor, Shortcuts, and
macuse itself. Add more in `deny_apps`.

Each allowed or refused call is appended to
`~/Library/Logs/macuse/audit.jsonl`. Each line records the client, the
session, the app and the action. For typing it records how many characters
were typed, not the text itself.

## Configuration

`~/Library/Application Support/macuse/config.json` is optional:

```json
{
  "listen": "127.0.0.1:7710",
  "deny_apps": ["com.example.SomeApp"],
  "approval_timeout_sec": 120
}
```

The same folder holds `token` (the API token, mode 0600) and
`approvals.json`. Delete `token` and restart macuse to rotate the token.

## Security

- The API listens on loopback only. Every `/v1` call needs the bearer token.
  The popover's routes use a separate key that is created at each start and
  is never written to disk.
- Anything that has the token can ask to control apps, but approvals still
  decide which apps it gets. Any local process, or any container on a Mac
  running Docker Desktop, can reach the port, so treat the token like a
  password.
- macuse asks macOS for Accessibility and Screen Recording, and nothing else.

## REST API

The MCP client is a thin layer over this API, which other tools can call
directly. Every call sends `Authorization: Bearer <token>`. Calls can also
send `X-Macuse-Client` (shown in prompts) and `X-Macuse-Session` (the unit
that **This session** and **Stop** apply to).

| Method | Path             | Body                                |
|--------|------------------|-------------------------------------|
| GET    | `/v1/status`     |                                     |
| GET    | `/v1/apps`       |                                     |
| POST   | `/v1/apps/start` | `{"bundle_id"}`                     |
| POST   | `/v1/state`      | `{"bundle_id", "capture", ...}`     |
| POST   | `/v1/action`     | `{"bundle_id", "action", ...}`      |

Errors are `{"error": {"code", "message"}}`, with codes such as `denied`,
`not_running`, `paused`, `stopped` and `unauthorized`.

## Build from source

```sh
make build           # bin/macuse for this machine
make app             # universal dist/macuse.app (unsigned)
make install         # signed app in /Applications, macuse linked into $GOPATH/bin
make restart         # install, then (re)start it as a login service
make uninstall       # stop it and remove the app and the link
make test            # tests
make coverage-check  # 100% coverage, in Docker
make lint            # golangci-lint for Linux and macOS, in Docker
```

macOS ties the privacy grants to the app's signature. A build you sign
yourself needs its own grants.

## License

Apache 2.0. See [LICENSE](LICENSE).

Not affiliated with Apple Inc. Mac and macOS are trademarks of Apple Inc.
