-- name: GetConnectConnection :one
SELECT * FROM connect_connections WHERE id = @id;

-- name: ListConnectConnections :many
SELECT * FROM connect_connections
ORDER BY is_default DESC, updated_at DESC, id DESC;

-- name: CountConnectConnections :one
SELECT COUNT(*) FROM connect_connections;

-- name: UpsertConnectConnection :exec
INSERT INTO connect_connections (
    id, label, base_url, token, admin_token, kind, is_default, created_at, updated_at
) VALUES (
    @id, @label, @base_url, @token, @admin_token, @kind, @is_default, @created_at, @updated_at
)
ON CONFLICT(id) DO UPDATE SET
    label = excluded.label,
    base_url = excluded.base_url,
    token = excluded.token,
    admin_token = excluded.admin_token,
    kind = excluded.kind,
    is_default = excluded.is_default,
    updated_at = excluded.updated_at;

-- name: DeleteConnectConnection :execrows
DELETE FROM connect_connections WHERE id = @id;

-- name: ClearConnectDefault :exec
UPDATE connect_connections SET is_default = 0;

-- name: SetConnectDefault :execrows
UPDATE connect_connections SET is_default = 1, updated_at = @updated_at WHERE id = @id;
