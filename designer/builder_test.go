package designer

import (
	"strings"
	"testing"
)

func TestBuildPromptSections(t *testing.T) {
	out := BuildPrompt(Input{Goal: "Classify support tickets and escalate the urgent ones"})

	for _, want := range []string{
		"You are a workflow designer for FloMorphic",
		"## Output shape",
		"## Scope — and the loop it hides",
		"## Wiring",
		"## Joining branches back together",
		"## Node kinds",
		"## Canvas",
		"## Task",
		"Classify support tickets and escalate the urgent ones",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt missing section/text: %q", want)
		}
	}

	// Every catalog kind must render a heading.
	for _, k := range catalog {
		if !strings.Contains(out, "### "+k.Kind+" — "+k.Label) {
			t.Errorf("prompt missing node kind heading for %q", k.Kind)
		}
	}

	// The goto guidance (the misuse the user hit) must be present.
	if !strings.Contains(out, "A Goto is a subroutine call") {
		t.Error("prompt missing goto usage note")
	}

	// An empty canvas must instruct exactly one startNode.
	if !strings.Contains(out, "The canvas is empty. Include exactly one `startNode`") {
		t.Error("empty-canvas instruction missing")
	}

	// No plugins → no Plugins section.
	if strings.Contains(out, "## Plugins available") {
		t.Error("Plugins section rendered with no plugins")
	}
}

func TestBuildPromptDataFields(t *testing.T) {
	out := BuildPrompt(Input{Goal: "x"})
	// Compact JSON, key order preserved from the catalog (not alphabetised).
	if !strings.Contains(out, `data fields (with defaults): {"mode":"hour","value":1,"at":""}`) {
		t.Error("until data-fields line not rendered compactly / in order")
	}
	// A kind with no extra fields gets the "none" line.
	if !strings.Contains(out, "### startNode — Start\nEntry point.") {
		t.Error("startNode block malformed")
	}
	if !strings.Contains(out, "data fields: none beyond title / key / scope.") {
		t.Error("'none' data-fields line missing")
	}
}

func TestBuildPromptCanvasAndPlugins(t *testing.T) {
	out := BuildPrompt(Input{
		Goal:  "wire it",
		Nodes: []CanvasNode{{ID: "n1", Type: "llm", Title: "Classify"}},
		Plugins: []PluginAction{
			{PluginID: "jira", PluginName: "Jira", Action: "create_issue", Label: "Create issue", Description: "open a ticket"},
			{PluginID: "jira", PluginName: "Jira", Action: "add_comment", Label: "Add comment"},
		},
	})

	if !strings.Contains(out, "## Plugins available") {
		t.Fatal("Plugins section missing")
	}
	if !strings.Contains(out, "### Jira (`pluginId: \"jira\"`)") {
		t.Error("plugin group heading missing")
	}
	if !strings.Contains(out, "- `action: \"create_issue\"` — Create issue: open a ticket") {
		t.Error("plugin action line (with description) missing")
	}
	if !strings.Contains(out, "- `action: \"add_comment\"` — Add comment") {
		t.Error("plugin action line (no description) missing")
	}
	if !strings.Contains(out, `- n1 · llm · "Classify"`) {
		t.Error("canvas node line missing")
	}
	// A canvas with no startNode must nudge for one.
	if !strings.Contains(out, "There is no Start node yet — include one.") {
		t.Error("missing 'no start node' nudge")
	}
}
