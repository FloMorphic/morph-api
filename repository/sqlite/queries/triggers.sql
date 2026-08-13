-- name: GetTrigger :one
SELECT * FROM triggers WHERE id = @id;

-- name: GetTriggerBySlug :one
SELECT * FROM triggers WHERE slug = @slug AND enabled = 1;

-- name: ListTriggers :many
SELECT * FROM triggers
WHERE (@flow_id = '' OR flow_id = @flow_id)
  AND (@kind = '' OR kind = @kind)
  AND (@search = '' OR title LIKE '%' || @search || '%')
ORDER BY updated_at DESC, id DESC
LIMIT @limit OFFSET @offset;

-- name: CountTriggers :one
SELECT COUNT(*) FROM triggers
WHERE (@flow_id = '' OR flow_id = @flow_id)
  AND (@kind = '' OR kind = @kind)
  AND (@search = '' OR title LIKE '%' || @search || '%');

-- name: ListEnabledSchedules :many
SELECT * FROM triggers WHERE kind = 'schedule' AND enabled = 1 ORDER BY id;

-- name: UpsertTrigger :exec
INSERT INTO triggers (
    id, kind, flow_id, start_node_id, title, enabled, slug, config,
    cron_effective, last_at, hits, created_at, updated_at
) VALUES (
    @id, @kind, @flow_id, @start_node_id, @title, @enabled, @slug, @config,
    @cron_effective, @last_at, @hits, @created_at, @updated_at
)
ON CONFLICT(id) DO UPDATE SET
    kind = excluded.kind,
    flow_id = excluded.flow_id,
    start_node_id = excluded.start_node_id,
    title = excluded.title,
    enabled = excluded.enabled,
    slug = excluded.slug,
    config = excluded.config,
    cron_effective = excluded.cron_effective,
    last_at = excluded.last_at,
    hits = excluded.hits,
    updated_at = excluded.updated_at;

-- name: UpdateTriggerState :exec
UPDATE triggers SET last_at = @last_at, hits = @hits, updated_at = @updated_at
WHERE id = @id;

-- name: DeleteTrigger :execrows
DELETE FROM triggers WHERE id = @id;
