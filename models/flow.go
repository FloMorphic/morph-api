package models

import (
	inflowModels "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
)

// FlowRecord is a saved workflow. `ViewFlow` is the Vue Flow graph the frontend
// editor produces (nodes/edges/viewport); the inflow compiler turns it into a
// runnable flow. JSON tags mirror flomorphic-wapp's FlowRecord interface, so the
// wire shape is drop-in for the web app (view_flow, createdAt, updatedAt).
type FlowRecord struct {
	ID        string               `json:"id"`
	Title     string               `json:"title"`
	CreatedAt int64                `json:"createdAt"`
	UpdatedAt int64                `json:"updatedAt"`
	ViewFlow  inflowModels.VueFlow `json:"view_flow"`
}
