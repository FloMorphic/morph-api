package extensionControllers

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FloMorphic/morph-api/models"
	"github.com/google/uuid"
)

// The installer is text we hand a user to pipe into a shell, so the one thing a
// test must guarantee is that every shape of it parses as bash — a stray quote
// in a repo URL or an env value would otherwise only surface on their machine.
func TestInstallScriptParsesAsBash(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	cases := []struct {
		name string
		rec  models.ExtensionRecord
	}{
		{
			name: "go plugin, explicit runtime and subdir",
			rec: models.ExtensionRecord{
				ID: "ext_1", Name: "Jira Connector", PluginID: "jira-connector-8f3a",
				Install: models.InstallSpec{
					Repo: "https://github.com/acme/inflow-plugins", Ref: "v1.2.0",
					Subdir: "jira", Runtime: models.RuntimeGo, EnvFile: ".env.morph",
					Env: []models.EnvVar{{Key: "JIRA_URL", Value: "https://acme.atlassian.net"}},
				},
			},
		},
		{
			name: "node plugin, auto runtime, no ref",
			rec: models.ExtensionRecord{
				ID: "ext_2", Name: "Slack", PluginID: "slack-01",
				Install: models.InstallSpec{Repo: "git@github.com:acme/slack-plugin.git"},
			},
		},
		{
			name: "docker plugin, name with nothing sluggable",
			rec: models.ExtensionRecord{
				ID: "ext_3", Name: "***", PluginID: "weird-99",
				Install: models.InstallSpec{
					Repo: "https://example.com/r.git", Runtime: models.RuntimeDocker,
					// Values a naive renderer would break on.
					Env: []models.EnvVar{{Key: "QUOTE", Value: `a "b" 'c' $d ` + "`e`"}},
				},
			},
		},
	}

	dir := t.TempDir()
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dotenv := pluginEnvFile(tc.rec.PluginID, "Y3JlZA==", tc.rec.Install.Env)
			script := installScript(&tc.rec, dotenv, installDir(&tc.rec, ""))

			path := filepath.Join(dir, strings.ReplaceAll(tc.rec.ID, "/", "_")+".sh")
			if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
				t.Fatalf("write script: %v", err)
			}
			if out, err := exec.Command("bash", "-n", path).CombinedOutput(); err != nil {
				t.Fatalf("case %d is not valid bash: %v\n%s\n---\n%s", i, err, out, script)
			}
			// The credential and the plugin id must survive into the heredoc, or
			// the plugin starts unauthenticated and the whole flow is pointless.
			if !strings.Contains(script, "INFRA_CRED=Y3JlZA==") {
				t.Error("credential missing from generated env")
			}
			if !strings.Contains(script, "PLUGIN_ID="+tc.rec.PluginID) {
				t.Error("plugin id missing from generated env")
			}
		})
	}
}

// A plugin id lands in `inflow.v1.<id>.…`, so anything that would split a
// subject or make it a wildcard has to be gone before it is issued — and the
// uuid half has to actually be there, since that is all that keeps two imports
// of the same plugin apart.
func TestNewPluginID(t *testing.T) {
	const uuidLen = 36
	for _, name := range []string{
		"Jira Connector", "a.b.c", "wild*card", "greater>than", "  ", "***",
		"a name so long that it would bloat every single subject it appears in",
	} {
		id := newPluginID(name)
		if strings.ContainsAny(id, ".*> \t\n") {
			t.Errorf("newPluginID(%q) = %q: not subject-safe", name, id)
		}
		if len(id) < uuidLen+1 {
			t.Errorf("newPluginID(%q) = %q: uuid half missing", name, id)
		}
		if _, err := uuid.Parse(id[len(id)-uuidLen:]); err != nil {
			t.Errorf("newPluginID(%q) = %q: tail is not a uuid", name, id)
		}
		if prefix := len(id) - uuidLen - 1; prefix > pluginIDNameMax {
			t.Errorf("newPluginID(%q) = %q: name half %d chars, max %d", name, id, prefix, pluginIDNameMax)
		}
	}
	// Two imports under the same name must not collide.
	if newPluginID("Jira") == newPluginID("Jira") {
		t.Error("newPluginID is not unique per call")
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Jira Connector": "jira-connector",
		"  My  Node!!  ": "my-node",
		"***":            "fallback-id",
		"":               "fallback-id",
		"HTTP/2 Client":  "http-2-client",
	}
	for in, want := range cases {
		if got := slug(in, "fallback-id"); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

// The three SDK variables come first and in a fixed order; declared extras
// follow, with blank keys dropped rather than written as `=value`.
func TestPluginEnvFile(t *testing.T) {
	got := pluginEnvFile("p1", "Y3JlZA==", []models.EnvVar{
		{Key: "A", Value: "1"},
		{Key: "  ", Value: "ignored"},
		{Key: "B", Value: ""},
	})
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	want := []string{"PLUGIN_ID=p1", "INFRA_URL=" + infraNatsURL(), "INFRA_CRED=Y3JlZA==", "A=1", "B="}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines %q, want %d", len(lines), lines, len(want))
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}
