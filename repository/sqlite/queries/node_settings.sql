-- name: GetNodeSetting :one
SELECT * FROM node_settings WHERE id = @id;

-- name: ListNodeSettings :many
SELECT * FROM node_settings
WHERE (@search = '' OR title LIKE '%' || @search || '%')
  AND (@node_uniq_id = '' OR node_uniq_id = @node_uniq_id)
ORDER BY updated_at DESC, id DESC
LIMIT @limit OFFSET @offset;

-- name: CountNodeSettings :one
SELECT COUNT(*) FROM node_settings
WHERE (@search = '' OR title LIKE '%' || @search || '%')
  AND (@node_uniq_id = '' OR node_uniq_id = @node_uniq_id);

-- name: UpsertNodeSetting :exec
INSERT INTO node_settings (
    id, node_uniq_id, node_type, title, settings, created_at, updated_at
) VALUES (
    @id, @node_uniq_id, @node_type, @title, @settings, @created_at, @updated_at
)
ON CONFLICT(id) DO UPDATE SET
    node_uniq_id = excluded.node_uniq_id,
    node_type = excluded.node_type,
    title = excluded.title,
    settings = excluded.settings,
    updated_at = excluded.updated_at;

-- name: DeleteNodeSetting :execrows
DELETE FROM node_settings WHERE id = @id;
