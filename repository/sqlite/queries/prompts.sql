-- name: GetPrompt :one
SELECT * FROM prompts WHERE id = @id;

-- name: ListPrompts :many
SELECT * FROM prompts
WHERE (@search = '' OR title LIKE '%' || @search || '%' OR description LIKE '%' || @search || '%')
ORDER BY updated_at DESC, id DESC
LIMIT @limit OFFSET @offset;

-- name: CountPrompts :one
SELECT COUNT(*) FROM prompts
WHERE (@search = '' OR title LIKE '%' || @search || '%' OR description LIKE '%' || @search || '%');

-- name: UpsertPrompt :exec
INSERT INTO prompts (
    id, title, description, template, variables, tags, created_at, updated_at
) VALUES (
    @id, @title, @description, @template, @variables, @tags, @created_at, @updated_at
)
ON CONFLICT(id) DO UPDATE SET
    title = excluded.title,
    description = excluded.description,
    template = excluded.template,
    variables = excluded.variables,
    tags = excluded.tags,
    updated_at = excluded.updated_at;

-- name: DeletePrompt :execrows
DELETE FROM prompts WHERE id = @id;
