# syntax=docker/dockerfile:1
#
# FloMorphic API image — flomorphic-api plus the builtin plugin nodes it
# credentials.
#
# Two things make this image less ordinary than it looks:
#
#  1. cgo. The sqlite driver links mattn/go-sqlite3 and compiles sqlite-vec, so
#     the build needs a C toolchain and goes through the Makefile (which points
#     cgo at the vendored sqlite3.h). The binary is musl-linked, hence an alpine
#     runtime of the same family.
#
#  2. It starts the plugin nodes. An inflow plugin needs a NATS credential on the
#     builtin-plugins account, and the component that mints one is this very API
#     (POST /extension/plugin/cred). So the entrypoint starts the API, waits for
#     /health, mints ONE multi-access credential, and runs every plugin in
#     $PLUGINS_REPO with it — each under the PLUGIN_ID the API's seed assigns to
#     its builtin node, so saved workflows keep resolving across reinstalls.
#     Set PLUGINS_ENABLED=0 for a plain API container.
#
# Build (context is this repo):
#   docker build -t flomorphic-api:local .
#   docker run --rm -p 8025:8025 --network inflow_net \
#     -e INFLOW_INFRA_API=http://inflow-infra:8022 \
#     -e INFLOW_INFRA_JWT_SECRET=<Infra API Secret Key> \
#     -v $PWD/data:/data flomorphic-api:local
#
# For the full product in one container — this API, the canvas, and one nginx in
# front — see FloMorphic/getting-started (docker/Dockerfile.flomorphic).

ARG PLUGINS_REPO=https://github.com/FloMorphic/builtin-plugins.git
ARG PLUGINS_REF=main

# Go module proxy, overridable because the default CDN is not reachable
# everywhere: proxy.golang.org redirects module zips to storage.googleapis.com,
# and networks that block it answer 403 mid-download. Point it at a mirror then:
#   docker build --build-arg GOPROXY=https://goproxy.cn,direct -t flomorphic-api:local .
ARG GOPROXY="https://proxy.golang.org,direct"

# ── the API ───────────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS api-build
ARG GOPROXY
# sqlite-vec's amalgamation contains, for every non-Windows/wasm target:
#   typedef u_int8_t uint8_t;  (and u_int16_t / u_int64_t)
# `u_int*_t` is a BSD/glibc spelling that musl does not define, so on Alpine the
# typedefs collapse to implicit int and the file fails to compile. Mapping them
# to the C99 names makes those lines legal self-typedefs; on glibc it is a no-op.
# Drop this the day sqlite-vec guards that block on __GLIBC__.
ARG MUSL_CFLAGS="-Du_int8_t=uint8_t -Du_int16_t=uint16_t -Du_int64_t=uint64_t"
RUN apk add --no-cache git make build-base
WORKDIR /src
# Modules first: they only change when go.mod/go.sum do, so the download layer
# survives ordinary source edits.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO_CFLAGS on the command line overrides the Makefile's own assignment, so the
# vendored sqlite3.h include has to be restated alongside the musl fix.
RUN make build BINARY=/out/flomorphic-api \
      CGO_CFLAGS="-I/src/repository/sqlite/cdeps $MUSL_CFLAGS"

# ── the builtin plugin nodes (pure Go, one binary per folder) ─────────────────
FROM golang:1.26-alpine AS plugins-build
ARG PLUGINS_REPO
ARG PLUGINS_REF
ARG GOPROXY
RUN apk add --no-cache git
RUN git clone --depth 1 -b "$PLUGINS_REF" "$PLUGINS_REPO" /src/plugins
# Every top-level folder with a go.mod is a plugin, so a new plugin in that repo
# needs no change here.
RUN mkdir -p /out/bin && cd /src/plugins && \
    for d in */; do \
      [ -f "$d/go.mod" ] || continue; \
      n="${d%/}"; \
      echo "building plugin $n"; \
      ( cd "$d" && CGO_ENABLED=0 go build -trimpath -o "/out/bin/$n" . ); \
    done && \
    printf '%s@%s' "$PLUGINS_REPO" "$PLUGINS_REF" > /out/bin/.ref

# ── runtime ───────────────────────────────────────────────────────────────────
#
# A Go image rather than bare alpine, deliberately: PLUGINS_REPO / PLUGINS_REF
# can be repointed at run time and the entrypoint then rebuilds the plugin
# binaries in place. Without a toolchain that extension point disappears.
FROM golang:1.26-alpine
RUN apk add --no-cache git curl jq openssl ca-certificates tzdata

