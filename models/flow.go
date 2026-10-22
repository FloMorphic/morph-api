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
	Preferences *FlowPreferences     `json:"preferences,omitempty"`
	CreatedAt   int64                `json:"createdAt"`
	UpdatedAt   int64                `json:"updatedAt"`
	ViewFlow    inflowModels.VueFlow `json:"view_flow"`
}
