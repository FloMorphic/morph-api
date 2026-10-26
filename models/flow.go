package models

import (
	inflowModels "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
)

// FlowRecord is a saved workflow. `ViewFlow` is the Vue Flow graph the frontend
// editor produces (nodes/edges/viewport); the inflow compiler turns it into a
// runnable flow. JSON tags mirror flomorphic-wapp's FlowRecord interface, so the
// wire shape is drop-in for the web app (view_flow, createdAt, updatedAt).
//
// `Tags` are free-form labels for grouping and filtering a flow list — what a
// consumer of this install (SurfStat's "run a flow on this row" picker above
// all) narrows by, so an operator picking a flow sees the handful their
// compliance framework installed rather than every flow on the canvas. They are
// normalized on the way in (see NormalizeTags) and filtered server-side by
// GET /flow?tags=.
//
// `Preferences` are the flow-level, non-graph choices — today its entry form,
// the input a flow asks for before it runs (see FlowPreferences).
//
// `Plugins` is the one DERIVED field: the plugins the flow's wired-up
// plugin-backed nodes run through, recomputed from the graph on every save (see
// CollectFlowPlugins). A client never sets it — whatever a save sends under
// `plugins` is replaced by what the saved graph says.
//
// On an upsert, a nil Tags means "leave the stored tags alone" and an empty
// non-nil slice means "clear them" — so a client that knows nothing about tags
// (an MCP patch, an older build) cannot silently strip the labels a package
// stamped on the flow. `json:"tags,omitempty"` keeps that distinction on the
// wire in both directions, and nil Preferences carries the same meaning.
type FlowRecord struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Tags  []string `json:"tags,omitempty"`
	// Preferences is nil when a save says nothing about them (keep what is
	// stored); a non-nil value with no entry clears them.
	Preferences *FlowPreferences `json:"preferences,omitempty"`
	// Plugins is derived from the graph on save, not sent by a client.
	Plugins   []FlowPlugin         `json:"plugins,omitempty"`
	CreatedAt int64                `json:"createdAt"`
	UpdatedAt int64                `json:"updatedAt"`
	ViewFlow  inflowModels.VueFlow `json:"view_flow"`
}

// FlowSummary is one row of the flow list: everything a list shows about a
// saved flow, minus the graph.
//
// The graph is the whole weight of a FlowRecord — a real flow's `view_flow` is
// tens of kilobytes of nodes, edges and canvas geometry — and no list draws it.
// A list shows a title, labels, dates and how many nodes a flow has; the editor
// loads the one flow it opens with GET /flow/id/:id. Shipping the graph with
// every row turned a page of flows into megabytes of JSON no caller read, which
// is what made the flow picker in a consuming app (the SurfStat / Venpace
// "run a flow on this row" dialog) slow to open.
//
// The field is absent rather than `json:"-"` on the shared record on purpose:
// the type is what keeps the column out of the SELECT too, so a list never
// reads 35 KB of graph per row off the database or spends the JSON decode on
// it (see the repositories' List).
//
// NodeCount is counted in SQL over the stored graph, so the node badge a list
// draws survives the graph itself staying in the database. It is 0 for a flow
// whose graph is empty or was never saved.
//
// Plugins rides along for the same reason, and is the clearest case for storing
// it: "which plugins does this flow need, and which of them are builtins" is a
// question a flow list answers, and it is the one thing about a flow's graph
// that a list could not derive without loading every graph it shows.
type FlowSummary struct {
	ID          string           `json:"id"`
	Title       string           `json:"title"`
	Tags        []string         `json:"tags,omitempty"`
	Preferences *FlowPreferences `json:"preferences,omitempty"`
	Plugins     []FlowPlugin     `json:"plugins,omitempty"`
	NodeCount   int              `json:"nodeCount"`
	CreatedAt   int64            `json:"createdAt"`
	UpdatedAt   int64            `json:"updatedAt"`
}
