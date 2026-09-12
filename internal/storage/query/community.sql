-- name: EnsureCommunityAccount :exec
INSERT INTO community_accounts(account) VALUES (?) ON CONFLICT(account) DO NOTHING;

-- name: GetCommunityAccount :one
SELECT * FROM community_accounts WHERE account = ?;

-- name: BumpCommunityRevision :one
UPDATE community_accounts SET revision = revision + 1 WHERE account = ? RETURNING revision;

-- name: EditCommunityAccount :execrows
UPDATE community_accounts SET description = ?, accept_invitations = ?, retention_days = ?, public_feed_logging = ?, revision = revision + 1
WHERE account = ? AND revision = ?;

-- name: PutCommunityBuddy :exec
INSERT INTO community_buddies(account, username, note, notify_online, priority, trusted) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(account, username) DO UPDATE SET note = excluded.note, notify_online = excluded.notify_online, priority = excluded.priority, trusted = excluded.trusted;

-- name: GetCommunityBuddy :one
SELECT * FROM community_buddies WHERE account = ? AND username = ?;

-- name: ListCommunityBuddies :many
SELECT * FROM community_buddies WHERE account = ? AND username > sqlc.arg(after_username)
ORDER BY username LIMIT min(max(CAST(sqlc.arg(page_size) AS INTEGER), 1), 200);

-- name: SetCommunityBuddyLastSeen :exec
UPDATE community_buddies SET last_seen = max(coalesce(last_seen, 0), CAST(sqlc.arg(seen_at) AS INTEGER))
WHERE account = sqlc.arg(account) AND username = sqlc.arg(username);

-- name: DeleteCommunityBuddy :execrows
DELETE FROM community_buddies WHERE account = ? AND username = ?;

-- name: EnsureCommunityConversation :one
INSERT INTO community_conversations(account, kind, target) VALUES (?, ?, ?)
ON CONFLICT(account, kind, target) DO UPDATE SET target = excluded.target
RETURNING *;

-- name: GetCommunityConversation :one
SELECT * FROM community_conversations WHERE account = ? AND id = ?;

-- name: FindCommunityConversation :one
SELECT * FROM community_conversations WHERE account = ? AND kind = ? AND target = ?;

-- name: ListCommunityConversations :many
SELECT * FROM community_conversations WHERE account = ? AND id > sqlc.arg(after_id)
ORDER BY id LIMIT min(max(CAST(sqlc.arg(page_size) AS INTEGER), 1), 200);

-- name: SetCommunityConversationClosed :execrows
UPDATE community_conversations SET closed = ? WHERE account = ? AND id = ?;

-- name: MarkCommunityRead :execrows
UPDATE community_conversations SET read_through = max(community_conversations.read_through, CAST(sqlc.arg(through_id) AS INTEGER))
WHERE community_conversations.account = sqlc.arg(account) AND community_conversations.id = sqlc.arg(conversation_id)
AND (sqlc.arg(through_id) <= community_conversations.read_through OR EXISTS (
    SELECT 1 FROM community_messages m WHERE m.account = sqlc.arg(account)
    AND m.conversation_id = sqlc.arg(conversation_id) AND m.id = sqlc.arg(through_id) AND m.state <> 'held'
));

-- name: InsertCommunityMessage :one
INSERT INTO community_messages(account, conversation_id, sender, direction, body, created_at, server_time, state, mention)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING *;

-- name: GetCommunityMessage :one
SELECT * FROM community_messages WHERE account = ? AND id = ?;

-- name: ListCommunityMessages :many
SELECT * FROM community_messages WHERE account = ? AND conversation_id = ? AND state <> 'held'
AND (CAST(sqlc.arg(before_id) AS INTEGER) = 0 OR id < sqlc.arg(before_id)) AND instr(body, CAST(sqlc.arg(search_text) AS TEXT)) > 0
ORDER BY id DESC LIMIT min(max(CAST(sqlc.arg(page_size) AS INTEGER), 1), 200);