COPY --from=api-build     /out/flomorphic-api /app/flomorphic-api
COPY --from=plugins-build /out/bin            /app/plugins
# The seed carries each builtin node's hard-coded PLUGIN_ID; keep a copy so the
# entrypoint can resolve ids even before the API answers.
COPY --from=api-build     /src/repository/sqlite/seed /app/seed

ARG PLUGINS_REPO
ARG PLUGINS_REF
ARG GOPROXY
# Carried into the container so a runtime plugin rebuild (a changed PLUGINS_REF)
# uses the same reachable proxy the image was built with.
ENV GOPROXY=${GOPROXY} \
    PORT=8025 \
    DB_SOURCE=/data/flomorphic.db \
    MCP_ENABLED=true \
    APP_DIR=/app \
    PLUGIN_BIN_DIR=/app/plugins \
    PLUGIN_SRC_DIR=/src/plugins \
    SEED_FILE=/app/seed/builtins.json \
    PLUGINS_ENABLED=1 \
    PLUGINS_REPO=${PLUGINS_REPO} \
    PLUGINS_REF=${PLUGINS_REF} \
    PLUGIN_CRED_NAME=flomorphic-builtins \
    PLUGIN_NATS_PORT=4222

COPY <<'EOF' /usr/local/bin/entrypoint.sh
#!/bin/sh
set -eu
[ "$#" -gt 0 ] && exec "$@"

# stdout for all three: docker merges the streams with no ordering guarantee
# between them, so a warning sent to stderr surfaces out of sequence.
log()  { printf '[flomorphic-api] %s\n' "$*"; }
warn() { printf '[flomorphic-api] ! %s\n' "$*"; }
die()  { printf '[flomorphic-api] error: %s\n' "$*"; exit 1; }

is_true() {
    case "$(printf '%s' "${1:-}" | tr '[:upper:]' '[:lower:]')" in
        1|true|yes|on) return 0 ;; *) return 1 ;;
    esac
}

procs=""
track() { procs="$procs $1:$2"; }
stop_children() {
    for e in $procs; do kill -TERM "${e%%:*}" 2>/dev/null || true; done
    sleep 2
    for e in $procs; do kill -KILL "${e%%:*}" 2>/dev/null || true; done
}
trap 'log "shutting down"; stop_children; exit 0' INT TERM

# ── the API ───────────────────────────────────────────────────────────────────
mkdir -p "$(dirname "$DB_SOURCE")"
cd "$APP_DIR"
./flomorphic-api &
api_pid="$!"
track "$api_pid" flomorphic-api
log "starting on :$PORT (db: $DB_SOURCE)"

i=0
until curl -fsS "http://127.0.0.1:$PORT/health" >/dev/null 2>&1; do
    kill -0 "$api_pid" 2>/dev/null || die "flomorphic-api exited during startup"
    i=$((i + 1)); [ "$i" -gt 120 ] && die "no /health answer within 120s"
    sleep 1
done
log "ready"

# ── auth (only when this API's own gate is on) ────────────────────────────────
b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }
AUTH_TOKEN=""
if is_true "${AUTH_ENABLED:-}"; then
    secret="${API_JWT_SECRET:-${INFLOW_INFRA_JWT_SECRET:-}}"
    if [ -n "$secret" ]; then
        now="$(date +%s)"
        h="$(printf '%s' '{"alg":"HS256","typ":"JWT"}' | b64url)"
        p="$(printf '{"admin":true,"iat":%s,"exp":%s}' "$now" "$((now + 3600))" | b64url)"
        s="$(printf '%s.%s' "$h" "$p" | openssl dgst -sha256 -hmac "$secret" -binary | b64url)"
        AUTH_TOKEN="$h.$p.$s"
    else
        warn "AUTH_ENABLED is set but no secret is configured"
    fi
fi
api_curl() {
    if [ -n "$AUTH_TOKEN" ]; then curl -fsS -H "Authorization: Bearer $AUTH_TOKEN" "$@"
    else curl -fsS "$@"; fi
}

