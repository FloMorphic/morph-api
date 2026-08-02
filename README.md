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

### Human Tasks — `HumanTask` (Human-in-the-Loop), page-paginated

Created only by the inflow `hitl` svc handler when a workflow reaches a
`humanInLoop` node — **there is deliberately no create/upsert route**. `Upsert`
lives in the repository, not the API.

| Method | Path                     | Notes                                            |
| ------ | ------------------------ | ------------------------------------------------ |
| GET    | `/hitl`                  | `?page=1&per_page=12&search=&status=open`        |
| GET    | `/hitl/id/:id`           | open the task (conversation)                     |
| POST   | `/hitl/id/:id/answer`    | `{ questionId, answer }` — answers a question    |
| POST   | `/hitl/id/:id/message`   | `{ role, text }` — appends a chat turn           |
| POST   | `/hitl/id/:id/close`     | force-finish (workflow ends at this step)        |
| DELETE | `/hitl/id/:id`           | —                                                |

A task flips `open → answered` once every question has an answer; `close` sets
`closed`. `status` filter accepts `open` / `answered` / `closed`.

### Node settings — `NodeSetting` (settings profiles), page-paginated

A reusable, named key/value config bound to a node — identified by `nodeUniqId`,
the node kind / plugin identity shared by every instance of that node. A node may
own several profiles (e.g. a distinct URL + token per environment); a canvas node
instance references one by id. `settings` is a free-form JSON object. `nodeType`
records the node's kind (e.g. `llm`, `plugin`) — set by the frontend, not the
user — so a profile carries what kind of node it belongs to.

| Method | Path               | Notes                                              |
| ------ | ------------------ | -------------------------------------------------- |
| POST   | `/settings`        | `NodeSetting` (no `id` ⇒ create); `nodeUniqId` req |
| GET    | `/settings`        | `?page=1&per_page=12&search=&node=<nodeUniqId>`    |
| GET    | `/settings/id/:id` | —                                                  |
| DELETE | `/settings/id/:id` | —                                                  |

The `node` query filter scopes the list to one node's profiles (used by the node
drawer's profile selector).

### Extensions — `ExtensionRecord` (the node palette), page-paginated

Every row is one palette node. `kind` separates admin-managed **builtins**
(seeded on first run, UI hard-coded in the front end) from user-imported
**extensions** — inflowv1 plugins, whose settings form and actions are read live
over NATS via `pluginId` rather than stored.

| Method | Path                                    | Notes                                            |
| ------ | --------------------------------------- | ------------------------------------------------ |
| POST   | `/extension`                            | `ExtensionRecord` (no `id` ⇒ create)             |
| GET    | `/extension`                            | `?page=1&per_page=12&search=&kind=`              |
| GET    | `/extension/extrinsics`                 | backend extrinsic services an extrinsic can bind |
| GET    | `/extension/id/:id`                     | —                                                |
| DELETE | `/extension/id/:id`                     | —                                                |
| GET    | `/extension/id/:id/intro`               | live: plugin `@intro`                            |
| GET    | `/extension/id/:id/settings`            | live: plugin `@settings` form                    |
| GET    | `/extension/id/:id/actions`             | live: plugin `@actions`                          |
| GET    | `/extension/id/:id/actions/:method/form`| live: one action's `@form`                       |
| POST   | `/extension/id/:id/:fn`                 | live: call `inflow.v1.<pluginId>.<fn>` (`:id` is the **plugin id**) |
| POST   | `/extension/plugin/cred`                | mint a plugin runtime credential                 |
| GET    | `/extension/id/:id/install`             | one-liner + script + env to install from source  |
| GET    | `/extension/id/:id/install.sh`          | the installer itself (text/plain, for `curl \| bash`) |
| GET    | `/extension/id/:id/env`                 | just the dotenv, for a checkout you already have |
| POST   | `/extension/id/:id/sync`                | rebuild this plugin's palette rows from `@actions`|

**Getting a third-party plugin running.** A plugin is an independent process the
user runs wherever they like; all this API needs is for it to reach Infra under
an id it knows. So onboarding is *register the row, then hand back what it takes
to run it* — two paths, both driven from the web app's Extensions page:

1. **From source.** The row carries an `install` spec (`repo`, `ref`, `subdir`,
   `runtime`, `envFile`, `env`). `GET …/install` answers with a one-liner that
   pipes `…/install.sh` into bash; the script clones the repo, writes the dotenv
   with a freshly minted credential, builds (`go` / `node` / `docker`, or detected)
   and starts the plugin in a directory the user names.
2. **Bring your own checkout.** `GET …/env` answers with only the dotenv —
   `PLUGIN_ID` / `INFRA_URL` / `INFRA_CRED` plus the row's declared extras — which
   is all a plugin needs to come up.

Nothing is cloned, built or executed here: the API renders text the user runs.
Both responses embed a plugin-scoped credential, so they are secret-bearing in
exactly the way `POST /extension/plugin/cred` already is — put the API behind
`AUTH_ENABLED` on any shared deployment.

