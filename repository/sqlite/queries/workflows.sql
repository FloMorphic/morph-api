-- name: GetWorkflow :one
SELECT * FROM workflows WHERE id = @id;

-- name: ListWorkflows :many
SELECT * FROM workflows
WHERE (@search = '' OR title LIKE '%' || @search || '%')
ORDER BY updated_at DESC, id DESC
LIMIT @limit OFFSET @offset;

-- name: CountWorkflows :one
SELECT COUNT(*) FROM workflows
WHERE (@search = '' OR title LIKE '%' || @search || '%');

-- name: UpsertWorkflow :exec
INSERT INTO workflows (id, title, view_flow, created_at, updated_at)
VALUES (@id, @title, @view_flow, @created_at, @updated_at)
ON CONFLICT(id) DO UPDATE SET
    title = excluded.title,
    view_flow = excluded.view_flow,
    updated_at = excluded.updated_at;

-- name: DeleteWorkflow :execrows
DELETE FROM workflows WHERE id = @id;
