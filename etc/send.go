// Package etc holds small cross-cutting HTTP helpers shared by the controllers.
package etc

import (
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/gofiber/fiber/v3"
)

// Send writes the standard `{ data, error }` envelope with the given status.
func Send(c fiber.Ctx, code int, data any, err any) error {
	return c.Status(code).JSON(models.Response{Data: data, Error: err})
}

// OK is Send with 200 and no error.
func OK(c fiber.Ctx, data any) error {
	return Send(c, fiber.StatusOK, data, nil)
}

// Fail writes an ErrorResponse envelope with the given status and message.
func Fail(c fiber.Ctx, code int, message string) error {
	return Send(c, code, nil, models.ErrorResponse{Code: code, Message: message})
}

// FailFromRepo maps a repository error to an HTTP response: ErrNotFound → 404,
// anything else → 500. The 404 message is caller-supplied so it can name the
// entity ("workflow not found").
func FailFromRepo(c fiber.Ctx, err error, notFoundMsg string) error {
	if repository.IsNotFound(err) {
		return Fail(c, fiber.StatusNotFound, notFoundMsg)
	}
	return Fail(c, fiber.StatusInternalServerError, err.Error())
}
