package extensionControllers

import (
	"encoding/json"
	"strings"

	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/models"
	InfraSpaces "github.com/Inflowenger/inflow-fusion/spaces"
	"github.com/gofiber/fiber/v3"
)

// Turning a running plugin into palette nodes.
//
// A plugin describes itself over inflowv1: `@intro` says who it is and what
// settings it needs before any action runs, `@actions` lists the methods it
// exposes — each with a title, an icon and the form its parameters are collected
// through. Neither is stored: the plugin is the only authority on what it can
// do, and it can be redeployed with a method added or dropped at any time.
//
// Sync is the one place that copies any of it into the database, and it does so
// as a *replacement*: every derived row for the plugin is deleted, then one row
// per live action is written. That is what makes a removed method disappear from
// the palette instead of lingering as a node that no longer resolves. The
// plugin's own registration row is never touched — it is the user's, not the
// plugin's.
//
// The settings form from `@intro` is deliberately NOT stored. It is the shape of
// a settings profile, and profiles live under `/settings` keyed by plugin id;
// the web app reads the form live and writes a profile from it.

// syncActions handles POST /extension/id/:id/sync — re-read the plugin's live
// descriptors and rebuild its palette rows from them.
func (ctl *controller) syncActions(c fiber.Ctx) error {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "extension not found")
	}
	pluginID := strings.TrimSpace(rec.PluginID)
	if pluginID == "" {
		return etc.Fail(c, fiber.StatusBadRequest, "extension has no plugin id (not an inflowv1 plugin)")
	}
	if rec.Action != "" {
		return etc.Fail(c, fiber.StatusBadRequest, "this row is one plugin action — sync the plugin it belongs to")
	}

	// The action list is what a sync is for, and it comes from the live plugin.
	// A plugin that is not running does not answer, and that is a 502 rather
	// than an empty sync: wiping the palette because a process happens to be
	// down would be the wrong move.
	actionsRaw, err := InfraSpaces.DefaultPluginActions(pluginID)
	if err != nil {
		return etc.Fail(c, fiber.StatusBadGateway, "plugin did not answer @actions: "+err.Error())
	}
	var actions []models.PluginAction
	if err := json.Unmarshal(actionsRaw, &actions); err != nil {
		return etc.Fail(c, fiber.StatusBadGateway, "plugin sent an unreadable @actions: "+err.Error())
	}

	// `@intro` is a bonus, not a requirement: it only labels the result with the
	// plugin's name and version. It is fetched best-effort because a plugin may
	// legitimately not serve it — the SDK through v0.1.3 never answers it at all
	// (its handler marshals the Intro *method* rather than the intro field), so
	// insisting on it would make sync unusable against every plugin built on
	// those versions.
	var intro models.PluginIntro
	if introRaw, err := InfraSpaces.DefaultPluginIntro(pluginID); err == nil {
		_ = json.Unmarshal(introRaw, &intro)
	}

	removed, err := ctl.repo.DeletePluginActions(c.Context(), pluginID)
	if err != nil {
		return etc.FailFromRepo(c, err, "extension not found")
	}

	added := 0
	for _, act := range actions {
		method := strings.TrimSpace(act.Method)
		if method == "" {
			continue // a nameless action has no subject to call; skip it
		}
		row := actionRow(rec, act, method)
		if err := ctl.repo.Upsert(c.Context(), &row); err != nil {
			return etc.FailFromRepo(c, err, "extension not found")
		}
		added++
	}

	return etc.OK(c, models.SyncResult{
		Intro:    intro,
		Actions:  actions,
		Added:    added,
		Removed:  removed,
		PluginID: pluginID,
	})
}

// actionRow builds the palette row for one action of a plugin. It carries the
// plugin's identity (so the node compiles against the right plugin and shares
// its settings profile) and the action's own form, which is what the node's
// drawer renders — a node dropped from the palette is then self-contained,
// with no second round trip to the plugin to draw its fields.
func actionRow(parent *models.ExtensionRecord, act models.PluginAction, method string) models.ExtensionRecord {
	title := strings.TrimSpace(act.Title)
	if title == "" {
		title = method
	}
	return models.ExtensionRecord{
		Kind:        models.KindExtension,
		Type:        models.ExtPluginBaseType,
		Name:        title,
		Description: act.Description,
		PluginID:    parent.PluginID,
		Action:      method,
		ParentID:    parent.ID,
		Icon:        actionIcon(parent, act),
		Parameters:  formParameters(act.Form),
		Outbound:    act.Outbound,
	}
}

// actionIcon prefers the icon the action names, and falls back to the icon the
// user picked for the plugin so a palette of actions still reads as one family.
func actionIcon(parent *models.ExtensionRecord, act models.PluginAction) models.Icon {
	if name := strings.TrimSpace(act.Icon.Icon); name != "" {
		return models.Icon{Class: strings.TrimSpace(act.Icon.Ref), Name: name}
	}
	return parent.Icon
}

// formParameters converts the SDK's form descriptor — JSON documents carried as
// *strings* — into the record's parsed schema/ui objects. A form that is absent
// or unparseable yields empty objects rather than an error: an action with no
// parameters is perfectly normal, and one plugin's malformed form must not fail
// the whole sync.
func formParameters(form models.FormBuilder) models.FormParameters {
	return models.FormParameters{
		Schema: decodeFormDoc(form.Jsonschema),
		UI:     decodeFormDoc(form.Jsonui),
	}
}

func decodeFormDoc(doc string) map[string]any {
	out := map[string]any{}
	if s := strings.TrimSpace(doc); s != "" {
		_ = json.Unmarshal([]byte(s), &out)
	}
	return out
}
