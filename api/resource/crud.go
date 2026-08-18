package resourceControllers

import (
	"strings"

	"github.com/FloMorphic/morph-api/etc"
	fuse "github.com/Inflowenger/inflow-fusion/inflow"
	"github.com/gofiber/fiber/v3"
)

// reloadLimit caps how many registered engine instances a reload pulls from
// infra — matches the SDK's own startup reload.
const reloadLimit = 100

type controller struct{}

// resourceDTO is the safe view of one dispatch candidate sent to the web app.
// The resource's Token is intentionally omitted — it is a bearer secret the SDK
// keeps for dispatch and the settings dialog never needs.
type resourceDTO struct {
	Name   string   `json:"name"`
	Url    string   `json:"url"`
	Tags   []string `json:"tags"`
	Pinned bool     `json:"pinned"`
}

// list handles GET /resource — the live dispatch pool, each candidate flagged
// with whether it is the one currently pinned. `pinned` (top level) is the name
// of that resource, or empty when dispatch is round-robin across the whole pool.
func (ctl *controller) list(c fiber.Ctx) error {
	return etc.OK(c, buildPoolView())
}

// addInput is the body of POST /resource — add one engine instance by hand.
// Url is required; Token is the resource's bearer secret (blank falls back to
// the infra bearer, matching how the SDK dispatches to secret-less portals). Pin
// is the dialog's "use just this one" checkbox: when set, the resource is tagged
// so every dispatch goes only to it.
type addInput struct {
	Name  string   `json:"name"`
	Url   string   `json:"url"`
	Token string   `json:"token"`
	Tags  []string `json:"tags"`
	Pin   bool     `json:"pin"`
}

// add handles POST /resource. The SDK liveness-probes the resource before it
// enters the pool, so a probe failure is reported back as a 502 (bad gateway):
// the resource is real input, just unreachable from here right now.
func (ctl *controller) add(c fiber.Ctx) error {
	var in addInput
	if err := c.Bind().Body(&in); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid resource payload")
	}
	in.Url = strings.TrimSpace(in.Url)
	if in.Url == "" {
		return etc.Fail(c, fiber.StatusBadRequest, "url is required")
	}
	tags := in.Tags
	if in.Pin && !hasTag(tags, fuse.PinResourceTag) {
		tags = append(tags, fuse.PinResourceTag)
	}
	err := fuse.AddResource(fuse.InflowResource{
		Name:  strings.TrimSpace(in.Name),
		Url:   in.Url,
		Token: in.Token,
		Tags:  tags,
	})
	if err != nil {
		return etc.Fail(c, fiber.StatusBadGateway, err.Error())
	}
	return etc.OK(c, buildPoolView())
}

// pinInput is the body of POST /resource/pin — the resource to pin, by name or
// url (either identifier a list row carries).
type pinInput struct {
	Resource string `json:"resource"`
}

// pin handles POST /resource/pin — force all dispatch onto one pooled resource.
func (ctl *controller) pin(c fiber.Ctx) error {
	var in pinInput
	if err := c.Bind().Body(&in); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid pin payload")
	}
	in.Resource = strings.TrimSpace(in.Resource)
	if in.Resource == "" {
		return etc.Fail(c, fiber.StatusBadRequest, "resource is required")
	}
	if !fuse.PinResource(in.Resource) {
		return etc.Fail(c, fiber.StatusNotFound, "resource not found in the dispatch pool")
	}
	return etc.OK(c, buildPoolView())
}

// unpin handles POST /resource/unpin — release the pin, back to round-robin.
func (ctl *controller) unpin(c fiber.Ctx) error {
	fuse.UnpinResource()
	return etc.OK(c, buildPoolView())
}

// reload handles POST /resource/reload — re-read the pool from infra. This drops
// any hand-added resources and re-derives the pin from the reloaded tags, exactly
// like the startup reload.
func (ctl *controller) reload(c fiber.Ctx) error {
	backend := fuse.GetInflowBackend()
	if backend == nil {
		return etc.Fail(c, fiber.StatusServiceUnavailable, "inflow runtime is not connected")
	}
	if _, err := backend.ReloadResources(reloadLimit); err != nil {
		return etc.Fail(c, fiber.StatusBadGateway, err.Error())
	}
	return etc.OK(c, buildPoolView())
}

// buildPoolView renders the current SDK dispatch pool into the response shape the
// web app consumes: the candidate list plus the pinned resource's name.
func buildPoolView() fiber.Map {
	pinned := fuse.GetPinnedResource()
	all := fuse.GetResourceCandidList()
	list := make([]resourceDTO, 0, len(all))
	for _, r := range all {
		list = append(list, resourceDTO{
			Name:   r.Name,
			Url:    r.Url,
			Tags:   r.Tags,
			Pinned: pinned != nil && pinned.Url == r.Url,
		})
	}
	pinnedName := ""
	if pinned != nil {
		pinnedName = pinned.Name
	}
	return fiber.Map{"list": list, "pinned": pinnedName}
}

// hasTag reports whether tags already contains want.
func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}
