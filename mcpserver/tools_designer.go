package mcpserver

import (
	"context"
	"strings"

	"github.com/FloMorphic/morph-api/api/wslog"
	"github.com/FloMorphic/morph-api/designer"
	"github.com/FloMorphic/morph-api/inflow"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	compiler "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// patchGuide is the compact contract for the patch authoring tools. The FULL
// design guidance — scope-is-a-queue, the many-scope routing limit, `$this` vs
// `$.path`, sequence-vs-parallel, promissall joining, every node kind's fields —
// is the `flo_design_workflow` PROMPT, which is the exact prompt the web app's
// build-with-AI dialog uses. Get that prompt first; this only names the shape.
const patchGuide = `A workflow is authored as a GRAPH PATCH — the readable form the designer guide teaches — not raw Vue-Flow. Call flo_get_design_guide FIRST (same guidance as the app's AI builder — scope/branching rules, $this per-row templates, wiring, joining, and every node kind's fields) before writing one.

Patch shape:
- nodes: [{ ref, kind, title, key?, scope?, data?, note? }]. ref is a local name YOU invent; edges refer to nodes by ref (real ids are assigned on apply). scope defaults to "$". One node MUST be kind "startNode".
- edges: [{ from, to, port?, note? }]. from/to are refs (or ids of nodes already on the canvas). port is REQUIRED only when the source has derived output ports — an LLM function name, a Rule handler name, or "_exception" — and is resolved here to the edge route tags the engine reads.
- notes: [free-text] — anything a human must finish by hand.

Two rules that bite hardest (the guide explains them fully):
- A many-valued scope ("$.items[*]") runs the node once per element — a QUEUE inside the one node, not a branch. Its text templates must read the current element with "{{$this.field}}"; "{{$.field}}" reads the root every pass. And a routing node (LLM with functions, Rule with handlers) must NOT take a many-valued scope.
- A promissall (Wait for All) only belongs where 2+ edges converge. After a single node — scoped or not — it has nothing to join.

Node "data" carries the kind-specific fields (llm functions, rule handlers/logic_rule, http body, cast mappings, …); the guide documents them per kind. Defaults are merged for you, so send only what differs.
flo_plan_patch converts + compiles a draft (no save); flo_apply_patch converts + saves it (same result as a hand-drawn flow). Both return a "problems" list flagging exactly the mistakes above (a routing/template many-scope error, a no-op promissall, branches converging without one) — read it and fix before/after applying.`

// registerDesignerTools exposes the AI workflow-designer surface over MCP at
// parity with the web app: the `flo_design_workflow` prompt (the same
// designer.BuildPrompt the REST /designer endpoint serves the build-AI dialog),
// and the patch tools that convert a designer patch into a Vue-Flow graph
// (designer.PlanPatch, the Go port of the front's planPatch) and plan or save
// it. So an agent designs a flow from the same brain a person does, then lands
// it through the same compiler + upsert.
func registerDesignerTools(s *server.MCPServer, store repository.Store) {
	flows := store.Workflows()

	// The prompt: fetched like the front's POST /designer/prompt. `goal` is the
	// workflow to build; `graph_id` optionally loads an existing flow so the
	// prompt lists its node ids for the model to wire into.
	s.AddPrompt(mcp.NewPrompt("flo_design_workflow",
		mcp.WithPromptDescription("The FloMorphic workflow-designer prompt — the exact guidance the web app's build-with-AI dialog uses (node catalog, scope/branching rules, wiring and joining, and the plugin actions available in this install). Returns instructions to emit a GRAPH PATCH; author it with flo_plan_patch / flo_apply_patch."),
		mcp.WithArgument("goal", mcp.ArgumentDescription("What the workflow should do — described in plain language."), mcp.RequiredArgument()),
		mcp.WithArgument("graph_id", mcp.ArgumentDescription("Optional id of an existing workflow (flow_…) to extend — its nodes are listed so the patch can wire into them.")),
	), func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		prompt := designGuide(ctx, store, req.Params.Arguments["goal"], req.Params.Arguments["graph_id"])
		return mcp.NewGetPromptResult(
			"FloMorphic workflow designer",
			[]mcp.PromptMessage{mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(prompt))},
		), nil
	})

	// The same guidance as a TOOL. MCP prompts are surfaced for a human to invoke
	// and are not delivered to the model automatically, and some clients expose no
	// prompt/resource surface at all — so an agent authoring a patch on its own
	// needs a way to PULL the designer instructions. This returns the exact same
	// designer.BuildPrompt text the prompt does.
	s.AddTool(mcp.NewTool("flo_get_design_guide",
		mcp.WithDescription("Get the FloMorphic workflow-designer guide — the SAME instructions the web app's build-with-AI dialog uses (node catalog, scope/branching rules incl. $this per-row templates, wiring and joining, and this install's plugin actions). READ THIS FIRST before authoring a graph patch with flo_plan_patch / flo_apply_patch."),
		mcp.WithString("goal", mcp.Description("optional: the workflow to build, echoed into the guide's Task section")),
		mcp.WithString("graph_id", mcp.Description("optional id of an existing workflow (flow_…) to extend — its nodes are listed to wire into")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		guide := designGuide(ctx, store, req.GetString("goal", ""), req.GetString("graph_id", ""))
		return mcp.NewToolResultText(guide), nil
	})

	patchArgs := func(extra ...mcp.ToolOption) []mcp.ToolOption {
		base := []mcp.ToolOption{
			mcp.WithArray("nodes", mcp.Required(), mcp.Description("Patch nodes: [{ref, kind, title, key?, scope?, data?, note?}]"),
				mcp.Items(map[string]any{"type": "object"})),
			mcp.WithArray("edges", mcp.Description("Patch edges: [{from, to, port?, note?}]"),
				mcp.Items(map[string]any{"type": "object"})),
		}
		return append(base, extra...)
	}

	s.AddTool(mcp.NewTool("flo_plan_patch",
		withOpts(patchArgs(),
			mcp.WithDescription("Convert a CANDIDATE graph patch into a Vue-Flow graph and compile it — no save. Returns the lowered node graph on success (or the compile error), plus any design problems. Use it to check a draft before flo_apply_patch.\n\n"+patchGuide),
		)...,
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		patch, bad := parsePatchInput(req)
		if bad != nil {
			return bad, nil
		}
		graph, problems := designer.PlanPatch(*patch, nil)
		rec := models.FlowRecord{Title: "draft", ViewFlow: graph}
		startNodeID, nodes, err := inflow.FLowCompiler(rec)
		if err != nil {
			return jsonResult(map[string]any{"ok": false, "error": err.Error(), "problems": problems, "graph": graph})
		}
		return jsonResult(map[string]any{"ok": true, "startNodeId": startNodeID, "nodes": nodes, "problems": problems, "graph": graph})
	})

	s.AddTool(mcp.NewTool("flo_apply_patch",
		withOpts(patchArgs(
			mcp.WithString("id", mcp.Description("workflow id; empty creates a new one")),
			mcp.WithString("title", mcp.Required(), mcp.Description("workflow title")),
		),
			mcp.WithDescription("Convert a graph patch into a Vue-Flow graph and SAVE it as a workflow — the same result as drawing it in the editor. Returns the saved workflow and any design problems. Structural rule: the patch must contain exactly one startNode.\n\n"+patchGuide),
		)...,
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		patch, bad := parsePatchInput(req)
		if bad != nil {
			return bad, nil
		}
		title := strings.TrimSpace(req.GetString("title", ""))
		if title == "" {
			return mcp.NewToolResultError("title is required"), nil
		}
		graph, problems := designer.PlanPatch(*patch, nil)
		if starts := countType(graph, inflow.NODE_START); starts != 1 {
			return mcp.NewToolResultError(`the patch must contain exactly one node of kind "startNode"`), nil
		}
		rec := models.FlowRecord{ID: req.GetString("id", ""), Title: title, ViewFlow: graph}
		// Same headless-author normalisation + upsert the raw /flow path uses, so
		// a patch-built flow lays out and renders like any other.
		inflow.NormalizeGraph(&rec)
		if err := flows.Upsert(ctx, &rec); err != nil {
			return repoError(err, "workflow not saved")
		}
		wslog.Emit("flow.changed", map[string]any{"id": rec.ID, "source": "mcp"})
		return jsonResult(map[string]any{"flow": rec, "problems": problems})
	})
}

