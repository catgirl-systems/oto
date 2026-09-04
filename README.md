# oto

A Linux Soulseek client with a keyboard-first terminal UI. It logs in directly to Soulseek—no slskd process or web dashboard required.

## Build and run

Requires Go 1.25 or newer.

```sh
go build -o oto ./cmd/oto
./oto
```

The first launch asks for the Soulseek credentials, listening address, optional network interface, download path, and an optional `name:path` share. Leave the interface blank for automatic OS routing, or enter a device such as `wg0`; currently-down VPN interfaces may be configured. The password is masked and the JSON config is created with mode `0600`.

```sh
./oto daemon          # foreground; use systemd, tmux, or Docker to keep it running
./oto daemon --share-rescan-delay 30s  # default 5m; use 0 to disable watching
./oto daemon --listen-port-file /run/oto/forwarded-port --listen-port-reconcile-interval 30s
./oto status
./oto status --json
./oto transfers
./oto transfers --json
./oto pause DOWNLOAD_ID
./oto resume DOWNLOAD_ID
./oto rescan
```

## Docker

Stable releases publish a multi-architecture image for amd64 and arm64 at `ghcr.io/catgirl-systems/oto`.

```yaml
services:
  oto:
    image: ghcr.io/catgirl-systems/oto:latest
    container_name: oto
    environment:
      PUID: "1000"
      PGID: "1000"
      TZ: Etc/UTC
      UMASK: "022"
      OTO_USERNAME: your-soulseek-username
      OTO_PASSWORD: your-soulseek-password
      OTO_DOWNLOAD_DIR: /downloads
    volumes:
      - ./oto-config:/config
      - ./downloads:/downloads
      - ./music:/shares/music:ro
    ports:
      - "50300:50300"
    restart: unless-stopped
```

The equivalent Docker CLI command is:

```sh
docker run -d \
  --name oto \
  -e PUID=1000 -e PGID=1000 -e TZ=Etc/UTC -e UMASK=022 \
  -e OTO_USERNAME=your-soulseek-username \
  -e OTO_PASSWORD=your-soulseek-password \
  -e OTO_DOWNLOAD_DIR=/downloads \
  -v "$PWD/oto-config:/config" \
  -v "$PWD/downloads:/downloads" \
  -v "$PWD/music:/shares/music:ro" \
  -p 50300:50300 \
  --restart unless-stopped \
  ghcr.io/catgirl-systems/oto:latest
```

The image runs `oto daemon` as the LinuxServer `abc` user. `PUID` and `PGID` should match the owner of the mounted directories. On first start it copies the example to `/config/config.json`; edit that file for shares or other settings, or use the existing `OTO_*` overrides. Configuration and daemon state persist under `/config`.

Release downloads include `oto-linux-amd64`, `oto-linux-arm64`, and `SHA256SUMS`. Verify them from the same directory with:

```sh
sha256sum -c SHA256SUMS
```

The TUI uses an owner-only Unix socket. If no standalone daemon exists, it starts a child daemon whose lifetime is tied to the TUI. Exiting the TUI asks for confirmation when that would pause active transfers. An attached standalone daemon and its transfers continue running.

Soulseek permits one login per username. Keeping the session in the daemon prevents the TUI from kicking it off the network.

Press `o` to choose Online, Away, or Offline without stopping the daemon. Offline stops reconnect attempts and requeues active downloads without deleting partial data; choosing Online or Away resumes them. Away survives automatic reconnects for the current daemon run.

The daemon watches every non-hidden, non-symlink directory under each share root using `fsnotify` (one recursive watch per directory). After filesystem changes stop for five minutes by default, it builds a complete shadow index and atomically publishes it; a fixed 30-minute maximum delay prevents continuous activity from postponing scans forever. Events during a scan discard that result and schedule another scan. Startup and manual rescans remain immediate, and watcher or scan failures keep the last good index available. `/v1/state` reports the current share scan (`scanning`, `publishing`, `completed`, `failed`, `cancelled`, or `discarded`) with root, accepted file/directory counts, and timing; the Shares view displays the same live progress.

For dynamically forwarded incoming ports, `--listen-port-file` watches the file's parent directory and hot-swaps the listener whenever the file contains a new port. `--listen-port-reconcile-interval` provides a periodic fallback (default `30s`; `0` disables it). Missing or empty files mark the port unavailable until a valid value appears. This interface is provider-neutral; for example, a VPN container can write its current forwarded port into a shared volume. When configured, it takes precedence over and skips automatic NAT-PMP/UPnP forwarding without changing the saved settings.

