-- Schema for the sqlite backend. This file is the source of truth for both the
-- runtime migrations (applied on connect) and sqlc code generation.
--
-- Entity payloads that are free-form graphs/objects (view_flow, context, header,
-- and the memory configs) are stored as JSON text; the repository marshals to
-- and from the domain models. Vector similarity indexes are sqlite-vec (vec0)
-- virtual tables provisioned per vector store at runtime — see memory.go — and
-- so are intentionally absent here.

CREATE TABLE IF NOT EXISTS workflows (
    id         TEXT    PRIMARY KEY,
    title      TEXT    NOT NULL DEFAULT '',
    view_flow  TEXT    NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS contexts (
    id                 TEXT    PRIMARY KEY,
    title              TEXT    NOT NULL DEFAULT '',
    context            TEXT    NOT NULL DEFAULT '{}',
    header             TEXT    NOT NULL DEFAULT '{}',
    updated_by_type    TEXT    NOT NULL DEFAULT 'manual',
    updated_by_address TEXT    NOT NULL DEFAULT '',
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS memory_stores (
    id              TEXT    PRIMARY KEY,
    name            TEXT    NOT NULL DEFAULT '',
    type            TEXT    NOT NULL,
    description     TEXT    NOT NULL DEFAULT '',
    vector_config   TEXT,
    document_config TEXT,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS prompts (
    id          TEXT    PRIMARY KEY,
    title       TEXT    NOT NULL DEFAULT '',
    description TEXT    NOT NULL DEFAULT '',
    template    TEXT    NOT NULL DEFAULT '',
    variables   TEXT    NOT NULL DEFAULT '[]',
    tags        TEXT    NOT NULL DEFAULT '[]',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- Human-in-the-Loop tasks. Created by the inflow svc handler when a workflow
-- reaches a `humanInLoop` node (never via the REST API). `questions` and
-- `messages` are JSON arrays; `data` is the scoped-data snapshot the node
-- captured. `mode` (park/continue) and `channel` (direct/telegram/whatsapp) are
-- the node's design-time session config, and `prompt` is the conversation
-- opener with its context variables already resolved by the runtime.
CREATE TABLE IF NOT EXISTS human_tasks (
    id         TEXT    PRIMARY KEY,
    title      TEXT    NOT NULL DEFAULT '',
    status     TEXT    NOT NULL DEFAULT 'open',
    pid        TEXT    NOT NULL DEFAULT '',
    flow_id    TEXT    NOT NULL DEFAULT '',
    node_id    TEXT    NOT NULL DEFAULT '',
    context_id TEXT    NOT NULL DEFAULT '',
    mode       TEXT    NOT NULL DEFAULT 'park',
    channel    TEXT    NOT NULL DEFAULT 'direct',
    prompt     TEXT    NOT NULL DEFAULT '',
    settings_id TEXT   NOT NULL DEFAULT '',
    node_key   TEXT    NOT NULL DEFAULT '',
    instance_id TEXT   NOT NULL DEFAULT '',
    questions  TEXT    NOT NULL DEFAULT '[]',
    messages   TEXT    NOT NULL DEFAULT '[]',
    data       TEXT    NOT NULL DEFAULT '{}',
    nexts      TEXT    NOT NULL DEFAULT '[]',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    closed_at  INTEGER NOT NULL DEFAULT 0
);

-- Node settings profiles. A reusable, named bag of key/value config bound to a
-- node (identified by `node_uniq_id` — the node kind/plugin identity, shared by
-- every instance of that node). A node may have several profiles (e.g. one URL +
-- token per environment); a canvas node instance selects one by id. `settings`
-- is a free-form JSON object. Managed through the REST API (full CRUD).
CREATE TABLE IF NOT EXISTS node_settings (
    id           TEXT    PRIMARY KEY,
    node_uniq_id TEXT    NOT NULL DEFAULT '',
    node_type    TEXT    NOT NULL DEFAULT '',
    title        TEXT    NOT NULL DEFAULT '',
    settings     TEXT    NOT NULL DEFAULT '{}',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

-- Process runs. One row per workflow execution launched on the inflow engine.
-- Created (upserted) by the inflow layer when a process request is sent — never
-- via a plain REST create — and closed out from the engine's `inflow.event.log`
-- proc.finish event. `request` is the ProcessRequest snapshot sent to the engine
-- and `meta` is a free-form object (e.g. the next-node list captured when a
-- human-in-the-loop node parks the run, which the resume run is entered on, and
-- `startNodeIds` — the full entry set, of which `start_node_id` is the first).
--
-- `index_id` is the *indexId*: an auto-increment integer that is the row's
-- identity and is echoed into the ProcessRequest meta (and so travels as a
-- header on the services a run calls), addressing a run independently of its
-- `pid`. A single `pid` can back several rows over its lifetime — a
-- human-in-the-loop pause finishes one row as `waiting`, and the human's answer
-- starts a fresh row that resumes the same `pid` — so `pid` alone does not
-- identify a row; the index does.
CREATE TABLE IF NOT EXISTS processes (
    index_id      INTEGER PRIMARY KEY AUTOINCREMENT,
    pid           TEXT    NOT NULL DEFAULT '',
    instance_id   TEXT    NOT NULL DEFAULT '',
    flow_id       TEXT    NOT NULL DEFAULT '',
    context_id    TEXT    NOT NULL DEFAULT '',
    start_node_id TEXT    NOT NULL DEFAULT '',
    status        TEXT    NOT NULL DEFAULT 'running',
    resource_url  TEXT    NOT NULL DEFAULT '',
    request       TEXT    NOT NULL DEFAULT '{}',
    meta          TEXT    NOT NULL DEFAULT '{}',
    -- Run-end traversal snapshot (engine "_sched": flowSig/traverse/joinGen),
    -- captured per run so a continuation (Continue After, HITL resume) can seed it,
    -- and available to render what the run traversed.
    snapshot      TEXT    NOT NULL DEFAULT '{}',
    error         TEXT    NOT NULL DEFAULT '',
    scheduled_at  INTEGER NOT NULL DEFAULT 0,
    started_at    INTEGER NOT NULL DEFAULT 0,
    finished_at   INTEGER NOT NULL DEFAULT 0,
    duration_ms   INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

-- Palette extensions. Every row is one node in the canvas palette. `kind`
-- separates admin-managed builtins (seeded on first run; UI hard-coded in the
-- front end) from user-imported inflowv1 plugins (`extension`, whose settings
-- form and actions are fetched live over NATS via `plugin_id`). `type` is the
-- generic inflow/palette node type it compiles to. `icon`, `params` (JSON-schema
-- form) and `bind_to` (extrinsic topic + values) are JSON objects. `install` is
-- the JSON "how to run it" spec for imported plugins (repo/ref/runtime + the
-- extra env they need), which the install endpoints render into a script and an
-- env file. `outbound` is the optional JSON array of declared branch ports for a
-- synced plugin action (title/tags/description); each becomes an output port on
-- the canvas node and its tags are stamped onto the edges drawn from it. Managed
-- through the REST API (full CRUD); builtins are additionally seeded
-- idempotently by name.
CREATE TABLE IF NOT EXISTS extensions (
    id          TEXT    PRIMARY KEY,
    kind        TEXT    NOT NULL DEFAULT 'extension',
    type        TEXT    NOT NULL DEFAULT 'plugin',
    name        TEXT    NOT NULL DEFAULT '',
    description TEXT    NOT NULL DEFAULT '',
    plugin_id   TEXT    NOT NULL DEFAULT '',
    icon        TEXT    NOT NULL DEFAULT '{}',
    params      TEXT    NOT NULL DEFAULT '{}',
    bind_to     TEXT    NOT NULL DEFAULT '{}',
    install     TEXT    NOT NULL DEFAULT '{}',
    action      TEXT    NOT NULL DEFAULT '',
    parent_id   TEXT    NOT NULL DEFAULT '',
    outbound    TEXT    NOT NULL DEFAULT '[]',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- Workflow triggers: inbound webhooks and recurring schedules that launch a
-- flow. Kept apart from the flow graph. `config` is the kind-specific JSON
-- (webhook: methods/auth/whitelist incl. the secret; schedule: mode/cron/
-- interval/timezone); `cron_effective` is the single spec the scheduler arms on;
-- `hits` is the bounded webhook delivery log.
CREATE TABLE IF NOT EXISTS triggers (
    id             TEXT    PRIMARY KEY,
    kind           TEXT    NOT NULL DEFAULT '',
    flow_id        TEXT    NOT NULL DEFAULT '',
    start_node_id  TEXT    NOT NULL DEFAULT '',
    title          TEXT    NOT NULL DEFAULT '',
    enabled        INTEGER NOT NULL DEFAULT 1,
    slug           TEXT    NOT NULL DEFAULT '',
    config         TEXT    NOT NULL DEFAULT '{}',
    cron_effective TEXT    NOT NULL DEFAULT '',
    last_at        INTEGER NOT NULL DEFAULT 0,
    hits           TEXT    NOT NULL DEFAULT '[]',
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_workflows_updated_at ON workflows (updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_contexts_updated_at ON contexts (updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_memory_updated_at ON memory_stores (updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_prompts_updated_at ON prompts (updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_human_tasks_updated_at ON human_tasks (updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_node_settings_updated_at ON node_settings (updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_node_settings_node ON node_settings (node_uniq_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_processes_updated_at ON processes (updated_at DESC, index_id DESC);
CREATE INDEX IF NOT EXISTS idx_processes_pid ON processes (pid, status);
CREATE INDEX IF NOT EXISTS idx_processes_scheduled ON processes (status, scheduled_at);
CREATE INDEX IF NOT EXISTS idx_extensions_updated_at ON extensions (updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_extensions_kind ON extensions (kind, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_extensions_builtin_name ON extensions (kind, name);
CREATE UNIQUE INDEX IF NOT EXISTS idx_triggers_slug ON triggers (slug) WHERE slug != '';
CREATE INDEX IF NOT EXISTS idx_triggers_flow ON triggers (flow_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_triggers_schedule ON triggers (kind, enabled);
