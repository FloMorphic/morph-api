package extensionControllers

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	"github.com/FloMorphic/morph-api/env"
	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/models"
	"github.com/gofiber/fiber/v3"
)

// The Windows half of plugin onboarding (see install.go for the whole story).
//
// A plugin is a process the user runs, not a container we schedule — go build,
// npm start, or docker. Two of those three are native on Windows, so the Windows
// path here is NOT the WSL hand-off the product installers use: it is a real
// PowerShell installer and a real PowerShell lifecycle helper, so a plugin author
// on Windows never needs WSL at all. The docker runtime is the same docker CLI
// either way.
//
// These render the same four steps as the bash pair, in the same order, with the
// same command surface (build/start/stop/restart/status/logs) — a plugin that
// installs on Linux installs on Windows, and the UI can offer either.
//
// Three Windows-specific things the bash version does not have to care about:
//
//  1. `exit` is unusable. These scripts are delivered as `irm <url> | iex`, and
//     under Invoke-Expression `exit` terminates the user's entire PowerShell
//     session — closing their window with the error still unread. So failure is a
//     throw caught by a wrapper at the bottom of each script.
//  2. The dotenv must be written UTF-8 with no BOM. PowerShell 5.1's Set-Content
//     -Encoding UTF8 emits a BOM, and a BOM makes the first key unparseable to
//     every dotenv reader — including the SDK the plugin uses.
//  3. Stopping means killing a process tree. `npm start` runs node as a child, so
//     stopping only npm would leave the plugin itself connected to Infra.

// controlFilePSName is the PowerShell lifecycle helper, the counterpart to
// controlFileName. Like it, it carries no credential and is safe to keep.
const controlFilePSName = "flomorphic-ctl.ps1"

