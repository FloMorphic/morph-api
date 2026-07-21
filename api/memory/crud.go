package memoryControllers

import (
	"strings"

	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/models"
	"github.com/gofiber/fiber/v3"
)

// list handles GET /memory — returns every store as a plain array (the web app
// treats memory as a small, un-paginated collection).
func (ctl *controller) list(c fiber.Ctx) error {
	items, err := ctl.repo.List(c.Context())
	if err != nil {
		return etc.FailFromRepo(c, err, "memory stores not found")
	}
	return etc.OK(c, items)
}

// create handles POST /memory. Validates the store shape before persisting; the
// repository provisions a sqlite-vec index for vector stores.
func (ctl *controller) create(c fiber.Ctx) error {
	var input models.MemoryStore
	if err := c.Bind().Body(&input); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid memory store payload")
	}
	// A create never trusts a client id; the repository assigns one.
	input.ID = ""
	if msg := validateMemory(&input); msg != "" {
		return etc.Fail(c, fiber.StatusBadRequest, msg)
	}
	if err := ctl.repo.Create(c.Context(), &input); err != nil {
		return etc.FailFromRepo(c, err, "memory store not found")
	}
	return etc.OK(c, input)
}

// getByID handles GET /memory/:id.
func (ctl *controller) getByID(c fiber.Ctx) error {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "memory store not found")
	}
	return etc.OK(c, rec)
}

// deleteByID handles DELETE /memory/:id.
func (ctl *controller) deleteByID(c fiber.Ctx) error {
	id := c.Params("id")
	if err := ctl.repo.Delete(c.Context(), id); err != nil {
		return etc.FailFromRepo(c, err, "memory store not found")
	}
	return etc.Send(c, fiber.StatusAccepted, fiber.Map{"id": id}, nil)
}

// validateMemory checks a create payload and returns a human message on the
// first problem, or "" when valid. Keeps the config matching the declared type.
func validateMemory(m *models.MemoryStore) string {
	if strings.TrimSpace(m.Name) == "" {
		return "name is required"
	}
	switch m.Type {
	case models.MemoryVector:
		m.Document = nil
		if m.Vector == nil {
			return "vector config is required for a vector store"
		}
		// A vector store captures its embedding provider/model/size once, here:
		// the vector svc handler reuses them for every index and search, so all
		// three must be present up front. (The token is needed to call the
		// provider but may be injected via other means, so it is not required at
		// create time — the handler surfaces a clear error if it is still absent.)
		if strings.TrimSpace(m.Vector.Provider) == "" {
			return "vector.provider is required (the embedding LLM provider, e.g. \"openai\", or a full base URL)"
		}
		if strings.TrimSpace(m.Vector.EmbeddingModel) == "" {
			return "vector.embeddingModel is required"
		}
		if m.Vector.Dimensions <= 0 {
			return "vector.dimensions must be greater than zero"
		}
	case models.MemoryDocument:
		m.Vector = nil
		if m.Document == nil {
			return "document config is required for a document store"
		}
		if strings.TrimSpace(m.Document.Table) == "" {
			return "document.table is required"
		}
		// The table name is interpolated into DDL/DML (it cannot be a bound
		// parameter), so it must be a plain identifier — reject anything else
		// up front rather than at provisioning time.
		if !models.IsSafeIdentifier(m.Document.Table) {
			return "document.table must be a plain identifier (letters, digits, underscore; not starting with a digit)"
		}
		if len(m.Document.Columns) == 0 {
			return "document.columns must have at least one column"
		}
	default:
		return "type must be 'vector' or 'document'"
	}
	return ""
}