// parsePatchInput reads the {nodes[], edges[]} authoring arguments into a
// designer.Patch. The conversion (designer.PlanPatch) validates each node's kind
// and wiring, so this only guards the outer shape.
func parsePatchInput(req mcp.CallToolRequest) (*designer.Patch, *mcp.CallToolResult) {
	var in struct {
		Nodes []designer.PatchNode `json:"nodes"`
		Edges []designer.PatchEdge `json:"edges"`
		Notes []string             `json:"notes"`
	}
	if err := req.BindArguments(&in); err != nil {
		return nil, mcp.NewToolResultErrorFromErr("invalid patch payload", err)
	}
	if len(in.Nodes) == 0 {
		return nil, mcp.NewToolResultError("a patch needs at least one node")
	}
	return &designer.Patch{Nodes: in.Nodes, Edges: in.Edges, Notes: in.Notes}, nil
}

// designGuide assembles the designer prompt — designer.BuildPrompt, the single
// source shared with the web app's build-AI dialog — for an optional goal and,
// when graph_id names an existing flow, its nodes to wire into. Backs both the
// flo_design_workflow prompt and the flo_get_design_guide tool so the two can
// never diverge.
func designGuide(ctx context.Context, store repository.Store, goal, graphID string) string {
	in := designer.Input{
		Goal:    goal,
		Plugins: pluginActions(ctx, store),
	}
	if id := strings.TrimSpace(graphID); id != "" {
		if rec, err := store.Workflows().GetByID(ctx, id); err == nil {
			in.Nodes = canvasNodes(rec)
		}
	}
	return designer.BuildPrompt(in)
}

// canvasNodes maps a saved flow's nodes to the designer's canvas shape (id /
// type / title) — the ids a patch may wire into. Mirrors the REST controller's
// canvasNodes.
func canvasNodes(rec *models.FlowRecord) []designer.CanvasNode {
	out := make([]designer.CanvasNode, 0, len(rec.ViewFlow.Nodes))
	for _, n := range rec.ViewFlow.Nodes {
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
// (extension rows with a non-empty Action + PluginID), named by their plugin's
// registration row — the "Plugins available" section of the prompt. Mirrors the
// REST designer controller; a read error just omits the section.
func pluginActions(ctx context.Context, store repository.Store) []designer.PluginAction {
	items, _, err := store.Extensions().List(ctx, repository.ListParams{
		Kind:  string(models.KindExtension),
		Limit: 500,
	})
	if err != nil {
		return nil
	}
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

// countType counts nodes of a Vue-Flow type in a planned graph.
func countType(g compiler.VueFlow, typ string) int {
	n := 0
	for _, node := range g.Nodes {
		if node.Type == typ {
			n++
		}
	}
	return n
}
