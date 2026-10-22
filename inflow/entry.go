package inflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// A flow's entry form, at runtime.
//
// A flow can declare the input it needs as a form (models.EntryForm). This is
// the half that runs: it resolves what a launcher should show, validates what
// comes back against the flow's own JSON Schema, and seeds the result into the
// one context location the entry's target names — the declarative replacement
// for the JS node after the start node whose only job was to put a fixed shape
// into the context.
//
// Every launch path goes through here (REST POST /process, the MCP
// flo_start_process tool, a scheduled row at fire time), so a flow's entry
// contract holds however the run was started.

// MetaEntryKey is the process-row meta slot a launch's entry values are kept
// in. A scheduled row is dispatched later by the scheduler through a freshly
// built request, so the values have to survive on the record to reach the seed
// that happens at fire time — and keeping them there also records what a run
// was actually given, which is otherwise invisible once the context moves on.
const MetaEntryKey = "entry"

// EntryView is everything a launcher needs to put a flow's entry form on
// screen: the declared form, where its answers will land, and the values to
// start the fields at.
type EntryView struct {
	// Entry is the flow's declared form, or nil when it declares none.
	Entry *models.EntryForm `json:"entry"`
	// Target is the resolved write path (the entry's own, or `$._entry`).
	Target string `json:"target"`
	// Prefill is the resolved mode the values below were produced under.
	Prefill models.PrefillMode `json:"prefill"`
	// Values are the fields' starting values: the schema's own defaults, or —
	// in live mode — what already sits at the target in the context this run
	// would go against, with the defaults still filling what it says nothing
	// about.
	Values map[string]any `json:"values"`
	// Source says where Values came from: "default" (the schema alone),
	// "context" (the live context, defaults underneath), or "none" (the flow
	// declares no entry).
	Source string `json:"source"`
}

// EntryOf returns a flow's entry form, or nil when it declares none.
func EntryOf(flow models.FlowRecord) *models.EntryForm {
	return flow.Preferences.EntryOrNil()
}

// ResolveEntryView builds the EntryView for a flow, for the context a run would
// go against (contextID may be empty — then there is no live context to read and
// the schema defaults are all there is).
//
// It never fails on the context: a missing or unreadable context document means
// "nothing to prefill", not an error, because the form is still the right thing
// to show.
func ResolveEntryView(ctx context.Context, store repository.Store, flow models.FlowRecord, contextID string) EntryView {
	entry := EntryOf(flow)
	if entry == nil {
		return EntryView{Target: models.EntryTargetDefault, Prefill: models.PrefillDefault, Values: map[string]any{}, Source: "none"}
	}
	view := EntryView{
		Entry:   entry,
		Target:  entry.TargetPath(),
		Prefill: entry.PrefillOrDefault(),
		Values:  EntryDefaults(entry),
		Source:  "default",
	}
	if view.Prefill != models.PrefillLive || strings.TrimSpace(contextID) == "" {
		return view
	}
	held, ok := entryValuesInContext(ctx, store, contextID, view.Target)
	if !ok {
		return view
	}
	// The defaults stay underneath: a field the live context says nothing about
	// still shows what the flow would do on its own.
	for k, v := range held {
		view.Values[k] = v
	}
	view.Source = "context"
	return view
}

// PrepareEntryValues is the submit side: the values a launcher sends, with the
// entry's own defaults filled in underneath, validated against the flow's
// schema.
//
// Defaults are applied before validation, so a schema that declares a required
// field WITH a default is satisfied by a submit that leaves it out — which is
// what makes a flow's entry form safe for an unattended launch.
//
// A plain-values entry (see models.IsSchemaDocument) has no contract to check
// against: its document IS the default, and what comes back is taken as given.
func PrepareEntryValues(entry *models.EntryForm, values map[string]any) (map[string]any, error) {
	if !entry.HasForm() {
		return nil, nil
	}
	merged := EntryDefaults(entry)
	for k, v := range values {
		merged[k] = v
	}
	if !entry.IsSchema() {
		return merged, nil
	}
	if err := validateAgainstSchema(entry.Form.Schema, merged); err != nil {
		return nil, err
	}
	return merged, nil
}

