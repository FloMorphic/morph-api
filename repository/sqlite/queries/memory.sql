-- name: GetMemoryStore :one
SELECT * FROM memory_stores WHERE id = @id;

-- name: ListMemoryStores :many
SELECT * FROM memory_stores
ORDER BY updated_at DESC, id DESC;

-- name: CreateMemoryStore :exec
INSERT INTO memory_stores (
    id, name, type, description, vector_config, document_config, created_at, updated_at
) VALUES (
    @id, @name, @type, @description, @vector_config, @document_config, @created_at, @updated_at
);

-- name: DeleteMemoryStore :execrows
DELETE FROM memory_stores WHERE id = @id;
