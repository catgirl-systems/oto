# Community acceptance ledger

## Contract

Implementation branch: `feat/social`, starting at `7aa4c41`. The agreed scope is
38/39 social rows plus 22 neighboring rows. Spelling remains unavailable. No
plugin runtime, now-playing integration, release, push, or unrelated README prose
changes are included. Reference checkouts stay unchanged.

Preserve the existing soulseek → daemon/storage → IPC → TUI layers. The daemon
owns durable social state and permission policy; lossy diagnostic events must not
carry authoritative social updates. Identities include server/account and exact
username; asynchronous results include a session generation. SQLite transcripts
are the logs, with independent ordering, replay receipts and monotone read
markers. Only the locked daemon upgrades old databases, using a validated SQLite
snapshot backup and a transactional v1→v2 migration that preserves legacy tables.

The Community workspace follows Transfers and contains Chats, Rooms, Buddies,
Discover. It must support responsive panes, independent frontend drafts, safe
paste, accessible focus, paged history, explicit pending/failed/unknown outcomes,
and shared contextual user actions. Ignore affects chat; bans affect serving.
Explicit username/IP/country bans override buddy/trust. Public/buddy/trusted access
is cumulative; disclosure never grants download access. Receiving is opt-in and
uses a separate safe destination tree. Gifts and broadcasts require previews and
confirmation; no test may spend privileges on the public network.

## Evidence levels and commands

- **Unit/scripted:** deterministic tests using temporary databases, loopback
  TCP/Unix sockets, cancellation and synchronization barriers. These do not
  establish real-server interoperability.
- **Reference wire:** frozen `internal/testutil/testdata/social.json` payloads.
  Client/peer bytes originate in Nicotine+'s encoder; server bytes were assembled
  independently using Python `struct`, then parsed by Nicotine+. Oto's codec is
  not used to create them. `python3 scripts/check-social-fixtures.py` checks codes,
  encodings, parsed fields, full consumption and the clean reference revision.
- **Real server/peer:** the pinned Soulfind/slskd integration suite, plus an
  isolated Nicotine+ peer. Unsupported advanced server functionality requires
  mandatory reference-checked scripted tests, explicitly labeled as such.
- **Terminal:** `go test -tags communitye2e -count=1 -v ./internal/e2e` builds oto,
  runs its real daemon and IPC socket, and drives real PTYs with a private tmux
  server. No fake TUI model or production account is used. The explicit test tag
  keeps tmux test-only; the CI job must run it and missing tmux is a failure.
  `OTO_E2E_ARTIFACTS=/path` preserves synthetic, credential-scrubbed diagnostics
  and captured screens on failure. Each invocation has isolated HOME/XDG paths.

Reference revisions:
- Nicotine+: `0e2a467269255572906f8360e89f70ddc40f7076`,
  `pynicotine/slskmessages.py` (watch/status 767–853; room send/join/leave 902–1006;
  PM/ACK 1096–1146; stats 1334–1362; peer profile 3556–3607; code maps 4302–4430).
- slskd: `bf3e1c7a536f5e2715802daeabff15a5ee3af622`,
  `src/slskd/Messaging/{ConversationService,RoomService}.cs` and user services.

## Row → scenario traceability

IDs below identify acceptance scenarios, not claims of implemented features.
**Pending** rows require backend, accessible UI/command workflow, and runnable
verification before README may mark them complete. Scenario families will gain
exact test names and evidence as their sequential commits land.

