package extensionControllers

import (
	"fmt"
	"strings"

	"github.com/FloMorphic/morph-api/env"
	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/models"
	"github.com/gofiber/fiber/v3"
)

// Onboarding a third-party plugin — the two paths this file serves.
//
// A plugin is an independent process the user runs wherever they like; all
// FloMorphic needs is for it to reach Infra over NATS under an id this API knows
// about. So "installing" one is: register the row (POST /extension gives it its
// PluginID), then get it running with a credential minted for that id.
//
//	1. From source — the user has only a git URL. `GET …/install` answers with a
//	   one-liner that pipes `…/install.sh` into bash: the script clones the repo,
//	   writes the dotenv (credential included), builds and starts the plugin in a
//	   directory the user names.
//	2. Bring your own checkout — the user already has the plugin. `GET …/env`
//	   answers with just the dotenv to drop next to it, which is the whole of what
//	   the plugin needs to come up.
//
// Both bake a freshly minted, plugin-scoped credential into their response, so
// both are secret-bearing — exactly like POST /extension/plugin/cred, which the
// front end already calls to run the shipped builtin plugins.
//
// Nothing is cloned, built or executed by this API: it renders text the user
// runs on their own machine.

// installEnv handles GET /extension/id/:id/env — the dotenv for a plugin the
// user already has checked out (path 2 above).
func (ctl *controller) installEnv(c fiber.Ctx) error {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "extension not found")
	}
	if strings.TrimSpace(rec.PluginID) == "" {
		return etc.Fail(c, fiber.StatusBadRequest, "extension has no plugin id (not an inflowv1 plugin)")
	}
	cred, err := mintCred(models.CredRequest{PluginId: rec.PluginID, Name: rec.Name, Access: models.StrictAccess})
	if err != nil {
		return credError(c, err)
	}
	return etc.OK(c, fiber.Map{
		"env":      pluginEnvFile(rec.PluginID, cred, rec.Install.Env),
		"envFile":  envFileName(rec),
		"cred":     cred,
		"pluginId": rec.PluginID,
	})
}

// installInfo handles GET /extension/id/:id/install — the one-liner that
// installs and starts this plugin from source, plus the script it runs and the
// env that script writes (path 1 above). `?dir=` picks the install directory.
func (ctl *controller) installInfo(c fiber.Ctx) error {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "extension not found")
	}
	if strings.TrimSpace(rec.PluginID) == "" {
		return etc.Fail(c, fiber.StatusBadRequest, "extension has no plugin id (not an inflowv1 plugin)")
	}
	if strings.TrimSpace(rec.Install.Repo) == "" {
		return etc.Fail(c, fiber.StatusBadRequest, "extension has no source repository — use the env file instead")
	}
	dir := installDir(rec, c.Query("dir"))
	cred, err := mintCred(models.CredRequest{PluginId: rec.PluginID, Name: rec.Name, Access: models.StrictAccess})
	if err != nil {
		return credError(c, err)
	}
	dotenv := pluginEnvFile(rec.PluginID, cred, rec.Install.Env)
	name := slug(rec.Name, rec.PluginID)

	scriptURL := fmt.Sprintf("%s/extension/id/%s/install.sh", publicBaseURL(c), rec.ID)
	// A configured API is a guarded API: echo the caller's own bearer back into
	// the command so the pasted line can fetch the script. It is the token they
	// already hold, so nothing new is disclosed.
	auth := ""
	if token := c.Get(fiber.HeaderAuthorization); env.AuthEnabled() && token != "" {
		auth = fmt.Sprintf(" -H %q", fiber.HeaderAuthorization+": "+token)
	}
	command := fmt.Sprintf("curl -fsSL%s %q | bash -s -- %q", auth, scriptURL, dir)

	return etc.OK(c, models.InstallInfo{
		Command:     command,
		ScriptURL:   scriptURL,
		Script:      installScript(rec, dotenv, dir),
		Env:         dotenv,
		EnvFile:     envFileName(rec),
		Control:     controlScript(rec, name),
		ControlFile: controlFileName,
		Dir:         dir,
		PluginID:    rec.PluginID,
	})
}

