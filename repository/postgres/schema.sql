-- Schema for the postgres backend. Mirrors repository/sqlite/schema.sql table
-- for table and column for column, so a deployment can move between the two
-- without any entity changing shape. The differences are only the ones postgres
-- forces:
--
--   * INTEGER epoch-millis timestamps become BIGINT;
--   * the sqlite 0/1 flags (`enabled`, `is_default`) become BOOLEAN;
--   * `processes.index_id` becomes BIGSERIAL (sqlite: INTEGER AUTOINCREMENT).
--
-- The free-form payload columns (view_flow, context, header, params, configs, …)
-- stay TEXT holding JSON, exactly as in sqlite: the repository marshals to and
-- from the domain models, so nothing in the application depends on the database
-- being able to parse them. Document-store tables are the deliberate exception
-- — their `data` column is JSONB, because those rows are what a workflow's own
-- read queries select from (see memory.go).
--
-- Vector similarity indexes are intentionally absent: embeddings stay in
-- sqlite-vec even on a postgres deployment (see repository/sqlite.VectorIndex
-- and the vector sidecar in db.go), so there is no pgvector requirement.

CREATE TABLE IF NOT EXISTS workflows (
    id         TEXT   PRIMARY KEY,
    title      TEXT   NOT NULL DEFAULT '',
    view_flow  TEXT   NOT NULL DEFAULT '{}',
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS contexts (
    id                 TEXT   PRIMARY KEY,
    title              TEXT   NOT NULL DEFAULT '',
    context            TEXT   NOT NULL DEFAULT '{}',
    header             TEXT   NOT NULL DEFAULT '{}',
    updated_by_type    TEXT   NOT NULL DEFAULT 'manual',
    updated_by_address TEXT   NOT NULL DEFAULT '',
    created_at         BIGINT NOT NULL,
    updated_at         BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS memory_stores (
    id              TEXT   PRIMARY KEY,
    name            TEXT   NOT NULL DEFAULT '',
    type            TEXT   NOT NULL,
    description     TEXT   NOT NULL DEFAULT '',
    vector_config   TEXT,
    document_config TEXT,
    created_at      BIGINT NOT NULL,
    updated_at      BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS prompts (
    id          TEXT   PRIMARY KEY,
    title       TEXT   NOT NULL DEFAULT '',
    description TEXT   NOT NULL DEFAULT '',
    template    TEXT   NOT NULL DEFAULT '',
    variables   TEXT   NOT NULL DEFAULT '[]',
    tags        TEXT   NOT NULL DEFAULT '[]',
    created_at  BIGINT NOT NULL,
    updated_at  BIGINT NOT NULL
);

-- Human-in-the-Loop tasks. Created by the inflow svc handler when a workflow
-- reaches a `humanInLoop` node (never via the REST API).
CREATE TABLE IF NOT EXISTS human_tasks (
    id          TEXT   PRIMARY KEY,
    title       TEXT   NOT NULL DEFAULT '',
    status      TEXT   NOT NULL DEFAULT 'open',
    pid         TEXT   NOT NULL DEFAULT '',
    flow_id     TEXT   NOT NULL DEFAULT '',
    node_id     TEXT   NOT NULL DEFAULT '',
    context_id  TEXT   NOT NULL DEFAULT '',
    mode        TEXT   NOT NULL DEFAULT 'park',
    channel     TEXT   NOT NULL DEFAULT 'direct',
    prompt      TEXT   NOT NULL DEFAULT '',
    settings_id TEXT   NOT NULL DEFAULT '',
    node_key    TEXT   NOT NULL DEFAULT '',
    instance_id TEXT   NOT NULL DEFAULT '',
    telegram    TEXT   NOT NULL DEFAULT '{}',
    questions   TEXT   NOT NULL DEFAULT '[]',
    messages    TEXT   NOT NULL DEFAULT '[]',
    data        TEXT   NOT NULL DEFAULT '{}',
    nexts       TEXT   NOT NULL DEFAULT '[]',
    created_at  BIGINT NOT NULL,
    updated_at  BIGINT NOT NULL,
    closed_at   BIGINT NOT NULL DEFAULT 0
);

-- Node settings profiles: a reusable, named bag of key/value config bound to a
-- node kind / plugin identity (`node_uniq_id`).
CREATE TABLE IF NOT EXISTS node_settings (
    id           TEXT   PRIMARY KEY,
    node_uniq_id TEXT   NOT NULL DEFAULT '',
    node_type    TEXT   NOT NULL DEFAULT '',
    title        TEXT   NOT NULL DEFAULT '',
    settings     TEXT   NOT NULL DEFAULT '{}',
    created_at   BIGINT NOT NULL,
    updated_at   BIGINT NOT NULL
);

-- Process runs. One row per workflow execution launched on the inflow engine.
-- `index_id` is the *indexId* echoed into the ProcessRequest meta: the row's
-- identity, independent of `pid` (several rows can share one pid across a
-- park→resume chain).
CREATE TABLE IF NOT EXISTS processes (
    index_id      BIGSERIAL PRIMARY KEY,
    pid           TEXT   NOT NULL DEFAULT '',
    instance_id   TEXT   NOT NULL DEFAULT '',
    flow_id       TEXT   NOT NULL DEFAULT '',
    context_id    TEXT   NOT NULL DEFAULT '',
    start_node_id TEXT   NOT NULL DEFAULT '',
    status        TEXT   NOT NULL DEFAULT 'running',
    resource_url  TEXT   NOT NULL DEFAULT '',
    request       TEXT   NOT NULL DEFAULT '{}',
    meta          TEXT   NOT NULL DEFAULT '{}',
    snapshot      TEXT   NOT NULL DEFAULT '{}',
    errors        TEXT   NOT NULL DEFAULT '{}',
    error         TEXT   NOT NULL DEFAULT '',
    scheduled_at  BIGINT NOT NULL DEFAULT 0,
    started_at    BIGINT NOT NULL DEFAULT 0,
    finished_at   BIGINT NOT NULL DEFAULT 0,
    duration_ms   BIGINT NOT NULL DEFAULT 0,
    created_at    BIGINT NOT NULL,
    updated_at    BIGINT NOT NULL
);

-- Palette extensions: every row is one node in the canvas palette.
CREATE TABLE IF NOT EXISTS extensions (
    id          TEXT   PRIMARY KEY,
    kind        TEXT   NOT NULL DEFAULT 'extension',
    type        TEXT   NOT NULL DEFAULT 'plugin',
    name        TEXT   NOT NULL DEFAULT '',
    description TEXT   NOT NULL DEFAULT '',
    plugin_id   TEXT   NOT NULL DEFAULT '',
    icon        TEXT   NOT NULL DEFAULT '{}',
    params      TEXT   NOT NULL DEFAULT '{}',
    bind_to     TEXT   NOT NULL DEFAULT '{}',
    install     TEXT   NOT NULL DEFAULT '{}',
    action      TEXT   NOT NULL DEFAULT '',
    parent_id   TEXT   NOT NULL DEFAULT '',
    outbound    TEXT   NOT NULL DEFAULT '[]',
    created_at  BIGINT NOT NULL,
    updated_at  BIGINT NOT NULL
);

-- Workflow triggers: inbound webhooks and recurring schedules that launch a
-- flow. `config` carries the kind-specific JSON (including the webhook secret).
CREATE TABLE IF NOT EXISTS triggers (
    id             TEXT    PRIMARY KEY,
    kind           TEXT    NOT NULL DEFAULT '',
    flow_id        TEXT    NOT NULL DEFAULT '',
    start_node_id  TEXT    NOT NULL DEFAULT '',
    title          TEXT    NOT NULL DEFAULT '',
    enabled        BOOLEAN NOT NULL DEFAULT TRUE,
    slug           TEXT    NOT NULL DEFAULT '',
    config         TEXT    NOT NULL DEFAULT '{}',
    cron_effective TEXT    NOT NULL DEFAULT '',
    last_at        BIGINT  NOT NULL DEFAULT 0,
    hits           TEXT    NOT NULL DEFAULT '[]',
    created_at     BIGINT  NOT NULL,
    updated_at     BIGINT  NOT NULL
);

-- OpenConnector connections: the central auth gateways FloMorphic reaches
-- external SaaS providers through.
CREATE TABLE IF NOT EXISTS connect_connections (
    id          TEXT    PRIMARY KEY,
    label       TEXT    NOT NULL DEFAULT '',
    base_url    TEXT    NOT NULL DEFAULT '',
    token       TEXT    NOT NULL DEFAULT '',
    admin_token TEXT    NOT NULL DEFAULT '',
    kind        TEXT    NOT NULL DEFAULT '',
    is_default  BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  BIGINT  NOT NULL,
    updated_at  BIGINT  NOT NULL
);

-- Telegram recipients a HITL session can be sent to: the chats one connected
-- bot is known to be able to reach. Scoped per (connection, alias).
CREATE TABLE IF NOT EXISTS telegram_recipients (
    id           TEXT   PRIMARY KEY,
    connection   TEXT   NOT NULL DEFAULT '',
    alias        TEXT   NOT NULL DEFAULT '',
    chat_id      TEXT   NOT NULL DEFAULT '',
    type         TEXT   NOT NULL DEFAULT '',
    title        TEXT   NOT NULL DEFAULT '',
    username     TEXT   NOT NULL DEFAULT '',
    first_name   TEXT   NOT NULL DEFAULT '',
    last_name    TEXT   NOT NULL DEFAULT '',
    last_seen_at BIGINT NOT NULL DEFAULT 0,
    created_at   BIGINT NOT NULL,
    updated_at   BIGINT NOT NULL
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
CREATE UNIQUE INDEX IF NOT EXISTS idx_triggers_slug ON triggers (slug) WHERE slug <> '';
CREATE INDEX IF NOT EXISTS idx_triggers_flow ON triggers (flow_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_triggers_schedule ON triggers (kind, enabled);
CREATE INDEX IF NOT EXISTS idx_connect_updated_at ON connect_connections (updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_telegram_recipients_scope ON telegram_recipients (connection, alias, last_seen_at DESC);
