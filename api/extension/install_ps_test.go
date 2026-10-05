package extensionControllers

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FloMorphic/morph-api/models"
)

// The generated PowerShell is only ever read by a shell on someone else's
// machine, so the things worth pinning here are the ones that would silently
// produce a broken script: an unsubstituted token, a value that escapes its
// quoting, a here-string the payload can close early, and the Windows-specific
// rules (no BOM, no `exit`, kill the tree).

func rec(name, pluginID string, spec models.InstallSpec) *models.ExtensionRecord {
	return &models.ExtensionRecord{ID: "ext1", Name: name, PluginID: pluginID, Install: spec}
}

func baseSpec() models.InstallSpec {
	return models.InstallSpec{Repo: "https://github.com/acme/plug.git", Ref: "main", Runtime: models.RuntimeGo}
}

// Every token must be substituted: a surviving "{{" is a script that cannot run,
// and it is invisible until someone pastes it.
func TestPSNoUnsubstitutedTokens(t *testing.T) {
	for _, rt := range []models.InstallRuntime{models.RuntimeAuto, models.RuntimeGo, models.RuntimeNode, models.RuntimeDocker, ""} {
		spec := baseSpec()
		spec.Runtime = rt
		r := rec("Acme Plugin", "pid-1", spec)
		for what, got := range map[string]string{
			"installer": installScriptPS(r, "PLUGIN_ID=pid-1\n", "./acme-plugin"),
			"control":   controlScriptPS(r, "acme-plugin"),
		} {
			if i := strings.Index(got, "{{"); i >= 0 {
				// `{{.Names}}` is a docker --format argument, not one of our tokens.
				if !strings.HasPrefix(got[i:], "{{.Names}}") {
					t.Errorf("runtime %q %s: unsubstituted token at %q", rt, what, got[i:min(i+40, len(got))])
				}
			}
		}
	}
}

// A display name is free text. Inside a single-quoted PowerShell string an
// apostrophe must be doubled, or everything after it changes meaning.
func TestPSNameWithApostropheIsQuoted(t *testing.T) {
	r := rec("Bob's Plugin'; Write-Host pwned", "pid-1", baseSpec())
	got := installScriptPS(r, "A=1\n", "./p")
	// The dangerous rendering is the raw name opening a literal it then closes.
	if strings.Contains(got, "'Bob's") {
		t.Fatal("name was not escaped into its string literal")
	}
	if !strings.Contains(got, "'Bob''s Plugin''; Write-Host pwned'") {
		t.Error("expected the name to render as one literal with doubled apostrophes")
	}
}

// Same for the fields that come straight off the record.
func TestPSSpecFieldsAreQuoted(t *testing.T) {
	spec := baseSpec()
	spec.Repo = "https://x/y.git'; Write-Host pwned; '"
	spec.Ref = "v1'"
	got := installScriptPS(rec("P", "pid-1", spec), "A=1\n", "./p")
	for _, bad := range []string{"$Repo = 'https://x/y.git'; Write-Host pwned", "$Ref = 'v1'\n"} {
		if strings.Contains(got, bad) {
			t.Errorf("value broke out of its literal: %q", bad)
		}
	}
	if !strings.Contains(got, `'https://x/y.git''; Write-Host pwned; '''`) {
		t.Error("expected the repo URL's apostrophes to be doubled")
	}
}

// A newline or `#>` in the name would end the header comment and let the rest of
// the line run as code.
func TestPSCommentIsNeutralised(t *testing.T) {
	got := psComment("line one\n#> Write-Host pwned\r\nmore")
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("newlines survived: %q", got)
	}
	if strings.Contains(got, "#>") {
		t.Errorf("block-comment terminator survived: %q", got)
	}
}

// A here-string ends at a line starting with its terminator, and the dotenv holds
// user-declared values — so such a payload has to be embedded another way.
func TestPSHereStringFallsBackToBase64(t *testing.T) {
	safe := "PLUGIN_ID=x\nINFRA_URL=y\n"
	if got := psEmbed(safe); !strings.HasPrefix(got, "@'\n") {
		t.Errorf("a safe payload should stay a readable here-string, got %q", got)
	}
	// The value a here-string yields excludes the newline right before its
	// terminator, so the payload must be followed by its own newline and then the
	// closing one — otherwise the written dotenv silently loses its final newline
	// and stops matching the bytes the bash heredoc writes.
	if got := psEmbed(safe); got != "@'\n"+safe+"\n'@\n" {
		t.Errorf("here-string would not round-trip the payload exactly: %q", got)
	}

	// A value whose newline is followed by the terminator would close the quote.
	hostile := "PLUGIN_ID=x\n'@\nWrite-Host pwned\n"
	got := psEmbed(hostile)
	if strings.Contains(got, "@'") {
		t.Fatalf("hostile payload was still embedded as a here-string: %q", got)
	}
	if !strings.Contains(got, base64.StdEncoding.EncodeToString([]byte(hostile))) {
		t.Error("expected the payload to be base64-encoded instead")
	}
	// Leading whitespace before the terminator still closes it.
	if psHereStringSafe("A=1\n   '@\n") {
		t.Error("an indented terminator must also be treated as unsafe")
	}
}

