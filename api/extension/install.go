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
		Command:   command,
		ScriptURL: scriptURL,
		Script:    installScript(rec, dotenv, dir),
		Env:       dotenv,
		EnvFile:   envFileName(rec),
		Dir:       dir,
		PluginID:  rec.PluginID,
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
DOCKER_NETWORK="${PLUGIN_DOCKER_NETWORK:-}"

say()  { printf '\033[36m==>\033[0m %%s\n' "$*"; }
ok()   { printf '    \033[32m✓\033[0m %%s\n' "$*"; }
die()  { printf '\033[31merror:\033[0m %%s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"; }

# Start a built plugin in the background, keeping its pid and log beside it, and
# fail loudly if it dies on startup (a bad credential shows up right here).
start_bg() {
  say "starting $NAME"
  nohup "$@" >"$PWD/$NAME.log" 2>&1 &
  echo $! >"$PWD/$NAME.pid"
  sleep 2
  if ! kill -0 "$(cat "$PWD/$NAME.pid")" 2>/dev/null; then
    tail -n 30 "$PWD/$NAME.log" >&2 || true
    die "$NAME exited on startup — see $PWD/$NAME.log"
  fi
  ok "running (pid $(cat "$PWD/$NAME.pid"))"
  ok "logs : tail -f $PWD/$NAME.log"
  ok "stop : kill \$(cat $PWD/$NAME.pid)"
}

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

# 3. build & run -------------------------------------------------------------
if [ "$RUNTIME" = "auto" ]; then
  if   [ -f go.mod ];       then RUNTIME=go
  elif [ -f package.json ]; then RUNTIME=node
  elif [ -f Dockerfile ];   then RUNTIME=docker
  else die "cannot tell how to build this plugin — re-register it with an explicit runtime"
  fi
  say "detected $RUNTIME plugin"
fi

case "$RUNTIME" in
  go)
    need go
    say "building"
    go build -o "bin/$NAME" .
    start_bg "./bin/$NAME"
    ;;
  node)
    need npm
    say "installing dependencies"
    npm install
    npm run build --if-present
    start_bg npm start
    ;;
  docker)
    need docker
    say "building image $NAME"
    docker build -t "$NAME" .
    docker rm -f "$NAME" >/dev/null 2>&1 || true
    say "starting container $NAME"
    if [ -n "$DOCKER_NETWORK" ]; then
      docker run -d --name "$NAME" --restart unless-stopped --network "$DOCKER_NETWORK" --env-file "$ENV_FILE" "$NAME"
    else
      docker run -d --name "$NAME" --restart unless-stopped --env-file "$ENV_FILE" "$NAME"
    fi
    ok "running as container $NAME"
    ok "logs : docker logs -f $NAME"
    ok "stop : docker rm -f $NAME"
    ;;
  *)
    die "unknown runtime '$RUNTIME'"
    ;;
esac

printf '\n'
ok "%s is installed — it should now show as up in the FloMorphic extension list"
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
		dotenv,
		rec.Name,
	)
	return b.String()
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
