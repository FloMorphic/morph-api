-- name: GetHumanTask :one
SELECT * FROM human_tasks WHERE id = @id;

-- name: ListHumanTasks :many
SELECT * FROM human_tasks
WHERE (@search = '' OR title LIKE '%' || @search || '%')
  AND (@status = '' OR status = @status)
ORDER BY updated_at DESC, id DESC
LIMIT @limit OFFSET @offset;

-- name: CountHumanTasks :one
SELECT COUNT(*) FROM human_tasks
WHERE (@search = '' OR title LIKE '%' || @search || '%')
  AND (@status = '' OR status = @status);

-- name: UpsertHumanTask :exec
INSERT INTO human_tasks (
    id, title, status, pid, flow_id, node_id, context_id,
    mode, channel, prompt,
    questions, messages, data, nexts, created_at, updated_at, closed_at
) VALUES (
    @id, @title, @status, @pid, @flow_id, @node_id, @context_id,
    @mode, @channel, @prompt,
    @questions, @messages, @data, @nexts, @created_at, @updated_at, @closed_at
)
ON CONFLICT(id) DO UPDATE SET
    title = excluded.title,
    status = excluded.status,
    pid = excluded.pid,
    flow_id = excluded.flow_id,
    node_id = excluded.node_id,
    context_id = excluded.context_id,
    mode = excluded.mode,
    channel = excluded.channel,
    prompt = excluded.prompt,
    questions = excluded.questions,
    messages = excluded.messages,
    data = excluded.data,
    nexts = excluded.nexts,
    updated_at = excluded.updated_at,
    closed_at = excluded.closed_at;

-- name: DeleteHumanTask :execrows
DELETE FROM human_tasks WHERE id = @id;
