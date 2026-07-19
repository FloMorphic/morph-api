# FloMorphic API (`morph-api`)

Backend for [FloMorphic](../flomorphic-wapp) — the visual workflow builder for
AI-native systems, built on the Inflowenger context runtime. It exposes CRUD for
the core entities the editor works with and bridges saved workflows/contexts to
the `inflow-fusion` runtime.

The HTTP layer follows the [`inspector-api`](../../inspector-api) conventions
(Fiber v3, `RegisterAll` mounting one route group per entity, a `{ data, error }`
envelope). Persistence is **SQLite via [sqlc](https://sqlc.dev)** with
[sqlite-vec](https://github.com/asg017/sqlite-vec) for vector indexing, behind a
database-agnostic repository interface.

## Entities & endpoints

Every response is wrapped in `{ "data": ..., "error": ... }`.

### Workflows — `FlowRecord`, page-paginated

| Method | Path              | Body / query                          |
| ------ | ----------------- | ------------------------------------- |
| POST   | `/flow`           | `FlowRecord` (no `id` ⇒ create)       |
| GET    | `/flow`           | `?page=1&per_page=12&search=`         |
| GET    | `/flow/id/:id`    | —                                     |
| DELETE | `/flow/id/:id`    | —                                     |

### Contexts — `ContextRecord`, page-paginated

| Method | Path              | Notes                                 |
| ------ | ----------------- | ------------------------------------- |
| POST   | `/context`        | `context` must be a JSON object       |
| GET    | `/context`        | `?page=1&per_page=12&search=`         |
| GET    | `/context/id/:id` | —                                     |
| DELETE | `/context/id/:id` | —                                     |

### Memory stores — `MemoryStore` (`vector` / `document`)

| Method | Path           | Notes                                       |
| ------ | -------------- | ------------------------------------------- |
| GET    | `/memory`      | plain array (small, un-paginated)           |
| POST   | `/memory`      | vector store ⇒ a sqlite-vec index is built  |
| GET    | `/memory/:id`  | —                                           |
| DELETE | `/memory/:id`  | vector store ⇒ its index is dropped         |

### Prompts — `PromptRecord` (templates), page-paginated

| Method | Path             | Notes                                        |
| ------ | ---------------- | -------------------------------------------- |
| POST   | `/prompt`        | `template` required; `{{var}}` placeholders  |
| GET    | `/prompt`        | `?page=1&per_page=12&search=` (title + desc) |
| GET    | `/prompt/id/:id` | —                                            |
| DELETE | `/prompt/id/:id` | —                                            |

Plus `GET /health`.

**Pagination.** List endpoints are page-based: `?page` (1-based) and `?per_page`
(default 12, max 100), returning `{ list, total, page, per_page, total_pages }`.
The count comes straight from SQL, so no cursor bookkeeping is needed.

## Architecture

```
app.go                     entrypoint: open store, (optional) inflow, mount routes
api/                       HTTP controllers — one folder per entity
  init.go                  RegisterAll(app, store)
  workflow/ context/ memory/ prompt/
models/                    wire types, kept 1:1 with flomorphic-wapp's src/types/api.ts
repository/                persistence CONTRACT (interfaces) + driver registry
  repository.go            Store, {Workflow,Context,Memory,Prompt}Repository, Register/Open
  sqlite/                  the sqlite driver (registers itself as "sqlite")
    schema.sql             DDL (source of truth for migrations + sqlc)
    queries/*.sql          sqlc queries
    sqlcgen/               generated code (do not edit)
    *.go                   adapters: sqlc rows <-> domain models, sqlite-vec index
    cdeps/sqlite3.h        vendored header for the cgo build (see below)
inflow/                    inflow-fusion wiring (compiler + backend contract)
etc/                       Send envelope helper, JWT middleware
```

### Multi-database support

Controllers depend only on the interfaces in `repository` — never on SQLite. A
backend implements `repository.Store` and registers itself:

```go
func init() { repository.Register("sqlite", Open) }
```

`repository.Open(driver, source)` then returns the right `Store`, selected by the
`DB_DRIVER` env var. Adding e.g. Postgres means a new `repository/postgres`
package (its own sqlc config against the same `models`) and importing it for its
`init()` — no changes to the API, controllers, or models.

### Vector indexing (sqlite-vec)

The connection auto-loads sqlite-vec (`sqlite_vec.Auto()`), verified with
`vec_version()` at startup. Creating a **vector** memory store provisions a
companion `vec0` virtual table sized to the store's `dimensions`; deleting the
store drops it. Document stores persist only their table/column schema.

## Running

Requires Go 1.26+, a C compiler (cgo), and — for regenerating queries — `sqlc`.

```sh
cp .env.example .env      # optional; sensible defaults otherwise
make run                  # or: make build && ./flomorphic-api
```

`make` sets `CGO_CFLAGS` to the vendored `sqlite3.h` so the sqlite-vec build
finds the header without a system `libsqlite3-dev`. To build by hand:

```sh
CGO_ENABLED=1 CGO_CFLAGS="-I$PWD/repository/sqlite/cdeps" go build .
```

### Configuration (env / `.env`)

| Variable                  | Default         | Purpose                                        |
| ------------------------- | --------------- | ---------------------------------------------- |
| `PORT`                    | `8025`          | HTTP listen port                               |
| `DB_DRIVER`               | `sqlite`        | Repository backend                             |
| `DB_SOURCE`               | `db/flomorphic.db` | sqlite file path (parent dir auto-created)   |
| `AUTH_ENABLED`            | `false`         | Require an HS256 bearer token on CRUD routes   |
| `API_JWT_SECRET`          | —               | HS256 secret (falls back to the infra secret)  |
| `INFLOW_INFRA_API`        | —               | inflow infra API base; unset ⇒ runtime disabled|
| `INFLOW_INFRA_JWT_SECRET` | —               | shared secret for the inflow runtime           |

The inflow runtime is **optional**: with `INFLOW_INFRA_API` unset the server runs
CRUD-only (matching the web app's standalone mode). Auth is off by default so the
web app works without a token.

## Development

```sh
make sqlc     # regenerate repository/sqlite/sqlcgen from schema.sql + queries/
make test
make tidy
```

After editing `schema.sql` or any `queries/*.sql`, run `make sqlc`.
