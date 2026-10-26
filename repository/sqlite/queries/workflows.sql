-- name: GetWorkflow :one
SELECT * FROM workflows WHERE id = @id;

-- The tag predicate is a match-ANY membership test: @tags is the filter as
-- ",a,b," (repository.TagFilter) and json_each walks the row's own array, so
-- `instr` of ",<tag>," is an exact element match. '' means "no filter".

-- A list never renders the graph, so view_flow is NOT selected: a flow's graph
-- is tens of kilobytes, and a page of them was megabytes read, decoded and
-- sent for a field no caller looked at (see models.FlowSummary). What a list
-- does show of the graph is how many nodes it has, so that is counted here
-- instead: json_valid guards the empty text an un-migrated row could hold, and
-- the COALESCE covers a graph with no "nodes" member.
--
-- `plugins` IS selected: it is the handful of plugin entries derived from that
-- graph on save (models.CollectFlowPlugins), which is precisely what a list can
-- show about a flow's plugins without reading one graph.

-- name: ListWorkflows :many
SELECT id, title, tags, preferences, plugins, created_at, updated_at,
       CAST(CASE WHEN json_valid(view_flow)
                 THEN COALESCE(json_array_length(view_flow, '$.nodes'), 0)
                 ELSE 0 END AS INTEGER) AS node_count
FROM workflows
WHERE (@search = '' OR title LIKE '%' || @search || '%')
  AND (@tags = '' OR EXISTS (
        SELECT 1 FROM json_each(workflows.tags) AS tag
        WHERE instr(@tags, ',' || tag.value || ',') > 0))
ORDER BY updated_at DESC, id DESC
LIMIT @limit OFFSET @offset;

-- name: ListWorkflowRecords :many
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
INSERT INTO workflows (id, title, view_flow, tags, preferences, plugins, created_at, updated_at)
VALUES (@id, @title, @view_flow, @tags, @preferences, @plugins, @created_at, @updated_at)
ON CONFLICT(id) DO UPDATE SET
    title = excluded.title,
    view_flow = excluded.view_flow,
    tags = excluded.tags,
    preferences = excluded.preferences,
    plugins = excluded.plugins,
    updated_at = excluded.updated_at;

-- name: DeleteWorkflow :execrows
DELETE FROM workflows WHERE id = @id;