// EntryDefaults is what "default mode" shows: for a JSON Schema entry the
// defaults the schema declares, and for a plain-values entry the document
// itself — the values the author wrote are exactly what they meant to seed.
func EntryDefaults(entry *models.EntryForm) map[string]any {
	if !entry.HasForm() {
		return map[string]any{}
	}
	if !entry.IsSchema() {
		out := make(map[string]any, len(entry.Form.Schema))
		for k, v := range entry.Form.Schema {
			out[k] = v
		}
		return out
	}
	return SchemaDefaults(entry.Form.Schema)
}

// SeedEntry writes a launch's entry values into the context document at the
// entry's target.
//
// It reads the document, sets that ONE location (creating only the objects the
// path names) and saves it back, so every other key — what another node wrote,
// what an earlier run left — survives untouched. A context id with no row yet
// is created holding just the seed: a run launched against an unwritten id is
// legal (the wire creates the document on first write), and that row is the one
// the run needs anyway.
func SeedEntry(ctx context.Context, store repository.Store, entry *models.EntryForm, contextID string, values map[string]any) error {
	if !entry.HasForm() || len(values) == 0 {
		return nil
	}
	id := strings.TrimSpace(contextID)
	if id == "" {
		return fmt.Errorf("entry seed: no context document to seed")
	}
	target := entry.TargetPath()

	rec, err := store.Contexts().GetByID(ctx, id)
	if err != nil {
		if !repository.IsNotFound(err) {
			return fmt.Errorf("entry seed: load context %s: %w", id, err)
		}
		rec = &models.ContextRecord{ID: id}
	}

	doc := map[string]any{}
	if body := strings.TrimSpace(rec.Context); body != "" && body != "null" {
		if err := json.Unmarshal([]byte(body), &doc); err != nil {
			// Refusing is the point: a document we cannot parse is a document we
			// cannot write one key of without throwing the rest away.
			return fmt.Errorf("entry seed: context %s is not a JSON object, leaving it unchanged: %w", id, err)
		}
	}
	if err := models.SetAtContextPath(doc, target, values); err != nil {
		return fmt.Errorf("entry seed: %w", err)
	}
	body, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("entry seed: marshal context %s: %w", id, err)
	}
	rec.Context = string(body)
	// A flow-driven change: the data belongs to the run that is starting, and
	// `entry` is the address a reader of the context row sees for it.
	rec.UpdatedBy = models.LastChange{By: models.ByFlow, Address: MetaEntryKey}
	if err := store.Contexts().Upsert(ctx, rec); err != nil {
		return fmt.Errorf("entry seed: save context %s: %w", id, err)
	}
	return nil
}

// entryValuesInContext reads the object a context document holds at a path.
// Anything that is not a readable object there reads as absent — the caller only
// wants to know whether there are values to show or to count as supplied.
func entryValuesInContext(ctx context.Context, store repository.Store, contextID, target string) (map[string]any, bool) {
	if strings.TrimSpace(contextID) == "" {
		return nil, false
	}
	rec, err := store.Contexts().GetByID(ctx, contextID)
	if err != nil {
		return nil, false
	}
	doc := map[string]any{}
	if body := strings.TrimSpace(rec.Context); body != "" {
		if json.Unmarshal([]byte(body), &doc) != nil {
			return nil, false
		}
	}
	held, ok := models.ValueAtContextPath(doc, target)
	if !ok {
		return nil, false
	}
	obj, ok := held.(map[string]any)
	return obj, ok
}

// SchemaDefaults collects the values a JSON Schema declares as its own
// defaults — the schema half of EntryDefaults: the form as the flow would run
// it if nobody typed anything.
//
// It walks `properties` one level at a time and recurses into object properties,
// so a nested shape gets its nested defaults. A property with no default
// contributes nothing (rather than a null), because an absent field and a field
// explicitly set to null are different inputs to a flow.
func SchemaDefaults(schema map[string]any) map[string]any {
	out := map[string]any{}
	props, _ := schema["properties"].(map[string]any)
	for name, raw := range props {
		prop, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if def, ok := prop["default"]; ok {
			out[name] = def
			continue
		}
		if schemaDeclaresObject(prop) {
			if nested := SchemaDefaults(prop); len(nested) > 0 {
				out[name] = nested
			}
		}
	}
	return out
}