-- name: CommunityUnread :many
SELECT c.id, count(m.id) AS unread, CAST(coalesce(sum(m.mention), 0) AS INTEGER) AS mentions
FROM community_conversations c
JOIN community_messages m ON m.account = c.account AND m.conversation_id = c.id
WHERE c.account = ? AND m.id > c.read_through AND m.direction = 'incoming' AND m.state = 'received'
GROUP BY c.id;

-- name: SetCommunityMessageState :execrows
UPDATE community_messages SET state = sqlc.arg(new_state), error = sqlc.arg(error)
WHERE account = sqlc.arg(account) AND id = sqlc.arg(id) AND state = sqlc.arg(old_state);

-- name: RecoverCommunityOutbox :execrows
UPDATE community_messages SET state = 'unknown' WHERE (CAST(sqlc.arg(account) AS TEXT) = '' OR account = sqlc.arg(account)) AND direction = 'outgoing' AND state = 'sending';

-- name: ListCommunityOutbox :many
SELECT * FROM community_messages WHERE account = ? AND direction = 'outgoing' AND state = 'queued' AND id > sqlc.arg(after_id)
ORDER BY id LIMIT min(max(CAST(sqlc.arg(page_size) AS INTEGER), 1), 200);

-- name: ListHeldCommunityMessages :many
SELECT * FROM community_messages WHERE account = ? AND sender = ? AND state = 'held' AND id > sqlc.arg(after_id)
ORDER BY id LIMIT min(max(CAST(sqlc.arg(page_size) AS INTEGER), 1), 200);

-- name: ClearCommunityHistory :execrows
DELETE FROM community_messages WHERE account = ? AND conversation_id = ? AND state IN ('received', 'sent', 'failed', 'cancelled');

-- name: PruneCommunityHistory :execrows
DELETE FROM community_messages WHERE account = ? AND created_at < sqlc.arg(before_time) AND state IN ('received', 'sent', 'failed', 'cancelled');

