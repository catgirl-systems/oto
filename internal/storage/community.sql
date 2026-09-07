-- All text identities use SQLite's default BINARY collation, never NOCASE.
-- Account strings reuse the daemon's existing server/account identity.
CREATE TABLE community_accounts (
    account TEXT PRIMARY KEY NOT NULL CHECK (length(account) > 0),
    revision INTEGER NOT NULL DEFAULT 0 CHECK (typeof(revision) = 'integer' AND revision >= 0),
    description TEXT NOT NULL DEFAULT '',
    accept_invitations INTEGER NOT NULL DEFAULT 1 CHECK (accept_invitations IN (0, 1)),
    retention_days INTEGER NOT NULL DEFAULT 0 CHECK (retention_days >= 0),
    public_feed_logging INTEGER NOT NULL DEFAULT 0 CHECK (public_feed_logging IN (0, 1))
);

CREATE TABLE community_buddies (
    account TEXT NOT NULL REFERENCES community_accounts(account) ON DELETE CASCADE,
    username TEXT NOT NULL CHECK (length(username) > 0),
    note TEXT NOT NULL DEFAULT '',
    notify_online INTEGER NOT NULL DEFAULT 0 CHECK (notify_online IN (0, 1)),
    priority INTEGER NOT NULL DEFAULT 0 CHECK (priority IN (0, 1)),
    trusted INTEGER NOT NULL DEFAULT 0 CHECK (trusted IN (0, 1)),
    last_seen INTEGER,
    PRIMARY KEY(account, username)
);

CREATE TABLE community_conversations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account TEXT NOT NULL REFERENCES community_accounts(account) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('private', 'room')),
    target TEXT NOT NULL CHECK (length(target) > 0),
    read_through INTEGER NOT NULL DEFAULT 0 CHECK (read_through >= 0),
    closed INTEGER NOT NULL DEFAULT 0 CHECK (closed IN (0, 1)),
    UNIQUE(account, kind, target),
    UNIQUE(account, id)
);
CREATE INDEX community_conversations_page ON community_conversations(account, id);

-- A local monotonic ID orders content independently of remote timestamps. The
-- outgoing states are the outbox; there is no second copy of message content.
CREATE TABLE community_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account TEXT NOT NULL,
    conversation_id INTEGER NOT NULL,
    sender TEXT NOT NULL,
    direction TEXT NOT NULL CHECK (direction IN ('incoming', 'outgoing', 'system')),
    body TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    server_time INTEGER,
    state TEXT NOT NULL CHECK (state IN ('received', 'held', 'queued', 'sending', 'sent', 'failed', 'unknown', 'cancelled')),
    mention INTEGER NOT NULL DEFAULT 0 CHECK (mention IN (0, 1)),
    error TEXT NOT NULL DEFAULT '',
    FOREIGN KEY(account, conversation_id) REFERENCES community_conversations(account, id) ON DELETE CASCADE
);
CREATE INDEX community_messages_page ON community_messages(account, conversation_id, id);
CREATE INDEX community_messages_retention ON community_messages(account, created_at);
CREATE INDEX community_outbox ON community_messages(account, state, id) WHERE direction = 'outgoing';

-- Receipts deliberately have no foreign key to content. Clearing/pruning a
-- transcript must not remove deduplication or resurrect deliberately ignored PMs.
CREATE TABLE community_receipts (
    account TEXT NOT NULL REFERENCES community_accounts(account) ON DELETE CASCADE,
    sender TEXT NOT NULL,
    server_id INTEGER NOT NULL,
    server_time INTEGER NOT NULL,
    fingerprint BLOB NOT NULL CHECK (length(fingerprint) = 32),
    disposition TEXT NOT NULL CHECK (disposition IN ('stored', 'discarded', 'held')),
    created_at INTEGER NOT NULL,
    PRIMARY KEY(account, sender, server_id, server_time, fingerprint)
);

-- Retriable local submissions outlive cleared content, too. The request hash
-- rejects accidental reuse of one ID for a different operation. Results include
-- pending/unknown outcomes; this table is not an automatic network retry queue.
CREATE TABLE community_submissions (
    account TEXT NOT NULL REFERENCES community_accounts(account) ON DELETE CASCADE,
    request_id TEXT NOT NULL CHECK (length(request_id) > 0),
    kind TEXT NOT NULL,
    fingerprint BLOB NOT NULL CHECK (length(fingerprint) = 32),
    result TEXT NOT NULL CHECK (json_valid(result)),
    created_at INTEGER NOT NULL,
    PRIMARY KEY(account, request_id)
);

-- Preferences only, not durable authority for membership, rosters or roles.
CREATE TABLE community_rooms (
    account TEXT NOT NULL REFERENCES community_accounts(account) ON DELETE CASCADE,
    room TEXT NOT NULL CHECK (length(room) > 0),
    autojoin INTEGER NOT NULL DEFAULT 0 CHECK (autojoin IN (0, 1)),
    private_room INTEGER NOT NULL DEFAULT 0 CHECK (private_room IN (0, 1)),
    own_wall TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(account, room)
);

CREATE TABLE community_interests (
    account TEXT NOT NULL REFERENCES community_accounts(account) ON DELETE CASCADE,
    item TEXT NOT NULL CHECK (length(item) > 0),
    opinion INTEGER NOT NULL CHECK (opinion IN (-1, 1)),
    PRIMARY KEY(account, item)
);

CREATE TABLE community_rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account TEXT NOT NULL REFERENCES community_accounts(account) ON DELETE CASCADE,
    action TEXT NOT NULL CHECK (action IN ('ignore', 'ban')),
    kind TEXT NOT NULL CHECK (kind IN ('username', 'ip', 'country')),
    value TEXT NOT NULL CHECK (length(value) > 0),
    message TEXT NOT NULL DEFAULT '',
    CHECK (action <> 'ignore' OR kind <> 'country'),
    UNIQUE(account, action, kind, value)
);
CREATE INDEX community_rules_page ON community_rules(account, id);

CREATE TABLE community_aliases (
    account TEXT NOT NULL REFERENCES community_accounts(account) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (length(name) > 0),
    expansion TEXT NOT NULL CHECK (length(expansion) > 0),
    PRIMARY KEY(account, name)
);
