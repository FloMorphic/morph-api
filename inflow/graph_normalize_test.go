package inflow

import (
	"testing"

	"github.com/FloMorphic/morph-api/models"
	compiler "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
)

// A complete graph — the shape the visual editor produces — must pass through
// NormalizeGraph untouched. This is the guarantee that wiring the normalizer
// into the editor's own save path changes nothing.
func TestNormalizeGraph_CompleteGraphUnchanged(t *testing.T) {
	f := models.FlowRecord{ViewFlow: compiler.VueFlow{
		Position: compiler.FlowPosition{X: 12, Y: 34, Zoom: 1.5},
		Nodes: []compiler.VueFlowNode{
			{ID: "a", Type: "startNode", Position: compiler.Position{X: 80, Y: 200},
				ComputedPosition: compiler.ComputedPosition{X: 80, Y: 200},
				Dimensions:       compiler.Dimensions{Width: 240, Height: 120}},
			{ID: "b", Type: "cast", Position: compiler.Position{X: 360, Y: 200},
				ComputedPosition: compiler.ComputedPosition{X: 360, Y: 200},
				Dimensions:       compiler.Dimensions{Width: 240, Height: 120}},
		},
	}}

	NormalizeGraph(&f)

	if f.ViewFlow.Position != (compiler.FlowPosition{X: 12, Y: 34, Zoom: 1.5}) {
		t.Errorf("viewport changed: %+v", f.ViewFlow.Position)
	}
	if p := f.ViewFlow.Nodes[1].Position; p.X != 360 || p.Y != 200 {
		t.Errorf("node b moved to %+v, want {360 200}", p)
	}
}

// A headless graph — every node at the origin, zero viewport — gets laid out and
// a usable zoom, so it renders instead of collapsing to a blank canvas.
func TestNormalizeGraph_HeadlessGraphLaidOut(t *testing.T) {
	f := models.FlowRecord{ViewFlow: compiler.VueFlow{
		Nodes: []compiler.VueFlowNode{
			{ID: "a", Type: "startNode"},
			{ID: "b", Type: "cast"},
		},
	}}

	NormalizeGraph(&f)

	if f.ViewFlow.Position.Zoom != 1 {
		t.Errorf("zoom = %v, want 1", f.ViewFlow.Position.Zoom)
	}
	a, b := f.ViewFlow.Nodes[0], f.ViewFlow.Nodes[1]
	if a.Position == b.Position {
		t.Errorf("nodes overlap at %+v — layout not applied", a.Position)
	}
	if a.Dimensions.Width == 0 || b.Dimensions.Height == 0 {
		t.Error("dimensions not defaulted")
	}
}

// A partially-placed graph (a human parked one node at the origin) must NOT be
// auto-laid-out — every position is respected — but a zero viewport zoom is
// still repaired.
func TestNormalizeGraph_PartiallyPlacedRespected(t *testing.T) {
	f := models.FlowRecord{ViewFlow: compiler.VueFlow{
		Nodes: []compiler.VueFlowNode{
			{ID: "a", Type: "startNode"}, // at origin on purpose
			{ID: "b", Type: "cast", Position: compiler.Position{X: 500, Y: 20}},
		},
	}}

	NormalizeGraph(&f)

	if p := f.ViewFlow.Nodes[0].Position; p.X != 0 || p.Y != 0 {
		t.Errorf("origin node was moved to %+v — layout should not run on a placed graph", p)
	}
	if f.ViewFlow.Position.Zoom != 1 {
		t.Errorf("zoom = %v, want 1", f.ViewFlow.Position.Zoom)
	}
}
