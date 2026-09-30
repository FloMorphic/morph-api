-- name: UpsertTelegramRecipient :exec
INSERT INTO telegram_recipients (
    id, connection, alias, chat_id, type, title, username, first_name, last_name,
    last_seen_at, created_at, updated_at
) VALUES (
    @id, @connection, @alias, @chat_id, @type, @title, @username, @first_name, @last_name,
    @last_seen_at, @created_at, @updated_at
)
ON CONFLICT(id) DO UPDATE SET
    type = excluded.type,
    title = excluded.title,
    username = excluded.username,
    first_name = excluded.first_name,
    last_name = excluded.last_name,
    -- A chat re-seen out of order must not walk the timestamp backwards.
    last_seen_at = MAX(telegram_recipients.last_seen_at, excluded.last_seen_at),
    updated_at = excluded.updated_at;

-- name: ListTelegramRecipients :many
SELECT * FROM telegram_recipients
WHERE connection = @connection AND alias = @alias
ORDER BY last_seen_at DESC, id DESC
LIMIT @limit;

-- name: DeleteTelegramRecipient :execrows
DELETE FROM telegram_recipients WHERE id = @id;