| ID | README row | Required scenario family / step | Evidence |
|---|---|---|---|
| S01 | Private messages | PM send/receive; two clients / 5 | Pending |
| S02 | Queued and offline private messages | PM outbox/replay/ambiguous writes / 5 | Pending |
| S03 | Persistent private-message history | PM restart/read/clear/export / 5 | Pending |
| S04 | Broadcast a private message to buddies or downloading users | Audience preview/pacing/partial outcomes / 15 | Pending |
| S05 | Join public chat rooms | Confirmed membership/roster/echo / 6 | Pending |
| S06 | Browse the room directory | Pagination/filter/population / 6 | Pending |
| S07 | Public feed of room messages | Explicit read-only bounded feed / 6 | Pending |
| S08 | Create chat rooms | Name validation/server errors / 6 | Pending |
| S09 | Remember and rejoin rooms | Reconnect/gaps/forget vs leave / 6 | Pending |
| S10 | Create and join private rooms | Private room authority / 7 | Pending |
| S11 | Private-room invitations | Membership grants/invitation preference / 7 | Pending |
| S12 | Private-room member, operator, and owner management | All supported roles/revocation / 7 | Pending |
| S13 | Persistent room-wall messages | Ticker update/remove/rejoin restoration / 7 | Pending |
| S14 | Buddy list | Account/exact-user isolation; CRUD / 8 | Pending |
| S15 | Buddy notes | Edit/restart/frontends / 8 | Pending |
| S16 | Buddy online-status notifications | Hydration suppression/transitions / 8 | Pending |
| S17 | Buddy last-seen timestamps | Remote offline vs local disconnect / 8 | Pending |
| S18 | Prioritized buddies | Flags and actual queue effect / 8,11 | Pending |
| S19 | Trusted buddies | Flags and actual share effect / 8,10 | Pending |
| S20 | Personal likes and dislikes | Normalize/persist/republish / 9 | Pending |
| S21 | Interest-based recommendations | Global/item/partial responses / 9 | Pending |
| S22 | Similar-user discovery | Queries/cancellation/200-watch bound / 9 | Pending |
| S23 | View user profiles | Partial timeout/bounded picture/save / 9 | Pending |
| S24 | Publish a self-description | Persisted self-profile/peer response / 9 | Pending |
| S25 | View user country, interests, shares, speed, slots, and queue statistics | Freshness/correlation/partial response / 9 | Pending |
| S26 | Resolve and display a user's IP address | Exact identity/epoch/country / 3,9 | Pending |
| S27 | Ignore users by username | Durable discard/ACK/no notifications / 10 | Pending |
| S28 | Ignore users by IP address | CIDR/unresolved withholding / 10 | Pending |
| S29 | Ban users by username | Serving matrix/revocation / 10 | Pending |
| S30 | Ban users by IP address | Exact/CIDR serving matrix / 10 | Pending |
| S31 | Ban users by country | Known vs unknown; stricter precedence / 10 | Pending |
| S32 | Custom ban and country-block messages | Rejection text across serving paths / 10 | Pending |
| S33 | CTCP/client-information requests | Default off/rate/replay/loop suppression / 14 | Pending |
| S34 | Keyword and mention detection | Boundaries/pre-censor detection / 14 | Pending |
| S35 | Chat tab completion | Tab/Shift+Tab/pane/input contract / 14 | Pending |
| S36 | Chat spelling checks | Explicitly excluded; no dependency | Excluded |
| S37 | Outgoing text substitutions | Ordered literal replacement/size validation / 14 | Pending |
| S38 | Incoming text censorship patterns | Wildcards/sanitized notifications / 14 | Pending |
| S39 | `/me` actions and extensible chat commands | Shared registry/aliases/slash/paste / 14 | Pending |
| N01 | Automatic away status after inactivity | Real activity/manual vs automatic / 15 | Pending |
| N02 | Automatic private-message reply while away | Once per sender/away period; exclusions / 15 | Pending |
| N03 | Check remaining Soulseek supporter privileges | Balance/staleness/rejection / 12 | Pending |
| N04 | Gift Soulseek privileges to another user | Preview/cancel/confirm/uncertain/no retry / 12 | Pending |
| N05 | Search files in joined rooms | Captured targets/no global fallback / 13 | Pending |
| N06 | Search files shared by all buddies | More than 32/bounded fan-out / 13 | Pending |
| N07 | Allow selected users to send unsolicited files | Consent/admission/bans/recovery / 17 | Pending |
| N08 | Separate folder for files manually sent by other users | Containment/collision/filter/hooks / 17 | Pending |
| N09 | Manually send a file to another user | Shared snapshot/preview/idempotency / 16 | Pending |
| N10 | Manually send a folder to another user | Reference recipient/accept/reject / 16 | Pending |
| N11 | Prioritize buddies in the upload queue | One preferred class/all scheduling modes / 11 | Pending |
| N12 | Prioritize Soulseek privileged users | Privilege changes/one active per user / 11 | Pending |
| N13 | Exempt buddies from upload queue limits | Opt-in/accounting/overflow/recovery / 11 | Pending |
| N14 | Message all users currently downloading | Active-upload audience preview / 15 | Pending |
| N15 | Buddy-only shares | Cumulative serving matrix / 10 | Pending |
| N16 | Trusted-buddy-only shares | Cumulative serving matrix / 10 | Pending |
| N17 | Reveal restricted share tiers selectively | Locked disclosure vs permission / 10 | Pending |
| N18 | Use buddy trust as a share permission | Self exclusion/revocation/cache/recovery / 10 | Pending |
| N19 | Persistent private-chat logs | SQLite searchable history/text+JSON export / 5 | Pending |
| N20 | Persistent chat-room logs | Retention/export/history gaps / 6 | Pending |
| N21 | Interactive command console in headless mode | Real binary/daemon console / 14 | Pending |
| N22 | Extensible chat and headless commands | Registry/alias args/cycles/no execution / 14 | Pending |

## Cross-cutting gates

