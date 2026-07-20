-- name: GetExtension :one
SELECT * FROM extensions WHERE id = @id;

-- name: GetBuiltinByName :one
SELECT * FROM extensions WHERE kind = 'builtin' AND name = @name;

-- name: ListExtensions :many
SELECT * FROM extensions
WHERE (@search = '' OR name LIKE '%' || @search || '%')
  AND (@kind = '' OR kind = @kind)
ORDER BY updated_at DESC, id DESC
LIMIT @limit OFFSET @offset;

-- name: CountExtensions :one
SELECT COUNT(*) FROM extensions
WHERE (@search = '' OR name LIKE '%' || @search || '%')
  AND (@kind = '' OR kind = @kind);

-- name: UpsertExtension :exec
INSERT INTO extensions (
    id, kind, type, name, description, plugin_id, icon, params, bind_to, created_at, updated_at
) VALUES (
    @id, @kind, @type, @name, @description, @plugin_id, @icon, @params, @bind_to, @created_at, @updated_at
)
ON CONFLICT(id) DO UPDATE SET
    kind = excluded.kind,
    type = excluded.type,
    name = excluded.name,
    description = excluded.description,
    plugin_id = excluded.plugin_id,
    icon = excluded.icon,
    params = excluded.params,
    bind_to = excluded.bind_to,
    updated_at = excluded.updated_at;

-- name: DeleteExtension :execrows
DELETE FROM extensions WHERE id = @id;