# ── the builtin plugin nodes ──────────────────────────────────────────────────
start_plugins() {
    is_true "${PLUGINS_ENABLED:-1}" || { log "plugin nodes disabled"; return 0; }
    if [ -z "${INFLOW_INFRA_API:-}" ]; then
        warn "INFLOW_INFRA_API is not set — CRUD-only mode, no plugin can be credentialed"
        return 0
    fi

    log "minting the shared builtin-plugins credential"
    cred="$(api_curl -X POST "http://127.0.0.1:$PORT/extension/plugin/cred" \
        -H 'Content-Type: application/json' \
        -d "$(printf '{"name":"%s","access":"multi"}' "$PLUGIN_CRED_NAME")" \
        | jq -er '.data.cred' || true)"
    [ -n "$cred" ] || { warn "could not mint a credential — is Infra reachable at $INFLOW_INFRA_API? skipping plugin nodes"; return 0; }

    if [ -n "${PLUGIN_INFRA_URL:-}" ]; then
        infra="$PLUGIN_INFRA_URL"
    else
        # The SDK prefixes nats:// itself, so this stays host:port. NATS lives on
        # the same host as Infra's REST API.
        host="$(printf '%s' "$INFLOW_INFRA_API" | sed -e 's|^[a-zA-Z][a-zA-Z0-9+.-]*://||' -e 's|/.*$||' -e 's|:[0-9]*$||')"
        infra="$host:$PLUGIN_NATS_PORT"
    fi
    log "plugin NATS endpoint: $infra"

    # Rebuild only when the requested repo@ref differs from what was baked in.
    want="$PLUGINS_REPO@$PLUGINS_REF"
    have="$(cat "$PLUGIN_BIN_DIR/.ref" 2>/dev/null || true)"
    if [ "$want" != "$have" ]; then
        log "plugin sources changed ($have -> $want) — rebuilding"
        # A failure here must not take the API down with it, so the rebuild runs
        # in a checked subshell and a bad ref simply means "no plugin nodes".
        if ! (
            set -e
            rm -rf "$PLUGIN_SRC_DIR"
            git clone --depth 1 -b "$PLUGINS_REF" "$PLUGINS_REPO" "$PLUGIN_SRC_DIR"
            mkdir -p "$PLUGIN_BIN_DIR"
            for d in "$PLUGIN_SRC_DIR"/*/; do
                [ -f "${d}go.mod" ] || continue
                n="$(basename "$d")"
                log "building plugin $n"
                ( cd "$d" && CGO_ENABLED=0 go build -trimpath -o "$PLUGIN_BIN_DIR/$n" . )
            done
            printf '%s' "$want" > "$PLUGIN_BIN_DIR/.ref"
        ); then
            warn "could not build the plugin binaries ($want) — continuing without the plugin nodes"
            return 0
        fi
    fi

    for bin in "$PLUGIN_BIN_DIR"/*; do
        [ -f "$bin" ] && [ -x "$bin" ] || continue
        n="$(basename "$bin")"
        case "$n" in .*) continue ;; esac

        # PLUGIN_ID precedence: explicit override -> the seeded builtin row ->
        # the seed file. Never invented: a workflow references it by value.
        var="PLUGIN_ID_$(printf '%s' "$n" | tr '[:lower:]-' '[:upper:]_')"
        eval "id=\${$var:-}"
        if [ -z "$id" ]; then
            id="$(api_curl "http://127.0.0.1:$PORT/extension?kind=builtin&per_page=100" 2>/dev/null \
                | jq -r --arg t "$n" '[.data.list[]? | select(.type == $t) | .pluginId // empty][0] // empty' 2>/dev/null || true)"
        fi
        if [ -z "$id" ] && [ -f "${SEED_FILE:-}" ]; then
            id="$(jq -r --arg t "$n" '[.[] | select(.type == $t) | .pluginId // empty][0] // empty' "$SEED_FILE" 2>/dev/null || true)"
        fi
        if [ -z "$id" ]; then
            warn "no PLUGIN_ID for '$n' — set $var to run it. Skipped."
            continue
        fi

        log "starting plugin node $n (PLUGIN_ID=$id)"
        ( cd "$PLUGIN_BIN_DIR" && PLUGIN_ID="$id" INFRA_CRED="$cred" INFRA_URL="$infra" exec "$bin" ) &
        track "$!" "plugin:$n"
    done
}
start_plugins

# ── supervise ─────────────────────────────────────────────────────────────────
while :; do
    for e in $procs; do
        if ! kill -0 "${e%%:*}" 2>/dev/null; then
            warn "${e#*:} (pid ${e%%:*}) exited — stopping the container"
            stop_children
            exit 1
        fi
    done
    sleep 5
done
EOF
RUN chmod +x /usr/local/bin/entrypoint.sh && mkdir -p /data

VOLUME ["/data"]
EXPOSE 8025
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 \
  CMD curl -fsS http://127.0.0.1:8025/health || exit 1

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