**From a running plugin to palette nodes.** A plugin describes itself over
inflowv1 and none of it is stored, because the plugin is the only authority on
what it can do and can be redeployed at any time. `POST …/sync` is the one place
that copies any of it into the database, and it does so as a **replacement**:
every row derived from that plugin is deleted, then one row per live action is
written (carrying the action's method, icon and form). That is what makes a
method the plugin dropped disappear from the palette instead of lingering as a
node that no longer resolves. The plugin's own registration row is never touched.

Those derived rows are `action` + `parentId` rows in the same table; the web app
lists them as a searchable *Plugins* tab in the canvas palette, and each drags out
as a `plugin` node that compiles to `request = <action>`.

> The settings form from `@intro` is deliberately **not** stored: it is the shape
> of a settings profile, and profiles live under `/settings` keyed by plugin id.
>
> Note that the Go plugin SDK through **v0.1.3 never answers `@intro`** — its
> handler marshals the `Intro` *method* instead of the intro field, so the reply
> never arrives. Sync therefore treats `@intro` as best-effort and requires only
> `@actions`, and the web app probes liveness with `@actions` and falls back to
> `@settings` for a plugin's requirements.

Plus `GET /health`.

**Pagination.** List endpoints are page-based: `?page` (1-based) and `?per_page`
(default 12, max 100), returning `{ list, total, page, per_page, total_pages }`.
The count comes straight from SQL, so no cursor bookkeeping is needed.

## Architecture

```
app.go                     entrypoint: open store, (optional) inflow, mount routes
api/                       HTTP controllers — one folder per entity
  init.go                  RegisterAll(app, store)
  workflow/ context/ memory/ prompt/ hitl/ settings/
models/                    wire types, kept 1:1 with flomorphic-wapp's src/types/api.ts
repository/                persistence CONTRACT (interfaces) + driver registry
  repository.go            Store, {Workflow,Context,Memory,Prompt,HumanTask}Repository, Register/Open
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

**On musl (Alpine, and therefore most containers)** sqlite-vec does not compile
as-is: its amalgamation does `typedef u_int8_t uint8_t;` for every non-Windows
target, and `u_int*_t` is a BSD/glibc spelling musl lacks, so the typedefs
collapse to implicit `int`. Map them back to the C99 names — a no-op on glibc:

```sh
make build CGO_CFLAGS="-I$PWD/repository/sqlite/cdeps \
  -Du_int8_t=uint8_t -Du_int16_t=uint16_t -Du_int64_t=uint64_t"
```

The Dockerfile does this for you (`MUSL_CFLAGS`).

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
| `PLUGIN_INFRA_URL`        | runtime's NATS  | `INFRA_URL` written into generated plugin envs |
| `PUBLIC_API_URL`          | request origin  | base URL used in the plugin install one-liner  |

`PLUGIN_INFRA_URL` matters when plugins run outside the compose network: the
address this API reaches Infra on (`infra:4222`) is not the one a plugin on
someone's laptop can dial, so set the published endpoint there. `PUBLIC_API_URL`
is only needed when a proxy rewrites `Host`.

The inflow runtime is **optional**: with `INFLOW_INFRA_API` unset the server runs
CRUD-only (matching the web app's standalone mode). Auth is off by default so the
web app works without a token.

### In Docker

```sh
docker build -t flomorphic-api:local .
docker run --rm -p 8025:8025 --network inflow_net \
  -e INFLOW_INFRA_API=http://inflow-infra:8022 \
  -e INFLOW_INFRA_JWT_SECRET=<Infra API Secret Key> \
  -e DB_SOURCE=/data/flomorphic.db -v "$PWD/data:/data" \
  flomorphic-api:local
```

The image also **starts the builtin plugin nodes**, which is not a convenience —
it is where the dependency actually points. An inflow plugin needs a NATS
credential on the builtin-plugins account, and the component that mints one is
this API (`POST /extension/plugin/cred`). So the entrypoint starts the server,
waits for `/health`, mints one multi-access credential, and runs every plugin
folder in `$PLUGINS_REPO` with it — each under the `PLUGIN_ID` this API's seed
(`repository/sqlite/seed/builtins.json`) assigns to its builtin node, so saved
workflows keep resolving across reinstalls.

| Variable           | Default                                             | Purpose                                         |
| ------------------ | --------------------------------------------------- | ----------------------------------------------- |
| `PLUGINS_ENABLED`  | `1`                                                 | `0` ⇒ a plain API container                     |
| `PLUGINS_REPO`     | `https://github.com/FloMorphic/builtin-plugins.git` | repo the plugin binaries come from              |
| `PLUGINS_REF`      | `main`                                              | branch/tag; changing it rebuilds them at start  |
| `PLUGIN_ID_<NAME>` | —                                                   | override one folder's id (e.g. `PLUGIN_ID_LLM`) |
| `PLUGIN_INFRA_URL` | derived from `INFLOW_INFRA_API` + `:4222`           | NATS endpoint (`host:port`, no scheme)          |
| `GOPROXY`          | `https://proxy.golang.org,direct`                   | module proxy for the build and for a runtime plugin rebuild |

> **Build fails with a 403 while downloading modules?** `proxy.golang.org`
> redirects module zips to `storage.googleapis.com`, and networks that block that
> host fail mid-download. Use a mirror:
> `docker build --build-arg GOPROXY=https://goproxy.cn,direct -t flomorphic-api:local .`
> (the same value works as `GOPROXY=…` for a plain `make build`).

For the whole product in one container — this API, the canvas and one nginx in
front — see [FloMorphic/getting-started](https://github.com/FloMorphic/getting-started).

## Development

```sh
make sqlc     # regenerate repository/sqlite/sqlcgen from schema.sql + queries/
make test
make tidy
```

After editing `schema.sql` or any `queries/*.sql`, run `make sqlc`.