| Gate | Required verification | Evidence |
|---|---|---|
| G01 | Schema fresh/upgrade/rollback/WAL backup/concurrent legacy history/downgrade | `internal/storage`: `TestCommunityMigration*`, `TestCommunityFreshBootstrapRollback`, `TestCommunityStorage*` |
| G02 | Reliable callback/backpressure/unlocked dispatch/watches/stale epochs/reconnect | `internal/soulseek`: `TestCommunityDispatch*`, `TestCommunityAddress*`, `TestCommunityInterruptedFrameRetiresTransport`, `TestCommunityCancelledRequestDoesNotWrite`; `internal/daemon`: `TestCommunityWatch*`, `TestCommunityPresenceFreshnessAndEpoch` |
| G03 | Two TUIs: drafts/read markers/no focus theft/scroll anchors/detach | Pending steps 4–5 |
| G04 | 120×40, 80×24, 40×16, tiny; resize while editing; Unicode/CJK/emoji; NO_COLOR | Pending steps 4–18 |
| G05 | Search/browse/folder/admission/active/recovery permission matrix | Pending step 10 |
| G06 | Large histories/directories/bursts during transfers/discovery bounds/performance | Pending step 18 |
| G07 | `go test -race ./...`; `go vet ./...` | Baseline race suite passes; final run pending |
| G08 | `sqlc generate` with no generated diff | Pending final run |
| G09 | Existing `TestSoulfind` integration command extended with social tests | Pending steps 5–18 |
| G10 | Mandatory terminal CI; 20 social race repetitions; 10 terminal repetitions | Pending final workflows |
| G11 | Bounded decoder fuzz/cancellation/shutdown | Pending steps 3–18 |
| G12 | CGO=0 linux amd64/arm64 builds; unchanged container behavior | Pending final run |
| G13 | Row audit and README matrix/count-only update | Pending step 19 |

## Completed slices

### 1. Acceptance fixtures and E2E harness

- `internal/testutil/social.go`: explicitly scripted loopback TCP helpers and
  frozen reference fixtures; `TestSocialFixtureFraming` and
  `TestScriptSocketCleanup` verify corpus framing and interrupted-I/O cleanup.
- `scripts/check-social-fixtures.py`: 21 fixtures cross-checked with the pinned
  unmodified Nicotine+ codec. This establishes seed wire evidence, not social
  feature implementation or real-server coverage.
- `internal/e2e/terminal_test.go`, `TestCommunityTerminalStartupNavigation`:
  built binary, real daemon/socket/server login, Search→Wishlist→Browse→Transfers,
  reverse navigation, detach and reattach with independent 120×40 and 80×24 PTYs.
  `NO_COLOR=1`. More layouts and social workflows remain pending.
- Slice gate passed: `go test -race ./...`, `go vet ./...`, terminal-package
  vet, reference fixture check, and ten isolated terminal repetitions. No
  real-server social interoperability has been run yet.

### 2. Storage

- Schema v2; daemon-only transactional migration with private, validated WAL-aware
  backup, unchanged legacy records/uint64 encodings, public root defaults, and
  live v1 reader/writer compatibility. Generated account-scoped Community queries
  cover durable messages/outbox, independent replay/submission receipts, monotone
  read markers, buddies/settings/rooms/interests/rules/aliases, and 200-row paging.
- `TestCommunityMigration*`, `TestCommunityFreshBootstrapRollback` and
  `TestCommunityStorage*` passed 20 race-enabled repetitions. Full race/vet suites
  and ten terminal smoke repetitions passed; repeated sqlc generation is stable.
  This is storage evidence only; user-facing feature rows remain pending.

### 3. Reliable dispatch and shared watches

- Synchronous, cancellation-aware authoritative server callbacks, isolated from
  lossy diagnostics and the overlapping peer command namespace. Typed watch,
  presence and statistics codecs; existing address correlation reused and bounded.
  Interrupted frame writes retire the transport instead of corrupting later sends.
- Daemon account/session fencing; shared watch ownership with expiring frontend
  leases, exact-case identities, coalesced-release/reacquire hydration, paged
  restoration of buddies/open conversations, reconnect resubscription and stale
  live metadata. Last-seen advances only on observed remote offline transitions;
  failed persistence does not publish the update.
- `TestCommunityUserProtocol` checks 10 frozen reference fixtures. Corpus now has
  23 reference-checked fixtures. `TestCommunityDispatchBurst` verifies 1,024 reliable
  updates while the diagnostic buffer is full. Watch tests include 205 persisted
  buddies, multiple consumers, detach expiry, offline lease capacity and reconnect.
- Full race/vet suites, 20 targeted race repetitions, 10 terminal smoke repetitions,
  and a 10-second bounded decoder fuzz run passed. Independent review findings
  gained regression tests and fixes. This is scripted/reference evidence, not
  real-server social interoperability or complete user-facing feature coverage.

## Remaining commit sequence

4 Community shell/user actions
→ 5 private chat → 6 public rooms/feed → 7 private roles/walls → 8 buddies
→ 9 profiles/discovery → 10 permissions → 11 upload policies → 12 privileges
→ 13 scoped search → 14 text/commands → 15 away/broadcasts → 16 manual sends
→ 17 consented receiving → 18 full verification → 19 verified README matrix.
Each slice includes applicable tests before the next commit. Nothing is complete
merely because a fixture, test harness, planned test name, or compatible API exists.
