PRAGMA foreign_keys = ON;

CREATE TABLE storage_schema (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    version INTEGER NOT NULL CHECK (version = 1)
);
INSERT INTO storage_schema(id, version) VALUES (1, 1);

CREATE TABLE state_meta (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    download_sequence BLOB NOT NULL CHECK (length(download_sequence) = 8),
    upload_sequence BLOB NOT NULL CHECK (length(upload_sequence) = 8),
    upload_queue_sequence BLOB NOT NULL CHECK (length(upload_queue_sequence) = 8),
    wishlist_sequence BLOB NOT NULL CHECK (length(wishlist_sequence) = 8),
    history_sequence BLOB NOT NULL CHECK (length(history_sequence) = 8),
    stats_since INTEGER
);
INSERT INTO state_meta(id, download_sequence, upload_sequence, upload_queue_sequence, wishlist_sequence, history_sequence, stats_since)
VALUES (1, zeroblob(8), zeroblob(8), zeroblob(8), zeroblob(8), zeroblob(8), NULL);

CREATE TABLE downloads (
    id TEXT PRIMARY KEY,
    stats_account TEXT NOT NULL DEFAULT '',
    filter_bypass INTEGER NOT NULL DEFAULT 0,
    username TEXT NOT NULL,
    filename TEXT NOT NULL,
    size BLOB NOT NULL CHECK (length(size) = 8),
    offset BLOB NOT NULL CHECK (length(offset) = 8),
    download_dir TEXT NOT NULL DEFAULT '',
    destination TEXT NOT NULL,
    state TEXT NOT NULL,
    retry_at INTEGER,
    error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX downloads_state ON downloads(state, created_at, id);
CREATE INDEX downloads_updated ON downloads(updated_at, id);

CREATE TABLE uploads (
    id TEXT PRIMARY KEY,
    account TEXT NOT NULL,
    username TEXT NOT NULL,
    filename TEXT NOT NULL,
    direction TEXT NOT NULL,
    state TEXT NOT NULL,
    done BLOB NOT NULL CHECK (length(done) = 8),
    total BLOB NOT NULL CHECK (length(total) = 8),
    elapsed_ms BLOB CHECK (elapsed_ms IS NULL OR length(elapsed_ms) = 8),
    speed_bps BLOB NOT NULL CHECK (length(speed_bps) = 8),
    eta_seconds BLOB CHECK (eta_seconds IS NULL OR length(eta_seconds) = 8),
    queue INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    queue_order BLOB NOT NULL CHECK (length(queue_order) = 8),
    fingerprint TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    queued_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    recoverable INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX uploads_queue ON uploads(account, queue_order, id);
CREATE INDEX uploads_state ON uploads(state, updated_at, id);

CREATE TABLE active_attempts (
    id TEXT PRIMARY KEY,
    transfer_id TEXT NOT NULL,
    attempt BLOB NOT NULL CHECK (length(attempt) = 8),
    direction TEXT NOT NULL,
    username TEXT NOT NULL,
    filename TEXT NOT NULL,
    event_id TEXT NOT NULL,
    payload BLOB NOT NULL
);
CREATE INDEX active_attempts_transfer ON active_attempts(transfer_id, id);

CREATE TABLE seen (
    id TEXT PRIMARY KEY
);
CREATE TABLE events (
    id TEXT PRIMARY KEY,
    account TEXT,
    peer TEXT,
    direction TEXT,
    session TEXT,
    kind TEXT,
    at INTEGER,
    data TEXT NOT NULL
);
CREATE INDEX event_account ON events(account, at DESC, id DESC);
CREATE INDEX event_peer ON events(account, peer, at DESC, id DESC);
CREATE INDEX event_filter ON events(account, direction, session, kind, at DESC, id DESC);
CREATE TABLE totals (
    account TEXT,
    peer TEXT,
    direction TEXT,
    session TEXT,
    day TEXT,
    data TEXT NOT NULL,
    PRIMARY KEY(account, peer, direction, session, day)
);
CREATE INDEX totals_day ON totals(account, day);

CREATE TABLE wishlist (
    id TEXT PRIMARY KEY,
    query TEXT NOT NULL UNIQUE,
    filter TEXT NOT NULL DEFAULT '',
    added_at INTEGER NOT NULL,
    last_run_at INTEGER,
    result_count INTEGER NOT NULL DEFAULT 0,
    result_signature TEXT NOT NULL DEFAULT '',
    unread INTEGER NOT NULL DEFAULT 0,
    notification_sequence BLOB NOT NULL CHECK (length(notification_sequence) = 8)
);
CREATE INDEX wishlist_order ON wishlist(added_at, id);

CREATE TABLE history (
    kind TEXT NOT NULL,
    value TEXT NOT NULL,
    recency BLOB NOT NULL CHECK (length(recency) = 8),
    PRIMARY KEY(kind, value)
);
CREATE INDEX history_recent ON history(kind, recency DESC, value);

CREATE TABLE ui_preferences (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE share_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    source TEXT NOT NULL,
    normalized_username TEXT NOT NULL DEFAULT '',
    username TEXT NOT NULL DEFAULT '',
    saved_at INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL CHECK (state IN ('staging', 'published')),
    created_at INTEGER NOT NULL
);
CREATE INDEX share_snapshots_gc ON share_snapshots(state, id);
CREATE TABLE share_heads (
    source TEXT NOT NULL,
    normalized_username TEXT NOT NULL DEFAULT '',
    snapshot_id INTEGER NOT NULL REFERENCES share_snapshots(id) ON DELETE RESTRICT,
    PRIMARY KEY(source, normalized_username)
);
CREATE INDEX share_heads_snapshot ON share_heads(snapshot_id);
CREATE TABLE share_roots (
    snapshot_id INTEGER NOT NULL REFERENCES share_snapshots(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    name TEXT NOT NULL,
    path TEXT NOT NULL,
    PRIMARY KEY(snapshot_id, ordinal)
);
CREATE TABLE share_exclusions (
    snapshot_id INTEGER NOT NULL REFERENCES share_snapshots(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    pattern TEXT NOT NULL,
    PRIMARY KEY(snapshot_id, ordinal)
);
CREATE TABLE share_entries (
    snapshot_id INTEGER NOT NULL REFERENCES share_snapshots(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('local', 'remote')),
    root TEXT NOT NULL DEFAULT '',
    path TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL DEFAULT '',
    size BLOB NOT NULL CHECK (length(size) = 8),
    directory INTEGER NOT NULL DEFAULT 0,
    private INTEGER NOT NULL DEFAULT 0,
    vbr INTEGER NOT NULL DEFAULT 0,
    vbr_known INTEGER NOT NULL DEFAULT 0,
    extension TEXT NOT NULL DEFAULT '',
    bitrate INTEGER NOT NULL DEFAULT 0,
    duration INTEGER NOT NULL DEFAULT 0,
    sample_rate INTEGER NOT NULL DEFAULT 0,
    bit_depth INTEGER NOT NULL DEFAULT 0,
    audio_source TEXT NOT NULL DEFAULT '',
    fingerprint_size BLOB CHECK (fingerprint_size IS NULL OR length(fingerprint_size) = 8),
    fingerprint_mtime INTEGER NOT NULL DEFAULT 0,
    fingerprint_ctime INTEGER NOT NULL DEFAULT 0,
    extractor_version TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(snapshot_id, ordinal)
);
CREATE INDEX share_entries_lookup ON share_entries(snapshot_id, kind, root, path, name, ordinal);

PRAGMA user_version = 1;