// `exit` under Invoke-Expression terminates the user's whole PowerShell session,
// which is how these scripts are delivered. Only a file run may exit.
func TestPSNeverExitsUnguarded(t *testing.T) {
	r := rec("P", "pid-1", baseSpec())
	for what, script := range map[string]string{
		"installer": installScriptPS(r, "A=1\n", "./p"),
		"control":   controlScriptPS(r, "p"),
	} {
		for _, line := range strings.Split(script, "\n") {
			l := strings.TrimSpace(line)
			if !strings.HasPrefix(l, "exit") && !strings.Contains(l, "; exit") {
				continue
			}
			// The one permitted form is guarded by $PSCommandPath, which is empty
			// unless the script really is running from a file.
			if !strings.Contains(l, "$PSCommandPath") {
				t.Errorf("%s: unguarded exit: %q", what, l)
			}
		}
	}
}

// The dotenv must land as UTF-8 with no BOM: PowerShell 5.1's Set-Content -Encoding
// UTF8 writes one, and a BOM makes the first key unreadable to every dotenv parser.
func TestPSWritesEnvWithoutBOM(t *testing.T) {
	got := installScriptPS(rec("P", "pid-1", baseSpec()), "PLUGIN_ID=x\n", "./p")
	if !strings.Contains(got, "New-Object System.Text.UTF8Encoding $false") {
		t.Error("expected WriteAllText with a BOM-less UTF8Encoding")
	}
	if strings.Contains(got, "Set-Content -LiteralPath $envPath") {
		t.Error("the env file must not go through Set-Content, which adds a BOM on 5.1")
	}
}

// Stopping has to take the process tree: `npm start` runs node as a child, and
// killing npm alone leaves the plugin connected to Infra.
func TestPSControlStopsProcessTree(t *testing.T) {
	got := controlScriptPS(rec("P", "pid-1", baseSpec()), "p")
	if !strings.Contains(got, "taskkill /PID $id /T /F") {
		t.Error("expected taskkill with /T to stop the whole tree")
	}
}

// The bash and PowerShell helpers are meant to be interchangeable, so the verbs a
// user types must match exactly.
func TestPSControlMatchesBashCommandSurface(t *testing.T) {
	got := controlScriptPS(rec("P", "pid-1", baseSpec()), "p")
	for _, verb := range []string{"build", "start", "stop", "restart", "status", "logs"} {
		if !strings.Contains(got, "'"+verb+"'") {
			t.Errorf("control script is missing the %q action", verb)
		}
	}
}

// Dump the rendered scripts for an external PowerShell parse check. Only runs
// when PS_DUMP_DIR is set, so the normal test run touches no files.
func TestPSDumpForExternalParse(t *testing.T) {
	dir := os.Getenv("PS_DUMP_DIR")
	if dir == "" {
		t.Skip("set PS_DUMP_DIR to write the rendered scripts out")
	}
	cases := map[string]*models.ExtensionRecord{
		"go":     rec("Acme Go Plugin", "pid-go", models.InstallSpec{Repo: "https://github.com/a/b.git", Ref: "main", Runtime: models.RuntimeGo}),
		"node":   rec("Node Plugin", "pid-node", models.InstallSpec{Repo: "https://github.com/a/b.git", Subdir: "pkgs/thing", Runtime: models.RuntimeNode, EnvFile: ".env.morph"}),
		"docker": rec("Docker Plugin", "pid-dk", models.InstallSpec{Repo: "git@github.com:a/b.git", Runtime: models.RuntimeDocker}),
		"auto":   rec("Bob's Auto Plugin", "pid-auto", models.InstallSpec{Repo: "https://github.com/a/b.git", Runtime: models.RuntimeAuto}),
	}
	for name, r := range cases {
		dotenv := "PLUGIN_ID=" + r.PluginID + "\nINFRA_URL=nats:4222\nINFRA_CRED=-----BEGIN NATS USER JWT-----\nabc\n-----END-----\n"
		write(t, filepath.Join(dir, "install-"+name+".ps1"), installScriptPS(r, dotenv, installDir(r, "")))
		write(t, filepath.Join(dir, "ctl-"+name+".ps1"), controlScriptPS(r, slug(r.Name, r.PluginID)))
	}
	// And the base64 path, so the parser sees that variant too.
	hostile := "PLUGIN_ID=x\n'@\nWrite-Host pwned\n"
	r := rec("Hostile", "pid-h", baseSpec())
	write(t, filepath.Join(dir, "install-base64.ps1"), installScriptPS(r, hostile, "./h"))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