The headless controls attach only to the daemon's XDG Unix socket; they never launch a daemon or log in themselves. `transfers` lists both directions with full stable IDs, quoted text fields, or `--json` output. `pause ID` and `resume ID` operate on one download and preserve the TUI's partial-file and finalization protections. `rescan` works offline and waits for index publication without the ordinary request timeout. A concurrent manual scan fails with “share scan already in progress” (HTTP 409). Ctrl+C stops waiting, not the daemon-owned scan. Invalid commands, a missing daemon, and service errors exit nonzero.

## TUI

| Key | Action |
| --- | --- |
| `tab` / `shift+tab` | Search, Wishlist, Browse, Transfers, Shares, Settings |
| up/down or `j` / `k` | move through visible rows; up/down recalls search or filter history while editing |
| `page up` / `page down` | move through visible rows by one screen |
| left/right | collapse/expand a tree node; switch Settings sections; cycle an active choice |
| `home` / `end` | jump to the first/last row, or start/end of a text field while editing |
| `ctrl+left` / `ctrl+right` | move by word in any text field (`alt` also works) |
| `ctrl+backspace` / `ctrl+delete` | delete by word (`ctrl+w`, `alt+backspace`, and `alt+delete` also work) |
| `ctrl+a` / `ctrl+e`, `ctrl+u` / `ctrl+k` | jump to start/end; delete before/after the caret |
| `/` | edit search, add a wishlist item, enter a username or `name:path` share, or edit a setting |
| `enter` | open wishlist or saved Browse results; toggle a folder; download a Search/Browse file |
| `f` | edit cached Search filters, a wishlist item's stored filter, or find within a loaded Browse list |
| `w` in Search | save or update the active query and filter as a wishlist item |
| `tab` / `shift+tab` while filtering | complete fields, types, booleans, and comparison operators |
| `c` | clear/restore search filters or clear the selected transfer subtree |
| `space` | select a file or every loaded file below a user/folder node |
| `b` in Search | browse the selected result's user and jump to its folder |
| `i` in Search or Browse | show the selected file's properties and media metadata |
| `ctrl+page up` / `ctrl+page down` in Search, Browse, or Transfers | switch result tabs or Downloads/Uploads |
| `ctrl+w` in Search or Browse | close the active result tab |
| `d` | download selected files; choose a folder download mode and destination; remove a wishlist/share item; or cancel a transfer subtree |
| `r` | rerun a wishlist item, refresh a user browse or saved-user list, resume/retry a transfer subtree, or rescan shares |
| `p` in Downloads | pause the selected transfer subtree without deleting partial data |
| `o` | choose Online, Away, or Offline without quitting |
| `s` in Browse or Settings | explicitly save the active remote share list, or save Settings |
| `s` / `S` in Transfers | prepare a file/folder-name search / containing-folder search; Enter submits |
| `?` | keyboard guide |
| `q` | quit |

Set `NO_COLOR=1` to suppress terminal styling.

Search groups results as user → folders → files; Browse shows remote folders; Transfers groups each direction as user → folders → files; Shares lazily shows the daemon's scanned public-share index. Expansion and selection are session-local. Selection-based Search/Browse actions include only currently loaded files; using `d` on a folder can request that complete remote folder recursively without fetching the user's full share list. In the folder download dialog, press `/` to override the configured download root for that folder; the user and remote folder hierarchy is preserved below it.

Pending network searches and remote share-list requests temporarily replace the bottom keyboard hints with an activity bar. Share-list requests switch to byte and percentage progress once the peer's response size is known; the normal footer returns immediately when the operation finishes.

Press `s` in a loaded Browse tab to save that user's complete share list. With no Browse tabs open, saved users are listed for selection with the arrow keys and Enter. Opening remains network-first while connected, but falls back to the saved list when offline or when the peer cannot be reached; cached tabs are labeled `(cached)`, and `r` retries the live list. Press `f` in a loaded Browse tab to filter its files and folders locally by a case-insensitive path substring; enter an empty find to restore the complete tree. Finds are kept separately per open tab and work with live or saved lists without contacting the peer.

Searches support quoted phrases, excluded words (`-remix`), and partial terms (`*radio`). Filter cached results without another network search using fields such as:

```text
in:"live|radio session" out:remix type:audio,!mp3 size:>=20MiB bitrate:>=320 duration:>2:00 free:true public:true country:US,CA
```

