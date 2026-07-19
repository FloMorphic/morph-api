-- name: GetContext :one
SELECT * FROM contexts WHERE id = @id;

-- name: ListContexts :many
SELECT * FROM contexts
WHERE (@search = '' OR title LIKE '%' || @search || '%')
ORDER BY updated_at DESC, id DESC
LIMIT @limit OFFSET @offset;

-- name: CountContexts :one
SELECT COUNT(*) FROM contexts
WHERE (@search = '' OR title LIKE '%' || @search || '%');

-- name: UpsertContext :exec
INSERT INTO contexts (
    id, title, context, header, updated_by_type, updated_by_address, created_at, updated_at
) VALUES (
    @id, @title, @context, @header, @updated_by_type, @updated_by_address, @created_at, @updated_at
)
ON CONFLICT(id) DO UPDATE SET
    title = excluded.title,
    context = excluded.context,
    header = excluded.header,
    updated_by_type = excluded.updated_by_type,
    updated_by_address = excluded.updated_by_address,
    updated_at = excluded.updated_at;

-- name: DeleteContext :execrows
DELETE FROM contexts WHERE id = @id;
