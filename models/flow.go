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
// On an upsert, a nil Tags means "leave the stored tags alone" and an empty
// non-nil slice means "clear them" — so a client that knows nothing about tags
// (an MCP patch, an older build) cannot silently strip the labels a package
// stamped on the flow. `json:"tags,omitempty"` keeps that distinction on the
// wire in both directions.
type FlowRecord struct {
	ID        string               `json:"id"`
	Title     string               `json:"title"`
	Tags      []string             `json:"tags,omitempty"`
	CreatedAt int64                `json:"createdAt"`
	UpdatedAt int64                `json:"updatedAt"`
	ViewFlow  inflowModels.VueFlow `json:"view_flow"`
}