`in` and `out` are case-insensitive regular expressions. `type` accepts extensions or `audio`, `video`, `image`, `document`, `text`, `archive`, and `executable`. Size units may be binary (`MiB`) or decimal (`MB`); duration accepts seconds, `MM:SS`, or `HH:MM:SS`. Repeat numeric fields to form ranges. Comparisons support `<`, `<=`, `=`, `==`, `!=`, `>=`, and `>`. While editing filters, use `tab` and `shift+tab` to complete or cycle fields and special values.

`country` accepts case-insensitive, comma-separated two-letter codes. Positive codes are alternatives (`country:US,CA`); prefix exclusions with `!` (`country:!GB,!DE`). Unknown locations match exclusion-only filters but not positive codes.
Search queries and complete filter expressions are kept as separate most-recent-first histories. Press up/down while editing to recall entries. The Settings → Search section independently enables each history, sets its retention limit (`0` means unlimited), and clears it immediately.

**Settings → Search → Default result filter** (`search.default_filter`, empty by default) initializes every new ordinary search with the last successfully saved expression. Existing result tabs keep their filters; unsaved Settings edits do not apply. A filter explicitly entered in an empty Search workspace overrides the default for the next search only. Wishlist filters remain independent, including an empty stored filter; `w` saves the current tab's actual filter. Explicit empty IPC filters still mean unfiltered.

In either transfer direction, `s` fills the Search editor from the focused file (minus its final extension) or folder name. `S` uses a file's containing folder instead. Enter starts the search; Escape cancels the draft without creating a result tab or contacting the network. User-only rows do not select an arbitrary descendant.

Settings → Search also controls incoming search responses with `search.respond_to_incoming_searches`, `search.minimum_incoming_search_length` (`0` means no minimum), and `search.maximum_incoming_search_results`. Defaults match Nicotine+: On, 3 characters, and 300 results; the editable ranges are 0–50 characters and 50–10000 results. Saved changes hot-apply without reconnecting. Paths containing case-insensitive phrases prohibited by Soulseek server message 160 are always omitted from responses; this does not affect local browsing or exact-path uploads.

Wishlist searches are daemon-owned and survive TUI and daemon restarts in `wishlist.json`. Press `w` in Search to save the active query and filter. The Wishlist workspace uses `/` to add, `f` to edit the selected item's stored filter, Enter to open its latest cached results, `r` to rerun it immediately, and `d` to remove it. Automatic searches rotate one item at a time, so each item repeats after roughly item count × effective interval; **Settings → Search → Wishlist interval** (`search.wishlist_interval_minutes`) controls the delay between requests (`0` is Off), clamped to the minimum interval advertised by the Soulseek server. `search.wishlist_notifications` controls bells and desktop notifications. With notifications enabled, changed nonempty filtered results mark the item unread, ring an attached TUI once, and invoke `notify-send` when available. The unread badge remains available when notifications are disabled or desktop delivery fails. Result payloads stay in daemon memory, so after a daemon restart an item must run again before it can be opened.

Settings → Connection shows the public IPv4 address reported by the Soulseek server at login and controls **Connect on startup** (`soulseek.connect_on_startup`), **Network interface** (`soulseek.network_interface`), **NAT-PMP port forwarding** (`soulseek.nat_pmp_port_mapping`), and **UPnP port forwarding** (`soulseek.upnp_port_mapping`). The displayed address itself does not use a third-party lookup. Selecting **Listening port status** and pressing Enter explicitly sends one HTTPS request to the Soulseek website at `www.slsknet.org/porttest.php`; the daemon checks its current advertised TCP port, including a mapped or `--listen-port-file` port, and reports open, closed, or unknown after a maximum of five seconds. No check runs at startup or in the background. All three switches default to On and the forwarding protocols can be enabled independently. With both forwarding protocols enabled, oto tries NAT-PMP before UPnP. It maps only the incoming TCP listener through an IPv4 router, requests a 12-hour lease, and renews it every two hours. Discovery and mapping are best effort: failures do not prevent Soulseek login or its server-mediated firewall-piercing fallback.

The network-interface picker cycles through **Automatic**, interfaces visible in the daemon's network namespace, and **Custom…** for a name that is currently unavailable. Saving a changed interface reconnects the Soulseek session. A selected interface binds every Soulseek TCP socket with Linux `SO_BINDTODEVICE`; binding is fail-closed, so a missing interface or permission error leaves the session reconnecting instead of allowing traffic over another route. Automatic NAT-PMP/UPnP is skipped without changing its saved switches while interface binding is active; use `--listen-port-file` for a VPN-assigned forwarded port.