// installScriptRaw handles GET /extension/id/:id/install.sh — the installer
// itself, as text/plain so `curl … | bash` works. Deliberately outside the
// `{data, error}` envelope: its only consumer is a shell.
func (ctl *controller) installScriptRaw(c fiber.Ctx) error {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "extension not found")
	}
	if strings.TrimSpace(rec.PluginID) == "" || strings.TrimSpace(rec.Install.Repo) == "" {
		return c.Status(fiber.StatusBadRequest).
			SendString("echo 'this extension has no plugin id or no source repository' >&2; exit 1\n")
	}
	cred, err := mintCred(models.CredRequest{PluginId: rec.PluginID, Name: rec.Name, Access: models.StrictAccess})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).
			SendString(fmt.Sprintf("echo %q >&2; exit 1\n", "credential unavailable: "+err.Error()))
	}
	dotenv := pluginEnvFile(rec.PluginID, cred, rec.Install.Env)
	c.Set(fiber.HeaderContentType, "text/plain; charset=utf-8")
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.SendString(installScript(rec, dotenv, installDir(rec, c.Query("dir"))))
}

// controlScriptRaw handles GET /extension/id/:id/ctl.sh — the lifecycle helper
// as text/plain, so a user who deleted their copy can refetch it with
// `curl … -o flomorphic-ctl.sh`. Unlike the installer it carries no credential,
// so it is safe to serve and cache.
func (ctl *controller) controlScriptRaw(c fiber.Ctx) error {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "extension not found")
	}
	if strings.TrimSpace(rec.PluginID) == "" {
		return c.Status(fiber.StatusBadRequest).
			SendString("echo 'this extension has no plugin id (not an inflowv1 plugin)' >&2; exit 1\n")
	}
	c.Set(fiber.HeaderContentType, "text/plain; charset=utf-8")
	return c.SendString(controlScript(rec, slug(rec.Name, rec.PluginID)))
}

// --- rendering ------------------------------------------------------------

// installScript renders the bash installer: clone (or update) the source, write
// the dotenv, then build and start the plugin the way its runtime wants. The
// generated script is the user's to read before running — it prints each step
// and leaves the process under a pid file it names.
func installScript(rec *models.ExtensionRecord, dotenv, dir string) string {
	spec := rec.Install
	runtime := spec.Runtime
	if runtime == "" {
		runtime = models.RuntimeAuto
	}
	name := slug(rec.Name, rec.PluginID)

	var b strings.Builder
	fmt.Fprintf(&b, `#!/usr/bin/env bash
#
# FloMorphic plugin installer — %s
#   plugin id : %s
#   source    : %s
#
# Generated by the FloMorphic API. It carries a NATS credential scoped to this
# one plugin: treat this script as a secret and do not commit it.
#
#   usage: curl -fsSL <this-url> | bash -s -- [install-dir]
#
set -euo pipefail

DIR="${1:-${PLUGIN_DIR:-%s}}"
REPO=%q
REF=%q
SUBDIR=%q
ENV_FILE=%q
NAME=%q
RUNTIME=%q
CONTROL_FILE=%q

say()  { printf '\033[36m==>\033[0m %%s\n' "$*"; }
ok()   { printf '    \033[32m✓\033[0m %%s\n' "$*"; }
die()  { printf '\033[31merror:\033[0m %%s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"; }

need git

# 1. source ------------------------------------------------------------------
if [ -d "$DIR/.git" ]; then
  say "updating existing checkout in $DIR"
  git -C "$DIR" fetch --depth 1 origin "${REF:-HEAD}"
  git -C "$DIR" checkout --detach FETCH_HEAD
elif [ -d "$DIR" ] && [ -n "$(ls -A "$DIR" 2>/dev/null)" ]; then
  die "$DIR already exists and is not a git checkout — choose another directory"
else
  say "cloning $REPO into $DIR"
  if [ -n "$REF" ]; then
    git clone --depth 1 --branch "$REF" "$REPO" "$DIR"
  else
    git clone --depth 1 "$REPO" "$DIR"
  fi
fi

WORKDIR="$DIR"
[ -n "$SUBDIR" ] && WORKDIR="$DIR/$SUBDIR"
[ -d "$WORKDIR" ] || die "$WORKDIR not found in the checkout"
cd "$WORKDIR"
ok "source ready in $(pwd)"

# 2. environment -------------------------------------------------------------
say "writing $ENV_FILE"
( umask 077; cat > "$ENV_FILE" <<'FLOMORPHIC_ENV'
%sFLOMORPHIC_ENV
)
ok "$(pwd)/$ENV_FILE written (contains this plugin's credential)"

# 3. control script ----------------------------------------------------------
# The lifecycle helper is the single thing that knows how to build, start, stop
# and tail *this* plugin regardless of its language. It is dropped next to the
# plugin so the user can run it again after a reboot without the FloMorphic API.
say "writing $CONTROL_FILE"
cat > "$CONTROL_FILE" <<'FLOMORPHIC_CTL'
%sFLOMORPHIC_CTL
chmod +x "$CONTROL_FILE"
ok "$(pwd)/$CONTROL_FILE written"

# 4. build & run -------------------------------------------------------------
"./$CONTROL_FILE" build
"./$CONTROL_FILE" start

printf '\n'
ok "%s is installed — it should now show as up in the FloMorphic extension list"
say "manage it any time from $(pwd):"
ok "logs    : ./$CONTROL_FILE logs"
ok "restart : ./$CONTROL_FILE restart"
ok "stop    : ./$CONTROL_FILE stop"
`,
		rec.Name,
		rec.PluginID,
		sourceLine(spec),
		dir,
		spec.Repo,
		strings.TrimSpace(spec.Ref),
		strings.Trim(strings.TrimSpace(spec.Subdir), "/"),
		envFileName(rec),
		name,
		runtime,
		controlFileName,
		dotenv,
		controlScript(rec, name),
		rec.Name,
	)
	return b.String()
}

