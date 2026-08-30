# oto

A Linux Soulseek client with a keyboard-first terminal UI. It logs in directly to Soulseek—no slskd process or web dashboard required.

## Build and run

Requires Go 1.25 or newer.

```sh
go build -o oto ./cmd/oto
./oto
```

The first launch asks for the Soulseek credentials, listening address, download path, and an optional `name:path` share. The password is masked and the JSON config is created with mode `0600`.

```sh
./oto daemon          # foreground; use systemd, tmux, or Docker to keep it running
./oto status
./oto status --json
```

The TUI uses an owner-only Unix socket. If no standalone daemon exists, it starts a child daemon whose lifetime is tied to the TUI. Exiting the TUI asks for confirmation when that would pause active transfers. An attached standalone daemon and its transfers continue running.

Soulseek permits one login per username. Keeping the session in the daemon prevents the TUI from kicking it off the network.

## TUI

| Key | Action |
| --- | --- |
| `tab` / `shift+tab` | Search, Browse, Transfers, Shares, Settings |
| up/down or `j` / `k` | move through visible rows |
| left/right | collapse/expand a tree node; switch Settings sections |
| `home` / `end` | jump to the start/end of any text field |
| `ctrl+left` / `ctrl+right` | move by word in any text field (`alt` also works) |
| `ctrl+backspace` / `ctrl+delete` | delete by word (`ctrl+w`, `alt+backspace`, and `alt+delete` also work) |
| `ctrl+a` / `ctrl+e`, `ctrl+u` / `ctrl+k` | jump to start/end; delete before/after the caret |
| `/` | edit search, username, `name:path` share, or setting |
| `enter` | toggle a folder; download a Search/Browse file |
| `f` | edit cached search filters |
| `tab` / `shift+tab` while filtering | complete fields, types, booleans, and comparison operators |
| `c` | clear/restore search filters or clear the selected transfer subtree |
| `space` | select a file or every loaded file below a user/folder node |
| `b` in Search | browse the selected result's user and jump to its folder |
| `ctrl+page up` / `ctrl+page down` in Search, Browse, or Transfers | switch result tabs or Downloads/Uploads |
| `ctrl+w` in Search or Browse | close the active result tab |
| `d` | download the selected/cursor subtree, cancel a transfer subtree, or remove a share root |
| `r` | refresh a user browse, retry a transfer subtree, or rescan shares |
| `s` | save settings and reconnect |
| `?` | keyboard guide |
| `q` | quit |

Set `NO_COLOR=1` to suppress terminal styling.

Search groups results as user → folders → files; Browse shows remote folders; Transfers groups each direction as user → folders → files; Shares lazily shows the daemon's scanned public-share index. Expansion and selection are session-local, and recursive Search/Browse actions include only currently loaded files.

Searches support quoted phrases, excluded words (`-remix`), and partial terms (`*radio`). Filter cached results without another network search using fields such as:

```text
in:"live|radio session" out:remix type:audio,!mp3 size:>=20MiB bitrate:>=320 duration:>2:00 free:true public:true
```

`in` and `out` are case-insensitive regular expressions. `type` accepts extensions or `audio`, `video`, `image`, `document`, `text`, `archive`, and `executable`. Size units may be binary (`MiB`) or decimal (`MB`); duration accepts seconds, `MM:SS`, or `HH:MM:SS`. Repeat numeric fields to form ranges. Comparisons support `<`, `<=`, `=`, `==`, `!=`, `>=`, and `>`. While editing filters, use `tab` and `shift+tab` to complete or cycle fields and special values.

## Files and environment

Default locations follow XDG:

- config: `${XDG_CONFIG_HOME:-~/.config}/oto/config.json`;
- state and incomplete downloads: `${XDG_STATE_HOME:-~/.local/state}/oto/`;
- socket: `${XDG_RUNTIME_DIR:-/tmp/oto-$UID}/oto/oto.sock`;
- downloads: `~/Downloads/oto`.

`OTO_USERNAME`, `OTO_PASSWORD`, `OTO_SERVER`, `OTO_LISTEN_ADDR`, and `OTO_DOWNLOAD_DIR` override JSON values. The daemon never returns or logs the password.

Incoming TCP port `50300` must be reachable for best peer connectivity. Direct connections are attempted first and server-mediated firewall piercing is used as fallback. The Soulseek protocol itself is not encrypted; do not treat usernames, searches, or transferred data as private.
