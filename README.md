# slsk-tui

A Linux Soulseek client with a keyboard-first terminal UI. It logs in directly to Soulseek—no slskd process or web dashboard required.

> **License and warranty:** Copyright © 2026 catgirl-systems contributors. This program is free software under the [GNU AGPL v3 only](LICENSE), comes with **no warranty**, and may be redistributed under that license. Source: <https://github.com/catgirl-systems/slsk-tui>.

## MVP features

- global file search;
- browse another user's public shares;
- resumable file and folder downloads;
- public local shares and passive uploads;
- foreground daemon operation for Docker and service managers;
- one Soulseek session: the TUI attaches to a running daemon or starts a transient child.

Chat, rooms, buddies, private shares, manual push uploads, bandwidth limiting, filesystem watching, and non-Linux platforms are intentionally not included.

## Build and run

Requires Go 1.25 or newer.

```sh
go build -o slsk-tui ./cmd/slsk-tui
./slsk-tui
```

The first launch asks for the Soulseek credentials, listening address, download path, and an optional `name:path` share. The password is masked and the JSON config is created with mode `0600`.

```sh
./slsk-tui daemon          # foreground; use systemd, tmux, or Docker to keep it running
./slsk-tui status
./slsk-tui status --json
```

The TUI uses an owner-only Unix socket. If no standalone daemon exists, it starts a child daemon whose lifetime is tied to the TUI. Exiting the TUI asks for confirmation when that would pause active transfers. An attached standalone daemon and its transfers continue running.

Soulseek permits one login per username. Keeping the session in the daemon prevents the TUI from kicking it off the network.

## TUI

| Key | Action |
| --- | --- |
| `tab` / `shift+tab` | Search, Browse, Transfers, Shares |
| arrows or `j` / `k` | move |
| `/` | edit search, username, or `name:path` share |
| `space` | select a file |
| `d` | download, cancel transfer, or remove share (by workspace) |
| `r` | retry transfer or rescan shares |
| `c` | clear completed transfer |
| `?` | help, source, and license notice |
| `q` | quit |

Set `NO_COLOR=1` to suppress terminal styling.

## Files and environment

Default locations follow XDG:

- config: `${XDG_CONFIG_HOME:-~/.config}/slsk-tui/config.json`;
- state and incomplete downloads: `${XDG_STATE_HOME:-~/.local/state}/slsk-tui/`;
- socket: `${XDG_RUNTIME_DIR:-/tmp/slsk-tui-$UID}/slsk-tui/slsk-tui.sock`;
- downloads: `~/Downloads/slsk-tui`.

`SLSK_TUI_USERNAME`, `SLSK_TUI_PASSWORD`, `SLSK_TUI_SERVER`, `SLSK_TUI_LISTEN_ADDR`, and `SLSK_TUI_DOWNLOAD_DIR` override JSON values. The daemon never returns or logs the password.

Incoming TCP port `50300` must be reachable for best peer connectivity. Direct connections are attempted first and server-mediated firewall piercing is used as fallback. The Soulseek protocol itself is not encrypted; do not treat usernames, searches, or transferred data as private.
