-- name: GetWorkflow :one
SELECT * FROM workflows WHERE id = @id;

-- The tag predicate is a match-ANY membership test: @tags is the filter as
-- ",a,b," (repository.TagFilter) and json_each walks the row's own array, so
-- `instr` of ",<tag>," is an exact element match. '' means "no filter".

-- name: ListWorkflows :many
SELECT * FROM workflows
WHERE (@search = '' OR title LIKE '%' || @search || '%')
  AND (@tags = '' OR EXISTS (
        SELECT 1 FROM json_each(workflows.tags) AS tag
        WHERE instr(@tags, ',' || tag.value || ',') > 0))
ORDER BY updated_at DESC, id DESC
LIMIT @limit OFFSET @offset;

-- name: CountWorkflows :one
SELECT COUNT(*) FROM workflows
WHERE (@search = '' OR title LIKE '%' || @search || '%')
  AND (@tags = '' OR EXISTS (
        SELECT 1 FROM json_each(workflows.tags) AS tag
        WHERE instr(@tags, ',' || tag.value || ',') > 0));

-- name: UpsertWorkflow :exec
INSERT INTO workflows (id, title, view_flow, tags, preferences, created_at, updated_at)
VALUES (@id, @title, @view_flow, @tags, @preferences, @created_at, @updated_at)
ON CONFLICT(id) DO UPDATE SET
    title = excluded.title,
    view_flow = excluded.view_flow,
    tags = excluded.tags,
    preferences = excluded.preferences,
    updated_at = excluded.updated_at;

-- name: DeleteWorkflow :execrows
DELETE FROM workflows WHERE id = @id;