-- name: InsertCommunityReceipt :execrows
INSERT INTO community_receipts(account, sender, server_id, server_time, fingerprint, disposition, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(account, sender, server_id, server_time, fingerprint) DO NOTHING;

-- name: GetCommunityReceipt :one
SELECT * FROM community_receipts WHERE account = ? AND sender = ? AND server_id = ? AND server_time = ? AND fingerprint = ?;

-- name: InsertCommunitySubmission :execrows
INSERT INTO community_submissions(account, request_id, kind, fingerprint, result, created_at) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(account, request_id) DO NOTHING;

-- name: GetCommunitySubmission :one
SELECT * FROM community_submissions WHERE account = ? AND request_id = ?;

-- name: SetCommunitySubmissionResult :execrows
UPDATE community_submissions SET result = ? WHERE account = ? AND request_id = ?;

-- name: PutCommunityRoom :exec
INSERT INTO community_rooms(account, room, autojoin, private_room, own_wall) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(account, room) DO UPDATE SET autojoin = excluded.autojoin, private_room = excluded.private_room, own_wall = excluded.own_wall;

-- name: GetCommunityRoom :one
SELECT * FROM community_rooms WHERE account = ? AND room = ?;

-- name: ListCommunityRooms :many
SELECT * FROM community_rooms WHERE account = ? AND room > sqlc.arg(after_room)
ORDER BY room LIMIT min(max(CAST(sqlc.arg(page_size) AS INTEGER), 1), 200);

-- name: DeleteCommunityRoom :execrows
DELETE FROM community_rooms WHERE account = ? AND room = ?;

-- name: PutCommunityInterest :exec
INSERT INTO community_interests(account, item, opinion) VALUES (?, ?, ?)
ON CONFLICT(account, item) DO UPDATE SET opinion = excluded.opinion;

-- name: ListCommunityInterests :many
SELECT * FROM community_interests WHERE account = ? AND item > sqlc.arg(after_item)
ORDER BY item LIMIT min(max(CAST(sqlc.arg(page_size) AS INTEGER), 1), 200);

-- name: DeleteCommunityInterest :execrows
DELETE FROM community_interests WHERE account = ? AND item = ?;

-- name: PutCommunityRule :one
INSERT INTO community_rules(account, action, kind, value, message) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(account, action, kind, value) DO UPDATE SET message = excluded.message RETURNING *;

-- name: ListCommunityRules :many
SELECT * FROM community_rules WHERE account = ? AND id > sqlc.arg(after_id)
ORDER BY id LIMIT min(max(CAST(sqlc.arg(page_size) AS INTEGER), 1), 200);

-- name: DeleteCommunityRule :execrows
DELETE FROM community_rules WHERE account = ? AND id = ?;

-- name: PutCommunityAlias :exec
INSERT INTO community_aliases(account, name, expansion) VALUES (?, ?, ?)
ON CONFLICT(account, name) DO UPDATE SET expansion = excluded.expansion;

-- name: GetCommunityAlias :one
SELECT * FROM community_aliases WHERE account = ? AND name = ?;

-- name: ListCommunityAliases :many
SELECT * FROM community_aliases WHERE account = ? AND name > sqlc.arg(after_name)
ORDER BY name LIMIT min(max(CAST(sqlc.arg(page_size) AS INTEGER), 1), 200);

-- name: DeleteCommunityAlias :execrows
DELETE FROM community_aliases WHERE account = ? AND name = ?;

-- name: CommunityUnreadTotals :one
SELECT count(m.id) AS unread, CAST(coalesce(sum(m.mention), 0) AS INTEGER) AS mentions
FROM community_conversations c
JOIN community_messages m ON m.account = c.account AND m.conversation_id = c.id
WHERE c.account = ? AND m.id > c.read_through AND m.direction = 'incoming' AND m.state = 'received';


-- name: PageCommunityConversations :many
SELECT c.*,
    (SELECT count(*) FROM community_messages m WHERE m.account = c.account AND m.conversation_id = c.id AND m.id > c.read_through AND m.direction = 'incoming' AND m.state = 'received') AS unread,
    (SELECT CAST(coalesce(sum(m.mention), 0) AS INTEGER) FROM community_messages m WHERE m.account = c.account AND m.conversation_id = c.id AND m.id > c.read_through AND m.direction = 'incoming' AND m.state = 'received') AS mentions,
    (SELECT CAST(coalesce(max(m.id), 0) AS INTEGER) FROM community_messages m WHERE m.account = c.account AND m.conversation_id = c.id AND m.state <> 'held') AS latest_id
FROM community_conversations c WHERE c.account = sqlc.arg(account) AND c.id > sqlc.arg(after_id)
AND (CAST(sqlc.arg(kind) AS TEXT) = '' OR c.kind = sqlc.arg(kind))
AND (CAST(sqlc.arg(include_closed) AS INTEGER) <> 0 OR c.closed = 0)
AND instr(c.target, CAST(sqlc.arg(search_text) AS TEXT)) > 0
ORDER BY c.id LIMIT min(max(CAST(sqlc.arg(page_size) AS INTEGER), 1), 200);

-- name: CommunityHistoryInfo :one
SELECT
    (SELECT CAST(coalesce(max(m.id), 0) AS INTEGER) FROM community_messages m WHERE m.account = sqlc.arg(account) AND m.conversation_id = sqlc.arg(conversation_id) AND m.state <> 'held') AS latest_id,
    (SELECT count(*) FROM community_messages n WHERE n.account = sqlc.arg(account) AND n.conversation_id = sqlc.arg(conversation_id) AND n.state <> 'held' AND n.id > sqlc.arg(newer_than)) AS newer_count;


-- name: ClearCommunityHistoryThrough :execrows
DELETE FROM community_messages WHERE account = ? AND conversation_id = ? AND id <= sqlc.arg(through_id)
AND state IN ('received', 'sent', 'failed', 'cancelled');


-- name: ExportCommunityMessages :many
SELECT * FROM community_messages WHERE account = ? AND conversation_id = ? AND state <> 'held'
AND id > sqlc.arg(after_id) AND id <= sqlc.arg(through_id)
AND instr(body, CAST(sqlc.arg(search_text) AS TEXT)) > 0
ORDER BY id LIMIT min(max(CAST(sqlc.arg(page_size) AS INTEGER), 1), 200);
