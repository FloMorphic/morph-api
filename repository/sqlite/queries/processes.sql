-- name: GetProcess :one
SELECT * FROM processes WHERE index_id = @index_id;

-- name: GetRunningProcessByPID :one
SELECT * FROM processes
WHERE pid = @pid AND status = 'running'
ORDER BY index_id DESC
LIMIT 1;

-- name: ListProcesses :many
SELECT * FROM processes
WHERE (@search = '' OR pid LIKE '%' || @search || '%' OR flow_id LIKE '%' || @search || '%')
  AND (@status = '' OR status = @status)
  AND (@pid = '' OR pid = @pid)
  AND (@flow_id = '' OR flow_id = @flow_id)
ORDER BY updated_at DESC, index_id DESC
LIMIT @limit OFFSET @offset;

-- name: CountProcesses :one
SELECT COUNT(*) FROM processes
WHERE (@search = '' OR pid LIKE '%' || @search || '%' OR flow_id LIKE '%' || @search || '%')
  AND (@status = '' OR status = @status)
  AND (@pid = '' OR pid = @pid)
  AND (@flow_id = '' OR flow_id = @flow_id);

-- name: InsertProcess :execresult
INSERT INTO processes (
    pid, flow_id, context_id, start_node_id, status, resource_url,
    request, meta, error, scheduled_at, started_at, finished_at, duration_ms,
    created_at, updated_at
) VALUES (
    @pid, @flow_id, @context_id, @start_node_id, @status, @resource_url,
    @request, @meta, @error, @scheduled_at, @started_at, @finished_at, @duration_ms,
    @created_at, @updated_at
);

-- name: UpdateProcess :execrows
UPDATE processes SET
    pid = @pid,
    flow_id = @flow_id,
    context_id = @context_id,
    start_node_id = @start_node_id,
    status = @status,
    resource_url = @resource_url,
    request = @request,
    meta = @meta,
    error = @error,
    scheduled_at = @scheduled_at,
    started_at = @started_at,
    finished_at = @finished_at,
    duration_ms = @duration_ms,
    updated_at = @updated_at
WHERE index_id = @index_id;

-- name: DeleteProcess :execrows
DELETE FROM processes WHERE index_id = @index_id;
