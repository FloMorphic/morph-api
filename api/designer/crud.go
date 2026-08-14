package designerControllers

import (
	"strings"

	"github.com/FloMorphic/morph-api/designer"
	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	compiler "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
	"github.com/gofiber/fiber/v3"
)

// promptRequest is the body of POST /designer/prompt: the user's goal and,
// optionally, the graph currently on the canvas (so the prompt can list ids the
// model may wire into). Plugins are NOT sent by the client — the backend resolves
// them from the extension table.
type promptRequest struct {
	Goal  string            `json:"goal"`
	Graph *compiler.VueFlow `json:"graph"`
}

// prompt handles POST /designer/prompt — assemble the designer prompt from the
// central builder and return it as `{ prompt }`.
func (ctl *controller) prompt(c fiber.Ctx) error {
	var in promptRequest
	if err := c.Bind().Body(&in); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid prompt payload")
	}

	out := designer.BuildPrompt(designer.Input{
		Goal:    in.Goal,
		Nodes:   canvasNodes(in.Graph),
		Plugins: ctl.pluginActions(c),
	})
	return etc.OK(c, fiber.Map{"prompt": out})
}

// canvasNodes maps the on-canvas graph to the builder's node shape (id / type /
// title). The heavy Vue-Flow fields are irrelevant to the prompt.
func canvasNodes(graph *compiler.VueFlow) []designer.CanvasNode {
	if graph == nil {
		return nil
	}
	out := make([]designer.CanvasNode, 0, len(graph.Nodes))
	for _, n := range graph.Nodes {
		title := ""
		if data, ok := n.Data.(map[string]any); ok {
			if t, ok := data["title"].(string); ok {
				title = t
			}
		}
		out = append(out, designer.CanvasNode{ID: n.ID, Type: n.Type, Title: title})
	}
	return out
}

// pluginActions resolves the imported-plugin actions available in this install
// from the extension table — the rows with a non-empty Action and PluginID —
// naming each by its plugin's registration row. Mirrors the front side's
// fetchPluginActions. A read error is non-fatal: the prompt just omits the
// Plugins section, exactly as it does when nothing is imported.
func (ctl *controller) pluginActions(c fiber.Ctx) []designer.PluginAction {
	items, _, err := ctl.store.Extensions().List(c.Context(), repository.ListParams{
		Kind:  string(models.KindExtension),
		Limit: 500,
	})
	if err != nil {
		return nil
	}
	// Registration rows (no Action) name each plugin.
	names := map[string]string{}
	for i := range items {
		if items[i].Action == "" && items[i].PluginID != "" {
			names[items[i].PluginID] = items[i].Name
		}
	}
	out := []designer.PluginAction{}
	for i := range items {
		row := &items[i]
		if row.Action == "" || row.PluginID == "" {
			continue
		}
		name := names[row.PluginID]
		if name == "" {
			name = row.PluginID
		}
		out = append(out, designer.PluginAction{
			PluginID:    row.PluginID,
			PluginName:  name,
			Action:      row.Action,
			Label:       row.Name,
			Description: strings.TrimSpace(row.Description),
		})
	}
	return out
}
