package models

import "strings"

// EntryTargetDefault is where a flow's entry data lands when its entry form
// names no target: the `_entry` key at the root of the context document. It is
// the standard slot, so a flow that does not care where its input sits reads it
// at `{{$._entry.<field>}}` and a reader of someone else's flow knows where to
// look.
const EntryTargetDefault = "$._entry"

// PrefillMode decides what a launcher shows in the entry form's fields before
// the person types anything.
//
//   - PrefillDefault: the defaults the JSON Schema itself declares — "this is
//     what the flow does if you change nothing".
//   - PrefillLive: whatever already sits at the entry target in the context the
//     run will go against — "this is what the flow ran with last time", which is
//     what a re-run of a flow over a long-lived context wants. The schema
//     defaults still fill the fields the context says nothing about.
type PrefillMode string

const (
	PrefillDefault PrefillMode = "default"
	PrefillLive    PrefillMode = "live"
)

// FlowPreferences are the flow-level, non-graph choices a flow carries. Today
// that is one thing — its entry form (see EntryForm) — and the field is an
// object rather than a bare `entry` on FlowRecord so the next flow-level
// preference does not have to widen the record again.
//
// On an upsert a nil *FlowPreferences means "this save says nothing about
// preferences" and the stored ones are kept, exactly like a nil Tags; a non-nil
// value with no entry clears them. That distinction is what keeps an MCP patch
// or an older client from silently stripping a flow's entry form.
type FlowPreferences struct {
	Entry *EntryForm `json:"entry,omitempty"`
}

// EntryForm is the form a flow asks to have filled in before it runs, and where
// the answers are seeded.
//
// It is the declarative replacement for the pattern every non-trivial flow grew
// by hand: a JS node wired after the start node whose only job is to put a fixed
// shape into the context for the rest of the graph to read. Declared here, the
// same shape is a form any launcher can render (it is the plugin form-builder
// document — JSON Schema + JSON Forms UI schema — so the renderer the plugin
// dialogs already use draws it), a contract an agent can read over MCP, and a
// validated seed the backend writes itself.
//
// Everything about it is optional: a flow with no preferences launches exactly
// as it always did.
type EntryForm struct {
	// Title / Description head the form. Empty falls back to the flow's title
	// and no description.
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	// Form is the entry document: `schema` is what the fields come from and `ui`
	// an optional JSON Forms UI schema. It is the same FormParameters a palette
	// node carries (extension `params`), so a form written for one renders in
	// the other.
	//
	// `schema` is written in either of two ways, and IsSchemaDocument tells them
	// apart:
	//
	//   - a JSON Schema — rendered by the plugin form-builder, with every
	//     control, default, enum and validation rule it supports;
	//   - a plain JSON object of the values to seed — the easy way in, rendered
	//     as a flat key/value table and seeded as written, with nothing to
	//     validate against.
	Form FormParameters `json:"form"`
	// Target is the single context location the submitted values are written to
	// (see SetAtContextPath): `$._entry` by default, `$.input.order` or `$` for a
	// flow that wants them elsewhere. It names ONE location — the seed updates
	// that slot and leaves the rest of the context document alone.
	Target string `json:"target,omitempty"`
	// Prefill picks what a launcher fills the fields with (see PrefillMode).
	// Empty means PrefillDefault.
	Prefill PrefillMode `json:"prefill,omitempty"`
	// Required blocks a launch that supplies no values and finds nothing at the
	// target already — for a flow that cannot do anything sensible without its
	// input. A flow whose entry is merely convenient leaves it false.
	Required bool `json:"required,omitempty"`
}

// Entry returns the flow's entry form, or nil when it declares none. Safe on a
// nil receiver, so every caller can ask `flow.Preferences.Entry()` without
// first checking whether the flow has preferences at all.
func (p *FlowPreferences) EntryOrNil() *EntryForm {
	if p == nil || p.Entry == nil || !p.Entry.HasForm() {
		return nil
	}
	return p.Entry
}

// HasForm reports whether this entry actually describes fields. An entry with
// an empty schema describes nothing, and is treated as "no entry" everywhere
// rather than as a form with no inputs.
func (e *EntryForm) HasForm() bool {
	return e != nil && len(e.Form.Schema) > 0
}

// TargetPath is the entry's write path, defaulted.
func (e *EntryForm) TargetPath() string {
	if e == nil {
		return EntryTargetDefault
	}
	if t := strings.TrimSpace(e.Target); t != "" {
		return t
	}
	return EntryTargetDefault
}

// PrefillOrDefault is the entry's prefill mode, defaulted.
func (e *EntryForm) PrefillOrDefault() PrefillMode {
	if e != nil && e.Prefill == PrefillLive {
		return PrefillLive
	}
	return PrefillDefault
}

// Normalize trims and defaults a preferences object on the way in, and reduces
// an entry that describes no fields to none — so a client clearing the form by
// sending an empty schema gets the same stored state as one sending no entry.
// It returns the receiver for chaining and is a no-op on nil.
func (p *FlowPreferences) Normalize() *FlowPreferences {
	if p == nil {
		return nil
	}
	e := p.Entry
	if !e.HasForm() {
		p.Entry = nil
		return p
	}
	e.Title = strings.TrimSpace(e.Title)
	e.Description = strings.TrimSpace(e.Description)
	e.Target = strings.TrimSpace(e.Target)
	if e.Target == EntryTargetDefault {
		// The default is implied, not stored, so a flow that never set a target
		// does not look like one that pinned it.
		e.Target = ""
	}
	if e.Prefill != PrefillLive {
		e.Prefill = ""
	}
	return p
}

// IsEmpty reports whether a preferences object carries nothing — what a save
// that means "clear the preferences" sends, and what is stored as `{}`.
func (p *FlowPreferences) IsEmpty() bool {
	return p == nil || p.Entry == nil
}

// IsSchemaDocument reports whether an entry document is a JSON Schema rather
// than a plain object of values.
//
// The test is deliberately narrow — a `$schema` declaration, or a non-empty
// `properties` object — because the alternative is a document whose keys are a
// person's own data, and guessing "schema" from a key like `type` would read
// `{"type": "order"}` as a contract instead of as the value it is. A document
// that is a schema describing no fields (`{"type": "object"}` alone) is a form
// with nothing in it, so it reads as plain and renders as the empty table it is.
func IsSchemaDocument(doc map[string]any) bool {
	if len(doc) == 0 {
		return false
	}
	if _, ok := doc["$schema"]; ok {
		return true
	}
	props, ok := doc["properties"].(map[string]any)
	return ok && len(props) > 0
}

// IsSchema reports whether this entry is written as a JSON Schema (see
// IsSchemaDocument). The false case is the plain-values form.
func (e *EntryForm) IsSchema() bool {
	return e != nil && IsSchemaDocument(e.Form.Schema)
}
