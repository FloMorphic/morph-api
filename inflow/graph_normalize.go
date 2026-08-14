package inflow

import "github.com/FloMorphic/morph-api/models"

// NormalizeGraph fills the cosmetic Vue-Flow fields a headless author (MCP, an
// import, a script) tends to omit, so a graph produced without the visual editor
// still opens correctly in it. It is the single source of truth for graph
// presentation defaults, shared by the REST `/flow` upsert and the MCP
// authoring tool so the two can never drift (the classic symptom being a graph
// that saved fine but rendered blank).
//
// It is deliberately ADDITIVE and idempotent: it only ever fills a zero/invalid
// value, never overwrites one the caller supplied. A graph the visual editor
// produced — every node placed and sized, a real viewport zoom — passes through
// untouched, so wiring this into the editor's own save path changes nothing.
// The one auto-layout step is gated on the WHOLE graph being unpositioned, which
// only happens for a headless author; a human save (any node carrying a real
// position) never triggers it, so a node a person parked at the origin is left
// exactly where they put it.
func NormalizeGraph(f *models.FlowRecord) {
	const (
		colW, rowH = 320.0, 160.0
		perRow     = 4
		defW, defH = 240.0, 120.0
	)

	nodes := f.ViewFlow.Nodes

	// Auto-layout only a graph with no placement at all (headless author). If any
	// node carries a real position, the graph came from something that placed its
	// nodes — respect every position as given.
	allUnplaced := true
	for i := range nodes {
		n := &nodes[i]
		if n.Position.X != 0 || n.Position.Y != 0 || n.ComputedPosition.X != 0 || n.ComputedPosition.Y != 0 {
			allUnplaced = false
			break
		}
	}

	for i := range nodes {
		n := &nodes[i]
		if allUnplaced {
			n.Position.X = float64(i%perRow) * colW
			n.Position.Y = float64(i/perRow) * rowH
			n.Initialized = true
		}
		// Keep computedPosition in step with position when it was left empty (the
		// editor keeps the two equal; the compiler ignores both).
		if n.ComputedPosition.X == 0 && n.ComputedPosition.Y == 0 {
			n.ComputedPosition.X, n.ComputedPosition.Y = n.Position.X, n.Position.Y
		}
		if n.Dimensions.Width == 0 {
			n.Dimensions.Width = defW
		}
		if n.Dimensions.Height == 0 {
			n.Dimensions.Height = defH
		}
	}

	// The viewport transform. A zero zoom scales the whole canvas to nothing, so a
	// graph saved with the default zero viewport opens BLANK even when its nodes
	// are placed correctly. A real editor save never carries zoom 0, so defaulting
	// it to 1 (identity) is a no-op there.
	if f.ViewFlow.Position.Zoom == 0 {
		f.ViewFlow.Position.Zoom = 1
	}
}
