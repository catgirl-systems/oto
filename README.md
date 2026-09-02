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
./oto daemon --share-rescan-delay 30s  # default 5m; use 0 to disable watching
./oto daemon --listen-port-file /run/oto/forwarded-port --listen-port-reconcile-interval 30s
./oto status
./oto status --json
```

The TUI uses an owner-only Unix socket. If no standalone daemon exists, it starts a child daemon whose lifetime is tied to the TUI. Exiting the TUI asks for confirmation when that would pause active transfers. An attached standalone daemon and its transfers continue running.

Soulseek permits one login per username. Keeping the session in the daemon prevents the TUI from kicking it off the network.

The daemon watches every non-hidden, non-symlink directory under each share root using `fsnotify` (one recursive watch per directory). After filesystem changes stop for five minutes by default, it builds a complete shadow index and atomically publishes it; a fixed 30-minute maximum delay prevents continuous activity from postponing scans forever. Events during a scan discard that result and schedule another scan. Startup and manual rescans remain immediate, and watcher or scan failures keep the last good index available.

For dynamically forwarded incoming ports, `--listen-port-file` watches the file's parent directory and hot-swaps the listener whenever the file contains a new port. `--listen-port-reconcile-interval` provides a periodic fallback (default `30s`; `0` disables it). Missing or empty files mark the port unavailable until a valid value appears. This interface is provider-neutral; for example, a VPN container can write its current forwarded port into a shared volume.

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
| `i` in Search or Browse | show the selected file's properties and media metadata |
| `ctrl+page up` / `ctrl+page down` in Search, Browse, or Transfers | switch result tabs or Downloads/Uploads |
| `ctrl+w` in Search or Browse | close the active result tab |
| `d` | download selected files; choose folder-only or recursive download on a folder; cancel a transfer subtree; or remove a share root |
| `r` | refresh a user browse, retry a transfer subtree, or rescan shares |
| `s` | save settings and reconnect |
| `?` | keyboard guide |
| `q` | quit |

Set `NO_COLOR=1` to suppress terminal styling.

Search groups results as user → folders → files; Browse shows remote folders; Transfers groups each direction as user → folders → files; Shares lazily shows the daemon's scanned public-share index. Expansion and selection are session-local. Selection-based Search/Browse actions include only currently loaded files; using `d` on a folder can request that complete remote folder recursively without fetching the user's full share list.

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


## Feature comparison with Nicotine+

This tracks user-visible Soulseek functionality and meaningful operational quality-of-life features for a terminal client. Cosmetic GUI details, desktop integration, deep links, themes, layout customization, and similar presentation-only features are intentionally excluded. A check means the feature works end to end for users in the current client; internal scaffolding alone is not counted.

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
| Manually connect and disconnect without quitting | :x: | :white_check_mark: |
| Configure whether to connect on startup | :x: | :white_check_mark: |
| Bind Soulseek traffic to a VPN or network interface | :x: | :white_check_mark: |
| Automatic UPnP port forwarding | :x: | :white_check_mark: |
| Automatic NAT-PMP port forwarding | :x: | :white_check_mark: |
| External listening-port check | :x: | :white_check_mark: |
| Public IP address lookup | :x: | :white_check_mark: |
| Change Soulseek password from the client | :x: | :white_check_mark: |
| Online, away, and offline status controls | :x: | :white_check_mark: |
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
| Country-code result filter | :x: | :white_check_mark: |
| Persistent search history | :x: | :white_check_mark: |
| Persistent filter history | :x: | :white_check_mark: |
| Configurable default result filters | :x: | :white_check_mark: |
| Persistent wishlist searches | :x: | :white_check_mark: |
| Periodically rerun wishlist searches | :x: | :white_check_mark: |
| Store filters per wishlist item | :x: | :white_check_mark: |
| Manually rerun a wishlist item | :x: | :white_check_mark: |
| Notify when a wishlist finds results | :x: | :white_check_mark: |
| Disable responses to incoming searches | :x: | :white_check_mark: |
| Configure minimum incoming search length | :x: | :white_check_mark: |
| Configure maximum results returned to peers | :x: | :white_check_mark: |
| Honor server-provided excluded search phrases | :x: | :white_check_mark: |

### Browsing users and files

| Feature | oto | Nicotine+ |
| --- | --- | --- |
| Browse another user's shares | :white_check_mark: | :white_check_mark: |
| Browse your own shares | :white_check_mark: | :white_check_mark: |
| Refresh a user's share list | :white_check_mark: | :white_check_mark: |
| Jump from a search result to its user's folder | :white_check_mark: | :white_check_mark: |
| Browse public and private entries returned by a peer | :white_check_mark: | :white_check_mark: |
| Search within a loaded share list | :x: | :white_check_mark: |
| Download selected files | :white_check_mark: | :white_check_mark: |
| Download a loaded folder subtree | :white_check_mark: | :white_check_mark: |
| Request and download a complete remote folder recursively | :white_check_mark: | :white_check_mark: |
| Choose a different download destination interactively | :x: | :white_check_mark: |
| Save a remote share list to disk | :x: | :white_check_mark: |
| Reopen a saved share list, including while offline | :x: | :white_check_mark: |
| Show progress while retrieving large share lists | :x: | :white_check_mark: |
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
| Explicit pause and resume states | :x: | :white_check_mark: |
| Automatically retry transient download failures | :x: | :white_check_mark: |
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
| Run a command after a file finishes | :x: | :white_check_mark: |
| Run a command after a folder finishes | :x: | :white_check_mark: |
| Automatically clear finished or filtered downloads | :x: | :white_check_mark: |
| Allow selected users to send unsolicited files | :x: | :white_check_mark: |
| Request and track remote queue position | :white_check_mark: | :white_check_mark: |
| Show transfer speed and progress | :white_check_mark: | :white_check_mark: |
| Estimate elapsed and remaining transfer time | :x: | :white_check_mark: |
| Search the network for a transfer's file or folder name | :x: | :white_check_mark: |
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
| Global upload speed limit | :x: | :white_check_mark: |
| Alternate upload speed-limit preset | :x: | :white_check_mark: |
| Limit upload speed per transfer or across all transfers | :x: | :white_check_mark: |
| FIFO upload scheduling | :white_check_mark: | :white_check_mark: |
| Round-robin upload scheduling | :x: | :white_check_mark: |
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
| Persistent on-disk share index | :x: | :white_check_mark: |
| Scheduled daily share rescans | :x: | :white_check_mark: |
| Force a full share rebuild | :x: | :white_check_mark: |
| Stop an in-progress share scan | :x: | :white_check_mark: |
| Report share-scan progress | :x: | :white_check_mark: |
| Extract audio metadata while indexing local shares | :x: | :white_check_mark: |
| Publish shared folder and file counts | :white_check_mark: | :white_check_mark: |
| Public shares | :white_check_mark: | :white_check_mark: |
| Buddy-only shares | :x: | :white_check_mark: |
| Trusted-buddy-only shares | :x: | :white_check_mark: |
| Reveal restricted share tiers selectively | :x: | :white_check_mark: |
| Use buddy trust as a share permission | :x: | :white_check_mark: |

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