// controlFileName is the lifecycle helper the installer drops next to the
// plugin. It carries no credential (it only manages a local process), so unlike
// the installer it is safe to keep and re-run.
const controlFileName = "flomorphic-ctl.sh"

// controlScript renders flomorphic-ctl.sh: a runtime-agnostic
// start/stop/restart/status/logs wrapper. It owns the same runtime detection the
// installer used to inline, so the plugin's language is resolved once, here, and
// every lifecycle command dispatches on it. For go/node the process runs under a
// pid+log file beside the plugin; for docker it is a restart-policy container, so
// `start` after a host reboot is just `docker start`.
func controlScript(rec *models.ExtensionRecord, name string) string {
	runtime := rec.Install.Runtime
	if runtime == "" {
		runtime = models.RuntimeAuto
	}
	return fmt.Sprintf(`#!/usr/bin/env bash
#
# FloMorphic plugin control — %s
#
# Build, run and watch this plugin without knowing its language. Run from the
# plugin's own directory:
#
#   ./%s start      build if needed, then launch in the background
#   ./%s stop       stop the running plugin
#   ./%s restart    stop then start
#   ./%s status     is it running?
#   ./%s logs        follow its log (Ctrl-C to stop watching)
#   ./%s build      (re)build after pulling new code
#
set -euo pipefail
cd "$(dirname "$0")"

NAME=%q
RUNTIME=%q
ENV_FILE=%q

say()  { printf '\033[36m==>\033[0m %%s\n' "$*"; }
ok()   { printf '    \033[32m✓\033[0m %%s\n' "$*"; }
die()  { printf '\033[31merror:\033[0m %%s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"; }

# Resolve "auto" against what is actually in the checkout, the same way the
# installer did. Keeps the plugin's language in one place.
resolve_runtime() {
  [ "$RUNTIME" = auto ] || return 0
  if   [ -f go.mod ];       then RUNTIME=go
  elif [ -f package.json ]; then RUNTIME=node
  elif [ -f Dockerfile ];   then RUNTIME=docker
  else die "cannot tell how to run this plugin — set RUNTIME at the top of this script"
  fi
}

pid_running() {  # echoes the pid and succeeds when the go/node process is up
  [ -f "$NAME.pid" ] || return 1
  local p; p="$(cat "$NAME.pid" 2>/dev/null)" || return 1
  [ -n "$p" ] && kill -0 "$p" 2>/dev/null || return 1
  echo "$p"
}

docker_running() { docker ps --format '{{.Names}}' 2>/dev/null | grep -qx "$NAME"; }
docker_exists()  { docker ps -a --format '{{.Names}}' 2>/dev/null | grep -qx "$NAME"; }

cmd_build() {
  resolve_runtime
  case "$RUNTIME" in
    go)     need go;  say "building"; go build -o "bin/$NAME" . ;;
    node)   need npm; say "installing dependencies"; npm install; npm run build --if-present ;;
    docker) need docker; say "building image $NAME"; docker build -t "$NAME" . ;;
  esac
  ok "built"
}

cmd_start() {
  resolve_runtime
  case "$RUNTIME" in
    docker)
      need docker
      if docker_running; then ok "already running"; return; fi
      if docker_exists;  then say "starting container $NAME"; docker start "$NAME" >/dev/null
      else
        say "starting container $NAME"
        docker run -d --name "$NAME" --restart unless-stopped \
          ${PLUGIN_DOCKER_NETWORK:+--network "$PLUGIN_DOCKER_NETWORK"} \
          --env-file "$ENV_FILE" "$NAME" >/dev/null
      fi
      ok "running as container $NAME (logs: ./%s logs)"
      ;;
    *)
      if pid_running >/dev/null; then ok "already running (pid $(pid_running))"; return; fi
      local run
      case "$RUNTIME" in
        go)   [ -x "bin/$NAME" ] || die "not built yet — run ./%s build"; run=("./bin/$NAME") ;;
        node) need npm; run=(npm start) ;;
        *)    die "unknown runtime '$RUNTIME'" ;;
      esac
      say "starting $NAME"
      nohup "${run[@]}" >"$NAME.log" 2>&1 &
      echo $! >"$NAME.pid"
      sleep 2
      if ! pid_running >/dev/null; then
        tail -n 30 "$NAME.log" >&2 || true
        die "$NAME exited on startup — see $(pwd)/$NAME.log"
      fi
      ok "running (pid $(pid_running), logs: ./%s logs)"
      ;;
  esac
}

cmd_stop() {
  resolve_runtime
  case "$RUNTIME" in
    docker)
      need docker
      if docker_exists; then docker rm -f "$NAME" >/dev/null 2>&1 || true; ok "stopped"; else ok "not running"; fi
      ;;
    *)
      local p; if p="$(pid_running)"; then kill "$p" 2>/dev/null || true; rm -f "$NAME.pid"; ok "stopped (was pid $p)"; else ok "not running"; fi
      ;;
  esac
}

cmd_status() {
  resolve_runtime
  case "$RUNTIME" in
    docker) if docker_running; then ok "running (container $NAME)"; else say "stopped"; fi ;;
    *)      local p; if p="$(pid_running)"; then ok "running (pid $p)"; else say "stopped"; fi ;;
  esac
}

cmd_logs() {
  resolve_runtime
  case "$RUNTIME" in
    docker) need docker; docker logs -f "$NAME" ;;
    *)      [ -f "$NAME.log" ] || die "no log yet — start the plugin first"; tail -n 100 -f "$NAME.log" ;;
  esac
}

case "${1:-status}" in
  build)   cmd_build ;;
  start)   cmd_start ;;
  stop)    cmd_stop ;;
  restart) cmd_stop; cmd_start ;;
  status)  cmd_status ;;
  logs)    cmd_logs ;;
  *) die "usage: ./%s {start|stop|restart|status|logs|build}" ;;
esac
`,
		rec.Name,
		controlFileName, controlFileName, controlFileName, controlFileName, controlFileName, controlFileName,
		name,
		runtime,
		envFileName(rec),
		controlFileName, // docker start "logs:" hint
		controlFileName, // go "not built" hint
		controlFileName, // pid running "logs:" hint
		controlFileName, // usage
	)
}

