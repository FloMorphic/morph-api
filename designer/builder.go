// Package designer is the central builder for the AI "workflow designer" prompt —
// the instructions an assistant needs to emit a node/edge patch for a FloMorphic
// workflow. It is the single source of that prompt, shared by the REST endpoint
// the web app's build-ai dialog calls, and (later) the MCP workflow tools, so the
// guidance can never drift between where a human designs a flow and where an
// agent does.
//
// The content is data, not code: the static instruction block lives in
// assets/preamble.md and the per-node catalog in assets/catalog.json, both
// embedded. Tuning the prompt or adding a node kind is an edit to those files.
// This mirrors the front side's buildDesignerPrompt (flomorphic-wapp
// src/lib/aiGraph.ts) built from NODE_SPECS — the port those files were lifted
// from.
package designer

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed assets/preamble.md
var preamble string

//go:embed assets/catalog.json
var catalogJSON []byte

// nodeKind is one entry of the node catalog. dataFields is the kind's non-hoisted
// `data` defaults (everything beyond title/key/scope), kept as raw JSON so its
// key order is preserved verbatim in the rendered prompt. notes are the extra
// per-kind instruction lines.
type nodeKind struct {
	Kind        string          `json:"kind"`
	Label       string          `json:"label"`
	Tagline     string          `json:"tagline"`
	Description string          `json:"description"`
	Primitives  string          `json:"primitives"`
	DataFields  json.RawMessage `json:"dataFields,omitempty"`
	Notes       []string        `json:"notes,omitempty"`
}

var catalog []nodeKind

func init() {
	if err := json.Unmarshal(catalogJSON, &catalog); err != nil {
		// A malformed embedded catalog is a build-time authoring error, not a
		// runtime condition — fail loud so it is caught immediately.
		panic("designer: invalid catalog.json: " + err.Error())
	}
	indexCatalog()
}

// CanvasNode is one node already on the designer's canvas — enough for the prompt
// to tell the model which ids it may wire into.
type CanvasNode struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

// PluginAction is one imported-plugin action available in this install, listed so
// the model can drop a `plugin` node for it instead of inventing an integration.
type PluginAction struct {
	PluginID    string `json:"pluginId"`
	PluginName  string `json:"pluginName"`
	Action      string `json:"action"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Input is everything BuildPrompt needs beyond the static catalog: the goal, the
// current canvas (may be empty), and the plugin actions available here.
type Input struct {
	Goal    string
	Nodes   []CanvasNode
	Plugins []PluginAction
}

// BuildPrompt assembles the designer prompt: the static preamble, the node-kinds
// section rendered from the catalog, the plugins available in this install, the
// canvas snapshot, and the task. Mirrors buildDesignerPrompt on the front side.
func BuildPrompt(in Input) string {
	// Reconstruct the prompt as a line list, exactly as the front side does. The
	// preamble already ends at the "## Node kinds" heading.
	lines := strings.Split(strings.TrimRight(preamble, "\n"), "\n")

	for _, k := range catalog {
		lines = append(lines,
			"",
			fmt.Sprintf("### %s — %s", k.Kind, k.Label),
			fmt.Sprintf("%s. %s", k.Tagline, k.Description),
			fmt.Sprintf("Compiles to: %s.", k.Primitives),
			dataFieldsLine(k.DataFields),
		)
		lines = append(lines, k.Notes...)
	}

	lines = append(lines, pluginLines(in.Plugins)...)
	lines = append(lines, canvasLines(in.Nodes)...)
	lines = append(lines, "", "## Task", firstNonEmpty(strings.TrimSpace(in.Goal), "(describe the workflow to build)"))

	return strings.Join(lines, "\n")
}

// dataFieldsLine renders a kind's `data fields` line: the compact JSON of its
// non-hoisted defaults, or the "none" note when it has none.
func dataFieldsLine(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "data fields: none beyond title / key / scope."
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return "data fields: none beyond title / key / scope."
	}
	return "data fields (with defaults): " + buf.String()
}

// pluginLines renders the "## Plugins available" section, grouping actions by the
// plugin they came from (first-seen order). Empty when nothing is imported — then
// the prompt simply has no Plugins section and the model stays on the builtins.
func pluginLines(plugins []PluginAction) []string {
	if len(plugins) == 0 {
		return nil
	}
	lines := []string{
		"",
		"## Plugins available",
		"These imported-plugin actions are registered in this install. Use one by adding a node with `kind: \"plugin\"` and `data: { \"pluginId\": \"<id>\", \"action\": \"<action>\" }` (copy both verbatim from the list), a `title`, and a `note`. Do NOT invent a plugin or an action that is not listed, and do NOT fill in the action's own input fields or a settings profile — the designer completes those in the drawer after import. A plugin node has a single output port; wire it like any other node.",
	}

	order := []string{}
	grouped := map[string][]PluginAction{}
	names := map[string]string{}
	for _, p := range plugins {
		if _, seen := grouped[p.PluginID]; !seen {
			order = append(order, p.PluginID)
			name := p.PluginName
			if name == "" {
				name = p.PluginID
			}
			names[p.PluginID] = name
		}
		grouped[p.PluginID] = append(grouped[p.PluginID], p)
	}

	for _, id := range order {
		lines = append(lines, "", fmt.Sprintf("### %s (`pluginId: \"%s\"`)", names[id], id))
		for _, a := range grouped[id] {
			line := fmt.Sprintf("- `action: \"%s\"` — %s", a.Action, a.Label)
			if desc := strings.TrimSpace(a.Description); desc != "" {
				line += ": " + desc
			}
			lines = append(lines, line)
		}
	}
	return lines
}

// canvasLines renders the "## Canvas" section: the ids already placed (to wire
// into) or the empty-canvas instruction.
func canvasLines(nodes []CanvasNode) []string {
	lines := []string{"", "## Canvas"}
	if len(nodes) == 0 {
		lines = append(lines, "The canvas is empty. Include exactly one `startNode` as the entry point.")
		return lines
	}
	lines = append(lines, "Nodes already on the canvas — use these ids verbatim in `edges` to connect to them (do NOT re-create them):")
	hasStart := false
	for _, n := range nodes {
		if n.Type == "startNode" {
			hasStart = true
		}
		title := strings.TrimSpace(n.Title)
		suffix := ""
		if title != "" {
			suffix = fmt.Sprintf(" · %q", title)
		}
		lines = append(lines, fmt.Sprintf("- %s · %s%s", n.ID, n.Type, suffix))
	}
	if !hasStart {
		lines = append(lines, "There is no Start node yet — include one.")
	}
	return lines
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