Settings → Uploads manages ordered named speed-limit profiles. `uploads.active_profile` selects a profile; `speed_limit_kib` is KiB/s and `0` means unlimited. `limit_scope` accepts `total` or `per_transfer`; `scheduling` accepts `fifo`, `round_robin`, `random`, or `smallest_first`. Limits can apply once across all active uploads or independently to each transfer. FIFO, user-fair round-robin, user-uniform random, and smallest-file-first scheduling affect queued uploads only. Profile, scope, and scheduler changes are staged until `s`, then hot-applied without reconnecting; active transfers keep running.

Settings → Account can change the currently connected Soulseek account password. Select **Change Soulseek password**, press Enter, and enter the new password twice. The change is sent and saved immediately; it cannot be used while disconnected, while a username change is staged, or when `OTO_PASSWORD` supplies the credential.

## Download controls and completion commands

Downloads have a persistent **paused** state. `p` stops selected downloads, including ones waiting for a slot; `r` resumes them from the actual partial-file length. Resume is an action that returns a download to the queue, not a separate state. Paused and cancelled downloads stay stopped across reconnects and restarts. `d` still cancels; `c` clears inactive entries and removes their partial data.

Once all bytes arrive, a download briefly enters **finalizing** while moving to its destination. Pause, cancel, and clear cannot interrupt that move; status and other transfers remain responsive, including during cross-filesystem copies.

Transient connection failures automatically enter **retrying**, with another attempt due in **3 minutes**. Local file I/O failures and remote file-read failures retry after **15 minutes**. Retry deadlines survive restarts; attempts wait until connected and respect download slots. There is no retry limit. Unknown errors, malformed protocol messages, changed file sizes, and permanent rejections require manual intervention; `r` also retries immediately instead of waiting for a deadline.

**Settings → Downloads** provides **After file command** and **After folder command**, saved as `downloads.after_file_command` and `downloads.after_folder_command`. Both default to empty (disabled) and hot-apply when saved. For example:

```json
"downloads": {
  "after_file_command": "/usr/local/bin/process-file \"$1\"",
  "after_folder_command": "/usr/local/bin/process-folder \"$1\""
}
```