// psLit renders s as a single-quoted PowerShell string literal: inside one,
// everything is verbatim and the only escape is a doubled quote. Nothing the
// caller supplies (a repo URL, a ref, a filename) can break out of it.
func psLit(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// psEmbed renders payload as PowerShell that evaluates to it.
//
// Normally that is a verbatim here-string, which keeps the dotenv and the control
// script readable in the installer the user is invited to read before running it.
// A here-string ends at a line that begins with its terminator, though, and the
// dotenv carries user-declared values — so a payload that could close the quote
// early is base64'd instead. Correctness first, readability when it is free.
func psEmbed(payload string) string {
	if psHereStringSafe(payload) {
		// The newline immediately before the terminator is part of the delimiter,
		// not the value — so the payload is followed by its own newline and then
		// the closing one. That is what makes the embedded value byte-identical to
		// the payload, trailing newline included, and so identical to what the
		// bash heredoc writes.
		return "@'\n" + payload + "\n'@\n"
	}
	enc := base64.StdEncoding.EncodeToString([]byte(payload))
	return "[System.Text.Encoding]::UTF8.GetString([System.Convert]::FromBase64String(" + psLit(enc) + "))\n"
}

// psHereStringSafe reports whether payload can live in a verbatim here-string:
// no line may start the terminator, and PowerShell allows leading whitespace
// before it.
func psHereStringSafe(payload string) bool {
	for _, line := range strings.Split(payload, "\n") {
		if strings.HasPrefix(strings.TrimLeft(strings.TrimSuffix(line, "\r"), " \t"), "'@") {
			return false
		}
	}
	return true
}

// psComment makes arbitrary text safe to drop into a PowerShell comment: a
// newline would end a line comment and let the rest run as code, and "#>" would
// close a block comment early.
func psComment(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ", "#>", "#").Replace(s)
	return strings.TrimSpace(s)
}

// winDir presents a POSIX-looking default install directory the Windows way.
// `./name` is valid in PowerShell, but `.\name` is what a Windows user expects to
// read back; an absolute or already-backslashed path is left alone.
func winDir(dir string) string {
	if strings.HasPrefix(dir, "./") {
		return `.\` + strings.TrimPrefix(dir, "./")
	}
	return dir
}

// installScriptPS renders the PowerShell installer: clone (or update) the source,
// write the dotenv, drop the control script, then build and start — the same four
// steps installScript renders for bash.
func installScriptPS(rec *models.ExtensionRecord, dotenv, dir string) string {
	spec := rec.Install
	runtime := spec.Runtime
	if runtime == "" {
		runtime = models.RuntimeAuto
	}
	name := slug(rec.Name, rec.PluginID)

	return strings.NewReplacer(
		"{{NAME_TEXT}}", psComment(rec.Name),
		"{{NAME_PSLIT}}", psLit(rec.Name),
		"{{PLUGIN_ID}}", psComment(rec.PluginID),
		"{{SOURCE}}", psComment(sourceLine(spec)),
		"{{SLUG}}", name,
		"{{REPO_LIT}}", psLit(spec.Repo),
		"{{REF_LIT}}", psLit(strings.TrimSpace(spec.Ref)),
		"{{SUBDIR_LIT}}", psLit(strings.Trim(strings.TrimSpace(spec.Subdir), "/")),
		"{{ENVFILE_LIT}}", psLit(envFileName(rec)),
		"{{NAME_LIT}}", psLit(name),
		"{{RUNTIME_LIT}}", psLit(string(runtime)),
		"{{CTL_LIT}}", psLit(controlFilePSName),
		"{{CTL}}", controlFilePSName,
		"{{DIR_LIT}}", psLit(winDir(dir)),
		"{{ENV_EMBED}}", psEmbed(dotenv),
		"{{CTL_EMBED}}", psEmbed(controlScriptPS(rec, name)),
	).Replace(installPSTemplate)
}

// controlScriptPS renders flomorphic-ctl.ps1: the runtime-agnostic
// build/start/stop/restart/status/logs wrapper, Windows-native. It mirrors
// controlScript's command surface exactly, including resolving an "auto" runtime
// from what is actually in the checkout.
func controlScriptPS(rec *models.ExtensionRecord, name string) string {
	runtime := rec.Install.Runtime
	if runtime == "" {
		runtime = models.RuntimeAuto
	}
	return strings.NewReplacer(
		"{{NAME_TEXT}}", psComment(rec.Name),
		"{{NAME_LIT}}", psLit(name),
		"{{RUNTIME_LIT}}", psLit(string(runtime)),
		"{{ENVFILE_LIT}}", psLit(envFileName(rec)),
		"{{CTL}}", controlFilePSName,
	).Replace(controlPSTemplate)
}

// Templates use {{TOKEN}} rather than printf verbs: these scripts are long, hold
// dozens of substitutions, and a positional-argument list is impossible to read
// or safely reorder. Note that no PowerShell backtick appears in either template
// — they live in Go raw strings, which cannot contain one.

const installPSTemplate = `#Requires -Version 5.1
<#
    FloMorphic plugin installer (Windows) — {{NAME_TEXT}}
      plugin id : {{PLUGIN_ID}}
      source    : {{SOURCE}}

    Generated by the FloMorphic API. It carries a NATS credential scoped to this
    one plugin: treat this script as a secret and do not commit it.

    usage:  irm '<this-url>' | iex

    To install somewhere else, pass -Dir (a bare irm | iex cannot take arguments):

      & ([scriptblock]::Create((irm '<this-url>'))) -Dir 'C:\plugins\{{SLUG}}'

    or set $env:PLUGIN_DIR first.

    It clones the source, writes the dotenv, drops {{CTL}} beside the plugin, then
    builds and starts it. Re-running it updates the checkout in place.

    Requires git, plus whatever the plugin itself builds with (Go, Node or Docker).
#>
[CmdletBinding()]
param([string] $Dir)

$ErrorActionPreference = 'Stop'
$ProgressPreference    = 'SilentlyContinue'

$Repo        = {{REPO_LIT}}
$Ref         = {{REF_LIT}}
$Subdir      = {{SUBDIR_LIT}}
$EnvFile     = {{ENVFILE_LIT}}
$Name        = {{NAME_LIT}}
$Runtime     = {{RUNTIME_LIT}}
$ControlFile = {{CTL_LIT}}

if (-not $Dir) { $Dir = if ($env:PLUGIN_DIR) { $env:PLUGIN_DIR } else { {{DIR_LIT}} } }

function Say { param([string]$m) Write-Host '==> ' -ForegroundColor Cyan -NoNewline; Write-Host $m }
function Ok  { param([string]$m) Write-Host '    ' -NoNewline; Write-Host '[ok] ' -ForegroundColor Green -NoNewline; Write-Host $m }

# Stopping, when this script arrives as 'irm <url> | iex': 'exit' would terminate
# the caller's whole PowerShell session and close the window with the error still
# unread, so a failure is a throw that the wrapper at the bottom catches.
$script:Failed = $false
$STOP = 'PLUGIN_STOP:'
function Die  { param([string]$m) $script:Failed = $true; throw ($STOP + $m) }
function Need { param([string]$c) if (-not (Get-Command $c -ErrorAction SilentlyContinue)) { Die ($c + ' is required but not installed') } }

# Write text as UTF-8 with no BOM and the newlines exactly as generated.
# Set-Content would add a BOM on PowerShell 5.1 (which makes the first dotenv key
# unreadable to every parser) and rewrite the line endings.
function Write-TextFile {
  param([string]$Path, [string]$Text)
  [System.IO.File]::WriteAllText($Path, $Text, (New-Object System.Text.UTF8Encoding $false))
}

try {

Need git

# 1. source ------------------------------------------------------------------
if (Test-Path -LiteralPath (Join-Path $Dir '.git')) {
  Say ('updating existing checkout in ' + $Dir)
  & git -C $Dir fetch --depth 1 origin $(if ($Ref) { $Ref } else { 'HEAD' })
  if ($LASTEXITCODE -ne 0) { Die 'git fetch failed' }
  & git -C $Dir checkout --detach FETCH_HEAD
  if ($LASTEXITCODE -ne 0) { Die 'git checkout failed' }
} elseif ((Test-Path -LiteralPath $Dir) -and (Get-ChildItem -LiteralPath $Dir -Force -ErrorAction SilentlyContinue | Select-Object -First 1)) {
  Die ($Dir + ' already exists and is not a git checkout -- choose another directory')
} else {
  Say ('cloning ' + $Repo + ' into ' + $Dir)
  if ($Ref) { & git clone --depth 1 --branch $Ref $Repo $Dir } else { & git clone --depth 1 $Repo $Dir }
  if ($LASTEXITCODE -ne 0) { Die 'git clone failed' }
}

$WorkDir = if ($Subdir) { Join-Path $Dir $Subdir } else { $Dir }
if (-not (Test-Path -LiteralPath $WorkDir)) { Die ($WorkDir + ' not found in the checkout') }
Set-Location -LiteralPath $WorkDir
$Here = (Get-Location).Path
Ok ('source ready in ' + $Here)

# 2. environment -------------------------------------------------------------
Say ('writing ' + $EnvFile)
$envText = {{ENV_EMBED}}
$envPath = Join-Path $Here $EnvFile
Write-TextFile -Path $envPath -Text $envText
# Lock the credential file to this account. A new file inherits whatever the
# parent directory allows, which on a shared or roamed profile is too much.
try { & icacls $envPath /inheritance:r /grant:r ($env:USERNAME + ':(R,W)') > $null 2>&1 } catch { }
Ok ($envPath + ' written (contains this plugin''s credential)')

# 3. control script ----------------------------------------------------------
# The lifecycle helper is the single thing that knows how to build, start, stop
# and tail *this* plugin whatever its language. It lands next to the plugin so it
# can be re-run after a reboot without the FloMorphic API.
Say ('writing ' + $ControlFile)
$ctlText = {{CTL_EMBED}}
Write-TextFile -Path (Join-Path $Here $ControlFile) -Text $ctlText
Ok ((Join-Path $Here $ControlFile) + ' written')

# 4. build & run -------------------------------------------------------------
# Through this same PowerShell with -ExecutionPolicy Bypass, not as a bare
# '.\ctl.ps1': running a script FILE is policy-checked, and a default Windows
# client is Restricted -- which would fail the install at its last step even
# though this installer itself ran fine (iex is not policy-checked). The control
# script's own header says how to make direct invocation work later.
$psExe = (Get-Process -Id $PID).Path
if (-not $psExe) { $psExe = 'powershell.exe' }
$ctlPath = Join-Path $Here $ControlFile
& $psExe -NoProfile -ExecutionPolicy Bypass -File $ctlPath build
if ($LASTEXITCODE -ne 0) { Die 'build failed -- see the output above' }
& $psExe -NoProfile -ExecutionPolicy Bypass -File $ctlPath start
if ($LASTEXITCODE -ne 0) { Die 'the plugin did not start -- see the output above' }

Write-Host ''
Ok ({{NAME_PSLIT}} + ' is installed -- it should now show as up in the FloMorphic extension list')
Say ('manage it any time from ' + $Here + ':')
Ok ('logs    : .\' + $ControlFile + ' logs')
Ok ('restart : .\' + $ControlFile + ' restart')
Ok ('stop    : .\' + $ControlFile + ' stop')

} catch {
  $m = "$($_.Exception.Message)"
  if ($m.StartsWith($STOP)) { $m = $m.Substring($STOP.Length) }
  Write-Host ''
  Write-Host 'error: ' -ForegroundColor Red -NoNewline; Write-Host $m
  $script:Failed = $true
}

# A file run gets a real exit status; a piped run only gets the variable, because
# exiting would take the user's session down with it.
$global:LASTEXITCODE = $(if ($script:Failed) { 1 } else { 0 })
if ($PSCommandPath -and $script:Failed) { exit 1 }
`

const controlPSTemplate = `#Requires -Version 5.1
<#
    FloMorphic plugin control (Windows) — {{NAME_TEXT}}

    Build, run and watch this plugin without knowing its language. Run it from the
    plugin's own directory:

      .\{{CTL}} start      build if needed, then launch in the background
      .\{{CTL}} stop       stop the running plugin (and anything it spawned)
      .\{{CTL}} restart    stop then start
      .\{{CTL}} status     is it running?
      .\{{CTL}} logs       follow its output (Ctrl-C to stop watching)
      .\{{CTL}} logs -Err  follow its error stream instead
      .\{{CTL}} build      (re)build after pulling new code

    Carries no credential: it only manages a local process. Safe to keep and re-run.

    If PowerShell refuses to run it ("running scripts is disabled"), either unblock
    this one file:
      Unblock-File .\{{CTL}}; powershell -ExecutionPolicy Bypass -File .\{{CTL}} start
    or allow local scripts for your account:
      Set-ExecutionPolicy -Scope CurrentUser RemoteSigned
#>
[CmdletBinding()]
param(
  [Parameter(Position = 0)]
  [ValidateSet('build', 'start', 'stop', 'restart', 'status', 'logs')]
  [string] $Action = 'status',
  [switch] $Err
)

$ErrorActionPreference = 'Stop'
# Empty when this content is run from a scriptblock rather than the file, in which
# case the caller has already set the working directory.
if ($PSScriptRoot) { Set-Location -LiteralPath $PSScriptRoot }

$Name    = {{NAME_LIT}}
$Runtime = {{RUNTIME_LIT}}
$EnvFile = {{ENVFILE_LIT}}

# Start-Process will not point both streams at one file, so stdout and stderr are
# kept apart. The error log is the one that explains a crash.
$LogOut  = $Name + '.log'
$LogErr  = $Name + '.err.log'
$PidFile = $Name + '.pid'

function Say { param([string]$m) Write-Host '==> ' -ForegroundColor Cyan -NoNewline; Write-Host $m }
function Ok  { param([string]$m) Write-Host '    ' -NoNewline; Write-Host '[ok] ' -ForegroundColor Green -NoNewline; Write-Host $m }

# A throw, not exit: this content may also be run from a scriptblock rather than
# as a file, and there an exit would close the caller's whole session.
$script:Failed = $false
$STOP = 'PLUGIN_STOP:'
function Die  { param([string]$m) $script:Failed = $true; throw ($STOP + $m) }
function Need { param([string]$c) if (-not (Get-Command $c -ErrorAction SilentlyContinue)) { Die ($c + ' is required but not installed') } }

# Resolve 'auto' against what is actually in the checkout, the same way the bash
# helper does. Keeps the plugin's language decided in one place.
function Resolve-Runtime {
  if ($script:Runtime -ne 'auto') { return }
  if     (Test-Path -LiteralPath 'go.mod')       { $script:Runtime = 'go' }
  elseif (Test-Path -LiteralPath 'package.json') { $script:Runtime = 'node' }
  elseif (Test-Path -LiteralPath 'Dockerfile')   { $script:Runtime = 'docker' }
  else { Die 'cannot tell how to run this plugin -- set $Runtime at the top of this script' }
}

# The recorded pid, but only when it is still a live process. Windows reuses pids,
# so a stale pid file must not be mistaken for a running plugin; the start time is
# not enough on its own, so the process is also required to still exist.
function Get-PluginPid {
  if (-not (Test-Path -LiteralPath $PidFile)) { return $null }
  $raw = (Get-Content -LiteralPath $PidFile -ErrorAction SilentlyContinue | Select-Object -First 1)
  if (-not $raw) { return $null }
  $id = 0
  if (-not [int]::TryParse($raw.Trim(), [ref] $id)) { return $null }
  $proc = Get-Process -Id $id -ErrorAction SilentlyContinue
  if (-not $proc) { return $null }
  return $id
}

function Test-DockerRunning { (& docker ps --format '{{.Names}}' 2> $null) -contains $Name }
function Test-DockerExists  { (& docker ps -a --format '{{.Names}}' 2> $null) -contains $Name }

function Invoke-Build {
  Resolve-Runtime
  switch ($script:Runtime) {
    'go' {
      Need go
      Say 'building'
      New-Item -ItemType Directory -Force -Path 'bin' | Out-Null
      # Join-Path, not a literal separator: it is the one form that is correct
      # whatever PowerShell this runs on.
      & go build -o (Join-Path 'bin' ($Name + '.exe')) .
      if ($LASTEXITCODE -ne 0) { Die 'go build failed' }
    }
    'node' {
      Need npm
      Say 'installing dependencies'
      & npm install
      if ($LASTEXITCODE -ne 0) { Die 'npm install failed' }
      & npm run build --if-present
    }
    'docker' {
      Need docker
      Say ('building image ' + $Name)
      & docker build -t $Name .
      if ($LASTEXITCODE -ne 0) { Die 'docker build failed' }
    }
  }
  Ok 'built'
}

function Invoke-Start {
  Resolve-Runtime
  if ($script:Runtime -eq 'docker') {
    Need docker
    if (Test-DockerRunning) { Ok 'already running'; return }
    if (Test-DockerExists) {
      Say ('starting container ' + $Name)
      & docker start $Name > $null
    } else {
      Say ('starting container ' + $Name)
      $a = @('run', '-d', '--name', $Name, '--restart', 'unless-stopped')
      if ($env:PLUGIN_DOCKER_NETWORK) { $a += @('--network', $env:PLUGIN_DOCKER_NETWORK) }
      $a += @('--env-file', $EnvFile, $Name)
      & docker @a > $null
    }
    if ($LASTEXITCODE -ne 0) { Die 'docker could not start the plugin' }
    Ok ('running as container ' + $Name + ' (logs: .\{{CTL}} logs)')
    return
  }

  $existing = Get-PluginPid
  if ($existing) { Ok ('already running (pid ' + $existing + ')'); return }

  $exe = $null; $argv = @()
  switch ($script:Runtime) {
    'go' {
      $built = Join-Path 'bin' ($Name + '.exe')
      if (-not (Test-Path -LiteralPath $built)) { Die ('not built yet -- run .\{{CTL}} build') }
      $exe = (Resolve-Path -LiteralPath $built).Path
    }
    'node' {
      Need npm
      # npm on Windows is npm.cmd; Get-Command resolves it, Start-Process needs the
      # real path.
      $exe  = (Get-Command npm).Source
      $argv = @('start')
    }
    default { Die ('unknown runtime ' + $script:Runtime) }
  }

  Say ('starting ' + $Name)
  Remove-Item -LiteralPath $LogOut, $LogErr -Force -ErrorAction SilentlyContinue
  $sp = @{
    FilePath               = $exe
    WorkingDirectory       = (Get-Location).Path
    RedirectStandardOutput = $LogOut
    RedirectStandardError  = $LogErr
    PassThru               = $true
  }
  if ($argv.Count) { $sp['ArgumentList'] = $argv }
  # Hidden stops a console plugin flashing a window on Windows. PowerShell on
  # other platforms rejects the parameter outright -- there is no window to hide --
  # and $IsWindows only exists from PowerShell 6, where it tells us which we are on.
  if (($PSVersionTable.PSVersion.Major -lt 6) -or $IsWindows) { $sp['WindowStyle'] = 'Hidden' }
  # Start-Process detaches: the plugin keeps running after this window closes,
  # which is what nohup buys the bash version.
  $proc = Start-Process @sp
  Set-Content -LiteralPath $PidFile -Value $proc.Id -Encoding ascii
  Start-Sleep -Seconds 2
  if (-not (Get-PluginPid)) {
    foreach ($f in @($LogErr, $LogOut)) {
      if ((Test-Path -LiteralPath $f) -and (Get-Item -LiteralPath $f).Length -gt 0) {
        Write-Host ('--- ' + $f + ' ---') -ForegroundColor DarkGray
        Get-Content -LiteralPath $f -Tail 30 | ForEach-Object { Write-Host $_ }
      }
    }
    # The pid is dead; leaving the file would have status report on a process that
    # never came up.
    Remove-Item -LiteralPath $PidFile -Force -ErrorAction SilentlyContinue
    Die ($Name + ' exited on startup -- see ' + (Join-Path (Get-Location).Path $LogErr))
  }
  Ok ('running (pid ' + $proc.Id + ', logs: .\{{CTL}} logs)')
}

function Invoke-Stop {
  Resolve-Runtime
  if ($script:Runtime -eq 'docker') {
    Need docker
    if (Test-DockerExists) { & docker rm -f $Name > $null 2>&1; Ok 'stopped' } else { Ok 'not running' }
    return
  }
  $id = Get-PluginPid
  if (-not $id) { Remove-Item -LiteralPath $PidFile -Force -ErrorAction SilentlyContinue; Ok 'not running'; return }
  # The whole tree, not just the recorded pid: 'npm start' runs node as a child,
  # and killing npm alone would leave the plugin itself connected to Infra.
  # Stop-Process is the fallback, and the only option where taskkill is absent.
  if (Get-Command taskkill -ErrorAction SilentlyContinue) {
    & taskkill /PID $id /T /F > $null 2>&1
  }
  if (Get-PluginPid) { Stop-Process -Id $id -Force -ErrorAction SilentlyContinue }
  Remove-Item -LiteralPath $PidFile -Force -ErrorAction SilentlyContinue
  Ok ('stopped (was pid ' + $id + ')')
}

function Invoke-Status {
  Resolve-Runtime
  if ($script:Runtime -eq 'docker') {
    Need docker
    if (Test-DockerRunning) { Ok ('running (container ' + $Name + ')') } else { Say 'stopped' }
    return
  }
  $id = Get-PluginPid
  if ($id) { Ok ('running (pid ' + $id + ')') } else { Say 'stopped' }
}

function Invoke-Logs {
  Resolve-Runtime
  if ($script:Runtime -eq 'docker') { Need docker; & docker logs -f $Name; return }
  $f = if ($Err) { $LogErr } else { $LogOut }
  if (-not (Test-Path -LiteralPath $f)) { Die 'no log yet -- start the plugin first' }
  Get-Content -LiteralPath $f -Tail 100 -Wait
}

try {
  switch ($Action) {
    'build'   { Invoke-Build }
    'start'   { Invoke-Start }
    'stop'    { Invoke-Stop }
    'restart' { Invoke-Stop; Invoke-Start }
    'status'  { Invoke-Status }
    'logs'    { Invoke-Logs }
  }
} catch {
  $m = "$($_.Exception.Message)"
  if ($m.StartsWith($STOP)) { $m = $m.Substring($STOP.Length) }
  Write-Host 'error: ' -ForegroundColor Red -NoNewline; Write-Host $m
  $script:Failed = $true
}

$global:LASTEXITCODE = $(if ($script:Failed) { 1 } else { 0 })
if ($PSCommandPath -and $script:Failed) { exit 1 }
`

// --- handlers -------------------------------------------------------------

// installScriptPSRaw handles GET /extension/id/:id/install.ps1 — the PowerShell
// installer as text/plain, so `irm … | iex` works. The bash counterpart's twin;
// see installScriptRaw.
func (ctl *controller) installScriptPSRaw(c fiber.Ctx) error {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "extension not found")
	}
	if strings.TrimSpace(rec.PluginID) == "" || strings.TrimSpace(rec.Install.Repo) == "" {
		return psError(c, fiber.StatusBadRequest, "this extension has no plugin id or no source repository")
	}
	cred, err := mintCred(models.CredRequest{PluginId: rec.PluginID, Name: rec.Name, Access: models.StrictAccess})
	if err != nil {
		return psError(c, fiber.StatusInternalServerError, "credential unavailable: "+err.Error())
	}
	dotenv := pluginEnvFile(rec.PluginID, cred, rec.Install.Env)
	c.Set(fiber.HeaderContentType, "text/plain; charset=utf-8")
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.SendString(installScriptPS(rec, dotenv, installDir(rec, c.Query("dir"))))
}

// controlScriptPSRaw handles GET /extension/id/:id/ctl.ps1 — the PowerShell
// lifecycle helper, so a user who deleted their copy can refetch it. Carries no
// credential, so unlike the installer it is safe to serve and cache.
func (ctl *controller) controlScriptPSRaw(c fiber.Ctx) error {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "extension not found")
	}
	if strings.TrimSpace(rec.PluginID) == "" {
		return psError(c, fiber.StatusBadRequest, "this extension has no plugin id (not an inflowv1 plugin)")
	}
	c.Set(fiber.HeaderContentType, "text/plain; charset=utf-8")
	return c.SendString(controlScriptPS(rec, slug(rec.Name, rec.PluginID)))
}

// psError answers a script request with PowerShell that reports the problem.
// The consumer is a shell, so the message has to arrive as runnable code — and it
// must not call `exit`, which under `irm | iex` would close the user's session
// before they could read it.
func psError(c fiber.Ctx, status int, msg string) error {
	c.Set(fiber.HeaderContentType, "text/plain; charset=utf-8")
	return c.Status(status).SendString(
		"Write-Host 'error: " + strings.ReplaceAll(msg, "'", "''") + "' -ForegroundColor Red\n" +
			"$global:LASTEXITCODE = 1\n")
}

// windowsVariant is the Windows half of an InstallInfo: the same four things the
// flat bash fields carry, for the PowerShell pair. `dir` is echoed into the URL
// only when the caller asked for one, so the common pasted command stays short —
// the generated script bakes the default in either way.
func (ctl *controller) windowsVariant(c fiber.Ctx, rec *models.ExtensionRecord, dotenv, dir string) *models.InstallVariant {
	scriptURL := fmt.Sprintf("%s/extension/id/%s/install.ps1", publicBaseURL(c), rec.ID)
	if q := strings.TrimSpace(c.Query("dir")); q != "" {
		scriptURL += "?dir=" + url.QueryEscape(q)
	}
	// A guarded API needs the caller's own bearer echoed back so the pasted line
	// can fetch the script — the token they already hold, so nothing new is shown.
	command := fmt.Sprintf("irm %s | iex", psLit(scriptURL))
	if token := c.Get(fiber.HeaderAuthorization); env.AuthEnabled() && token != "" {
		command = fmt.Sprintf("irm -Headers @{ Authorization = %s } %s | iex", psLit(token), psLit(scriptURL))
	}
	name := slug(rec.Name, rec.PluginID)
	return &models.InstallVariant{
		Command:     command,
		ScriptURL:   scriptURL,
		Script:      installScriptPS(rec, dotenv, dir),
		Control:     controlScriptPS(rec, name),
		ControlFile: controlFilePSName,
	}
}
