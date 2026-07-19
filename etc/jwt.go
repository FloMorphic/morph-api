package etc

import (
	"github.com/FloMorphic/morph-api/env"
	jwtware "github.com/gofiber/contrib/v3/jwt"
	"github.com/gofiber/fiber/v3"
)

// HS256SecKeyHandler guards a route group with an HS256 bearer token, verified
// against the configured API secret. Wired only when AUTH_ENABLED is set.
func HS256SecKeyHandler() fiber.Handler {
	return jwtware.New(jwtware.Config{
		SigningKey: jwtware.SigningKey{Key: []byte(env.GetJwtSecret())},
	})
}