Commands are trusted local POSIX shell snippets, run with `/bin/sh -c` as the daemon user. Use **`"$1"`** for the final file/folder path (not Nicotine+'s bare `$` placeholder). Paths are passed separately as an argument, never substituted into shell source. Scripts must be installed in the daemon's environment, including inside the container when using Docker. Commands launch asynchronously, with output discarded unless redirected; failures are logged without failing the download or retrying the command. The folder command does not wait for file commands. Running command process groups are cancelled when the daemon stops.

Hooks run only after a successful final move and journal save, not on restoring completed downloads. Folder completion covers all currently known files from the same user in the **exact destination directory**, excluding the download root itself. Paused, cancelled, failed, and retrying entries block it until completed or cleared. Subfolders finish independently; this is not a recursive folder-job hook. Adding more files later can trigger it again. Hooks are best effort, not a crash-safe exactly-once job queue.

### Completion notifications

Settings → Downloads has independent **File notifications** (`downloads.file_notifications`, Off) and **Folder notifications** (`downloads.folder_notifications`, On) switches. Saved changes hot-apply. Notifications use the same successful move-and-journal-save boundary and exact-directory grouping as completion commands. Clearing entries alone does not send a notification; restoring completed downloads does not replay them.

The daemon calls `notify-send` asynchronously when available. Delivery has a two-second timeout; failure is logged without failing or retrying the download. Attached TUIs display the latest message and ring once per poll containing new enabled completions. Initial attachment and daemon restart do not replay old bells. These signals are session-local and best effort, not a durable notification queue. When both switches are enabled, the final file can produce both file and folder desktop notifications.

Desktop delivery requires `notify-send` and a usable desktop session in the daemon's environment, including inside Docker. No D-Bus forwarding is provided. Existing completion commands still run independently; disable overlapping notification scripts if using the built-in delivery.

## Files and environment

Default locations follow XDG:

- config: `${XDG_CONFIG_HOME:-~/.config}/oto/config.json`;
- state and incomplete downloads: `${XDG_STATE_HOME:-~/.local/state}/oto/`;
- search and filter history: `${XDG_STATE_HOME:-~/.local/state}/oto/history.json`;
- wishlist definitions and unread metadata: `${XDG_STATE_HOME:-~/.local/state}/oto/wishlist.json`;
- explicitly saved remote share lists: `${XDG_STATE_HOME:-~/.local/state}/oto/usershares/`;
- socket: `${XDG_RUNTIME_DIR:-/tmp/oto-$UID}/oto/oto.sock`;
- downloads: `~/Downloads/oto`.

`OTO_USERNAME`, `OTO_PASSWORD`, `OTO_SERVER`, `OTO_LISTEN_ADDR`, `OTO_NETWORK_INTERFACE`, and `OTO_DOWNLOAD_DIR` override JSON values. The daemon never returns or logs the password.

History, wishlist, and upload settings are user choices in `config.json`; mutable history and wishlist entries live in private state files so searches do not continually rewrite daemon configuration.

Incoming TCP port `50300` must be reachable for best peer connectivity. Automatic NAT-PMP/UPnP forwarding can make it reachable when supported by the IPv4 router and no network interface is selected; otherwise configure the router or use `--listen-port-file` for a VPN-assigned port. Direct connections are attempted first and server-mediated firewall piercing is used as fallback. The Soulseek protocol itself is not encrypted; do not treat usernames, searches, or transferred data as private.

Search-result country codes are approximate IP geolocation, not identity or residence data. oto performs the lookup offline using an embedded table generated from a pinned [sapics/ip-location-db](https://github.com/sapics/ip-location-db) `user-country-ipv4` snapshot released under the PDDL; peer IP addresses are not exposed or persisted.
## Feature comparison with Nicotine+

This tracks user-visible Soulseek functionality and meaningful operational quality-of-life features for a terminal client. Cosmetic GUI details, desktop integration, deep links, themes, layout customization, and similar presentation-only features are intentionally excluded. :white_check_mark: means the feature works end to end, :x: means it is unavailable, and :fast_forward: means oto supersedes the same user need with a different approach; internal scaffolding alone is not counted.

### Network and session

| Feature | oto | Nicotine+ |
| --- | --- | --- |
| Soulseek account login | :white_check_mark: | :white_check_mark: |
| Create a new account by logging in with unused credentials | :white_check_mark: | :white_check_mark: |
| Custom Soulseek server | :white_check_mark: | :white_check_mark: |
| Configurable listening address and port | :white_check_mark: | :white_check_mark: |
| Incoming direct peer connections | :white_check_mark: | :white_check_mark: |
| Direct outbound peer connections | :white_check_mark: | :white_check_mark: |
| Server-mediated firewall piercing | :white_check_mark: | :white_check_mark: |
| Distributed-search network participation | :white_check_mark: | :white_check_mark: |
| Respond to incoming searches | :white_check_mark: | :white_check_mark: |
| Automatic reconnect after connection failure | :white_check_mark: | :white_check_mark: |
| Manually connect and disconnect without quitting | :white_check_mark: | :white_check_mark: |
| Configure whether to connect on startup | :white_check_mark: | :white_check_mark: |
| Bind Soulseek traffic to a VPN or network interface | :white_check_mark: | :white_check_mark: |
| Automatic UPnP port forwarding | :white_check_mark: | :white_check_mark: |
| Automatic NAT-PMP port forwarding | :white_check_mark: | :white_check_mark: |
| External listening-port check | :white_check_mark: | :white_check_mark: |
| Public IP address lookup | :white_check_mark: | :white_check_mark: |
| Change Soulseek password from the client | :white_check_mark: | :white_check_mark: |
| Online, away, and offline status controls | :white_check_mark: | :white_check_mark: |
| Automatic away status after inactivity | :x: | :white_check_mark: |
| Automatic private-message reply while away | :x: | :white_check_mark: |
| Check remaining Soulseek supporter privileges | :x: | :white_check_mark: |
| Gift Soulseek privileges to another user | :x: | :white_check_mark: |

### Search

| Feature | oto | Nicotine+ |
| --- | --- | --- |
| Global file search | :white_check_mark: | :white_check_mark: |
| Exact quoted phrases | :white_check_mark: | :white_check_mark: |
| Excluded search terms | :white_check_mark: | :white_check_mark: |
| Partial-word search terms | :white_check_mark: | :white_check_mark: |
| Search files in joined rooms | :x: | :white_check_mark: |
| Search files shared by all buddies | :x: | :white_check_mark: |
| Search files shared by specific users | :x: | :white_check_mark: |
| Cache results and refilter without another network search | :white_check_mark: | :white_check_mark: |
| Include-text result filter | :white_check_mark: | :white_check_mark: |
| Exclude-text result filter | :white_check_mark: | :white_check_mark: |
| Regular-expression result filters | :white_check_mark: | :x: |
| File-extension result filter | :white_check_mark: | :white_check_mark: |
| Generic audio, video, image, document, archive, and executable filters | :white_check_mark: | :white_check_mark: |
| File-size comparisons and ranges | :white_check_mark: | :white_check_mark: |
| Bitrate comparisons and ranges | :white_check_mark: | :white_check_mark: |
| Duration comparisons and ranges | :white_check_mark: | :white_check_mark: |
| Available-upload-slot filter | :white_check_mark: | :white_check_mark: |
| Public/private-file filter | :white_check_mark: | :white_check_mark: |
| Display locked private search results | :white_check_mark: | :white_check_mark: |
| Country-code result filter | :white_check_mark: | :white_check_mark: |
| Persistent search history | :white_check_mark: | :white_check_mark: |
| Persistent filter history | :white_check_mark: | :white_check_mark: |
| Configurable default result filters | :white_check_mark: | :white_check_mark: |
| Persistent wishlist searches | :white_check_mark: | :white_check_mark: |
| Periodically rerun wishlist searches | :white_check_mark: | :white_check_mark: |
| Store filters per wishlist item | :white_check_mark: | :white_check_mark: |
| Manually rerun a wishlist item | :white_check_mark: | :white_check_mark: |
| Notify when a wishlist finds results | :white_check_mark: | :white_check_mark: |
| Disable responses to incoming searches | :white_check_mark: | :white_check_mark: |
| Configure minimum incoming search length | :white_check_mark: | :white_check_mark: |
| Configure maximum results returned to peers | :white_check_mark: | :white_check_mark: |
| Honor server-provided excluded search phrases | :white_check_mark: | :white_check_mark: |

### Browsing users and files

| Feature | oto | Nicotine+ |
| --- | --- | --- |
| Browse another user's shares | :white_check_mark: | :white_check_mark: |
| Browse your own shares | :white_check_mark: | :white_check_mark: |
| Refresh a user's share list | :white_check_mark: | :white_check_mark: |
| Jump from a search result to its user's folder | :white_check_mark: | :white_check_mark: |
| Browse public and private entries returned by a peer | :white_check_mark: | :white_check_mark: |
| Search within a loaded share list | :white_check_mark: | :white_check_mark: |
| Download selected files | :white_check_mark: | :white_check_mark: |
| Download a loaded folder subtree | :white_check_mark: | :white_check_mark: |
| Request and download a complete remote folder recursively | :white_check_mark: | :white_check_mark: |
| Choose a different download destination interactively | :white_check_mark: | :white_check_mark: |
| Save a remote share list to disk | :white_check_mark: | :white_check_mark: |
| Reopen a saved share list, including while offline | :white_check_mark: | :white_check_mark: |
| Show progress while retrieving large share lists | :white_check_mark: | :white_check_mark: |
| View detailed file properties and media metadata | :white_check_mark: | :white_check_mark: |

### Downloads

| Feature | oto | Nicotine+ |
| --- | --- | --- |
| Queue individual file downloads | :white_check_mark: | :white_check_mark: |
| Queue multiple selected downloads | :white_check_mark: | :white_check_mark: |
| Resume partial downloads by byte offset | :white_check_mark: | :white_check_mark: |
| Persist downloads across restarts | :white_check_mark: | :white_check_mark: |
| Retry failed downloads | :white_check_mark: | :white_check_mark: |
| Cancel downloads | :white_check_mark: | :white_check_mark: |
| Clear inactive downloads | :white_check_mark: | :white_check_mark: |
| Explicit pause and resume controls | :white_check_mark: | :white_check_mark: |
| Automatically retry transient download failures | :white_check_mark: | :white_check_mark: |
| Configurable maximum concurrent downloads | :white_check_mark: | :x: |
| Keep incomplete files separate from completed downloads | :white_check_mark: | :white_check_mark: |
| Store downloads in per-user subfolders | :white_check_mark: | :white_check_mark: |
| Avoid overwriting collisions by choosing an unused filename | :white_check_mark: | :white_check_mark: |
| Separate folder for files manually sent by other users | :x: | :white_check_mark: |
| Rename a file before downloading | :x: | :white_check_mark: |
| Automatic filename-based download filters | :x: | :white_check_mark: |
| Force a filtered download to bypass filters | :x: | :white_check_mark: |
| Global download speed limit | :x: | :white_check_mark: |
| Alternate download speed-limit preset | :x: | :white_check_mark: |
| Run a command after a file finishes | :white_check_mark: | :white_check_mark: |
| Run a command after a folder finishes | :white_check_mark: | :white_check_mark: |
| Notify when a file or folder finishes | :white_check_mark: | :white_check_mark: |
| Automatically clear finished or filtered downloads | :x: | :white_check_mark: |
| Allow selected users to send unsolicited files | :x: | :white_check_mark: |
| Request and track remote queue position | :white_check_mark: | :white_check_mark: |
| Show transfer speed and progress | :white_check_mark: | :white_check_mark: |
| Estimate elapsed and remaining transfer time | :x: | :white_check_mark: |
| Search the network for a transfer's file or folder name | :white_check_mark: | :white_check_mark: |
| Remove the associated incomplete file when deleting a transfer | :white_check_mark: | :white_check_mark: |

### Uploads

| Feature | oto | Nicotine+ |
| --- | --- | --- |
| Serve shared files to peers | :white_check_mark: | :white_check_mark: |
| Queue competing upload requests | :white_check_mark: | :white_check_mark: |
| Configurable fixed upload-slot count | :white_check_mark: | :white_check_mark: |
| Report upload queue positions | :white_check_mark: | :white_check_mark: |
| Persist the upload queue and history across restarts | :x: | :white_check_mark: |
| Retry failed uploads | :x: | :white_check_mark: |
| Abort selected uploads | :x: | :white_check_mark: |
| Abort every upload from selected users | :x: | :white_check_mark: |
| Clear uploads by status | :x: | :white_check_mark: |
| Automatically clear finished or cancelled uploads | :x: | :white_check_mark: |
| Manually send a file to another user | :x: | :white_check_mark: |
| Manually send a folder to another user | :x: | :white_check_mark: |
| Global upload speed limit | :white_check_mark: | :white_check_mark: |
| Named upload speed-limit profiles | :white_check_mark: | :x: |
| Alternate upload speed-limit preset | :white_check_mark: | :white_check_mark: |
| Limit upload speed per transfer or across all transfers | :white_check_mark: | :white_check_mark: |
| FIFO upload scheduling | :white_check_mark: | :white_check_mark: |
| Round-robin upload scheduling | :white_check_mark: | :white_check_mark: |
| Random upload scheduling | :white_check_mark: | :x: |
| Smallest-file-first upload scheduling | :white_check_mark: | :x: |
| Allocate upload slots until a bandwidth threshold is reached | :x: | :white_check_mark: |
| Prioritize buddies in the upload queue | :x: | :white_check_mark: |
| Prioritize Soulseek privileged users | :x: | :white_check_mark: |
| Per-user queued-file limit | :x: | :white_check_mark: |
| Per-user queued-byte limit | :x: | :white_check_mark: |
| Exempt buddies from upload queue limits | :x: | :white_check_mark: |
| Wait for active uploads to finish before quitting | :x: | :white_check_mark: |
| Message all users currently downloading | :x: | :white_check_mark: |

### Shares and permissions

| Feature | oto | Nicotine+ |
| --- | --- | --- |
| Multiple public share roots | :white_check_mark: | :white_check_mark: |
| Custom virtual names for share roots | :white_check_mark: | :white_check_mark: |
| Add and remove shares without restarting | :white_check_mark: | :white_check_mark: |
| Manual share rescan | :white_check_mark: | :white_check_mark: |
| Scan shares on startup | :white_check_mark: | :white_check_mark: |
| Exclude hidden files and folders | :white_check_mark: | :white_check_mark: |
| Configurable share exclusion patterns | :x: | :white_check_mark: |
| Persistent on-disk share index | :white_check_mark: | :white_check_mark: |
| Scheduled daily share rescans | :fast_forward: | :white_check_mark: |
| Force a full share rebuild | :x: | :white_check_mark: |
| Stop an in-progress share scan | :x: | :white_check_mark: |
| Report share-scan progress | :white_check_mark: | :white_check_mark: |
| Extract audio metadata while indexing local shares | :x: | :white_check_mark: |
| Publish shared folder and file counts | :white_check_mark: | :white_check_mark: |
| Public shares | :white_check_mark: | :white_check_mark: |
| Buddy-only shares | :x: | :white_check_mark: |
| Trusted-buddy-only shares | :x: | :white_check_mark: |
| Reveal restricted share tiers selectively | :x: | :white_check_mark: |
| Use buddy trust as a share permission | :x: | :white_check_mark: |

:fast_forward: oto watches share filesystem changes and reconciles after its quiet/max delays instead of waiting for a daily schedule.

### Users, chat, and community

| Feature | oto | Nicotine+ |
| --- | --- | --- |
| Private messages | :x: | :white_check_mark: |
| Queued and offline private messages | :x: | :white_check_mark: |
| Persistent private-message history | :x: | :white_check_mark: |
| Broadcast a private message to buddies or downloading users | :x: | :white_check_mark: |
| Join public chat rooms | :x: | :white_check_mark: |
| Browse the room directory | :x: | :white_check_mark: |
| Public feed of room messages | :x: | :white_check_mark: |
| Create chat rooms | :x: | :white_check_mark: |
| Remember and rejoin rooms | :x: | :white_check_mark: |
| Create and join private rooms | :x: | :white_check_mark: |
| Private-room invitations | :x: | :white_check_mark: |
| Private-room member, operator, and owner management | :x: | :white_check_mark: |
| Persistent room-wall messages | :x: | :white_check_mark: |
| Buddy list | :x: | :white_check_mark: |
| Buddy notes | :x: | :white_check_mark: |
| Buddy online-status notifications | :x: | :white_check_mark: |
| Buddy last-seen timestamps | :x: | :white_check_mark: |
| Prioritized buddies | :x: | :white_check_mark: |
| Trusted buddies | :x: | :white_check_mark: |
| Personal likes and dislikes | :x: | :white_check_mark: |
| Interest-based recommendations | :x: | :white_check_mark: |
| Similar-user discovery | :x: | :white_check_mark: |
| View user profiles | :x: | :white_check_mark: |
| Publish a self-description | :x: | :white_check_mark: |
| View user country, interests, shares, speed, slots, and queue statistics | :x: | :white_check_mark: |
| Resolve and display a user's IP address | :x: | :white_check_mark: |
| Ignore users by username | :x: | :white_check_mark: |
| Ignore users by IP address | :x: | :white_check_mark: |
| Ban users by username | :x: | :white_check_mark: |
| Ban users by IP address | :x: | :white_check_mark: |
| Ban users by country | :x: | :white_check_mark: |
| Custom ban and country-block messages | :x: | :white_check_mark: |
| CTCP/client-information requests | :x: | :white_check_mark: |
| Keyword and mention detection | :x: | :white_check_mark: |
| Chat tab completion | :x: | :white_check_mark: |
| Chat spelling checks | :x: | :white_check_mark: |
| Outgoing text substitutions | :x: | :white_check_mark: |
| Incoming text censorship patterns | :x: | :white_check_mark: |
| `/me` actions and extensible chat commands | :x: | :white_check_mark: |

### Extensibility, persistence, and operation

| Feature | oto | Nicotine+ |
| --- | --- | --- |
| Run the Soulseek client headlessly | :white_check_mark: | :white_check_mark: |
| Interactive command console in headless mode | :x: | :white_check_mark: |
| Scriptable transfer and rescan commands through a daemon socket | :white_check_mark: | :x: |
| Run the network session as a standalone background service | :white_check_mark: | :x: |
| Detach and later attach a frontend to the same live session | :white_check_mark: | :x: |
| Keep transfers running after the attached frontend exits | :white_check_mark: | :x: |
| Plugin system | :x: | :white_check_mark: |
| Install, enable, disable, reload, and configure plugins | :x: | :white_check_mark: |
| Plugin hooks for chat, search, users, and transfers | :x: | :white_check_mark: |
| Extensible chat and headless commands | :x: | :white_check_mark: |
| Built-in spam, anti-shout, leech-detection, and automation plugins | :x: | :white_check_mark: |
| Persistent chat-room logs | :x: | :white_check_mark: |
| Persistent private-chat logs | :x: | :white_check_mark: |
| Persistent transfer logs | :x: | :white_check_mark: |
| Configurable diagnostic/debug logs | :x: | :white_check_mark: |
| Current-session and lifetime transfer statistics | :x: | :white_check_mark: |
| Now-playing messages from MPRIS, Last.fm, Libre.fm, or ListenBrainz | :x: | :white_check_mark: |