// sourceLine is the "repo @ ref (subdir)" summary in the script header.
func sourceLine(spec models.InstallSpec) string {
	line := spec.Repo
	if ref := strings.TrimSpace(spec.Ref); ref != "" {
		line += " @ " + ref
	}
	if sub := strings.Trim(strings.TrimSpace(spec.Subdir), "/"); sub != "" {
		line += " (" + sub + ")"
	}
	return line
}

// envFileName is the dotenv the plugin reads. The Go SDK's documented default is
// `.env.inflow`; a plugin that reads another name declares it on the row.
func envFileName(rec *models.ExtensionRecord) string {
	if f := strings.TrimSpace(rec.Install.EnvFile); f != "" {
		return f
	}
	return ".env.inflow"
}

// installDir resolves where the plugin lands: the caller's choice, else a
// directory named after the plugin under the working directory.
func installDir(rec *models.ExtensionRecord, requested string) string {
	if d := strings.TrimSpace(requested); d != "" {
		return d
	}
	return "./" + slug(rec.Name, rec.PluginID)
}

// slug reduces a display name to a filesystem/container-safe token, falling back
// to the plugin id when the name has nothing usable in it.
func slug(name, fallback string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case b.Len() > 0 && !prevDash:
			b.WriteRune('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return strings.TrimSpace(fallback)
	}
	return out
}

// publicBaseURL is the origin the install one-liner points at: PUBLIC_API_URL
// when set (the reliable answer behind a proxy), otherwise the origin this very
// request arrived on.
func publicBaseURL(c fiber.Ctx) string {
	if u := env.GetPublicApiUrl(); u != "" {
		return u
	}
	return strings.TrimRight(c.BaseURL(), "/")
}