// schemaDeclaresObject reports whether a property schema describes an object,
// tolerating a `type: ["object", "null"]` union and a schema that only has
// `properties`.
func schemaDeclaresObject(prop map[string]any) bool {
	switch t := prop["type"].(type) {
	case string:
		return t == "object"
	case []any:
		for _, v := range t {
			if s, _ := v.(string); s == "object" {
				return true
			}
		}
	case nil:
		_, has := prop["properties"]
		return has
	}
	return false
}

// validateAgainstSchema checks submitted values against the entry's JSON Schema
// and reports the failures in the schema library's own readable form (one line
// per violation, each naming the field).
//
// A schema the compiler rejects is the flow author's bug, not the submitter's,
// and is reported as such — the launch still fails, because seeding data the
// flow's own contract cannot vouch for is worse than refusing.
func validateAgainstSchema(schema map[string]any, values map[string]any) error {
	compiler := jsonschema.NewCompiler()
	// The schema is a document we already hold as a map; round-tripping it
	// through JSON is what gives the compiler the plain any-shaped tree it
	// expects (and rejects a schema carrying values that are not JSON at all).
	raw, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("the flow's entry schema is not valid JSON: %w", err)
	}
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
	if err != nil {
		return fmt.Errorf("the flow's entry schema is not valid JSON: %w", err)
	}
	// A URN, not a filename: the library resolves a bare name against the
	// working directory and then names that absolute path in every error it
	// reports — the submitter would be reading the server's filesystem layout.
	const url = "urn:flomorphic:flow-entry"
	if err := compiler.AddResource(url, doc); err != nil {
		return fmt.Errorf("the flow's entry schema cannot be used: %w", err)
	}
	sch, err := compiler.Compile(url)
	if err != nil {
		return fmt.Errorf("the flow's entry schema cannot be used: %w", err)
	}
	// The values go through the same round trip: the validator works on the
	// JSON types (float64 numbers, []any arrays), not on whatever a binder left
	// in the map.
	vraw, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("the entry values are not valid JSON: %w", err)
	}
	val, err := jsonschema.UnmarshalJSON(strings.NewReader(string(vraw)))
	if err != nil {
		return fmt.Errorf("the entry values are not valid JSON: %w", err)
	}
	if err := sch.Validate(val); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			return fmt.Errorf("the entry values do not match the flow's entry form: %s", formatValidationError(ve))
		}
		return fmt.Errorf("the entry values do not match the flow's entry form: %w", err)
	}
	return nil
}

// formatValidationError renders a validation failure as the one thing the
// submitter can act on: which field is wrong and why. The library's own text
// opens with a line naming the schema resource, which says nothing to whoever
// filled the form in, so it is dropped and the per-field lines are kept.
func formatValidationError(ve *jsonschema.ValidationError) string {
	lines := strings.Split(ve.Error(), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines[1:] {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if line == "" {
			continue
		}
		// The root location reads as `at ''`, which is noise on a form whose
		// fields are all at the top level.
		out = append(out, strings.TrimPrefix(line, "at '': "))
	}
	if len(out) == 0 {
		return ve.Error()
	}
	return strings.Join(out, "; ")
}

// seedScheduledEntry seeds a scheduled row's kept entry values (MetaEntryKey)
// into its context at fire time, resolving the flow's entry form again so the
// target is the one the flow declares *now*.
//
// A row whose flow has since dropped its entry form is launched without a seed
// rather than refused: the values were an input to a form that no longer
// exists, and the run itself is still valid.
func seedScheduledEntry(ctx context.Context, store repository.Store, rec *models.Process) error {
	values, ok := rec.Meta[MetaEntryKey].(map[string]any)
	if !ok || len(values) == 0 {
		return nil
	}
	flow, err := store.Workflows().GetByID(ctx, rec.FlowID)
	if err != nil {
		return fmt.Errorf("entry seed: load flow %s: %w", rec.FlowID, err)
	}
	entry := EntryOf(*flow)
	if entry == nil {
		return nil
	}
	return SeedEntry(ctx, store, entry, rec.ContextID, values)
}
