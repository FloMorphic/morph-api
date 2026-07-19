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

CREATE INDEX IF NOT EXISTS idx_workflows_updated_at ON workflows (updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_contexts_updated_at ON contexts (updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_memory_updated_at ON memory_stores (updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_prompts_updated_at ON prompts (updated_at DESC, id DESC);
