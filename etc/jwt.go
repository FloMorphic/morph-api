package etc

import (
	"github.com/FloMorphic/morph-api/env"
	jwtware "github.com/gofiber/contrib/v3/jwt"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/extractors"
)

// HS256SecKeyHandler guards a route group with an HS256 bearer token, verified
// against the configured API secret. Wired only when AUTH_ENABLED is set.
func HS256SecKeyHandler() fiber.Handler {
	return jwtware.New(jwtware.Config{
		SigningKey: jwtware.SigningKey{Key: []byte(env.GetJwtSecret())},
	})
}

// HS256SocketKeyHandler is HS256SecKeyHandler for the WebSocket upgrade, which
// cannot carry an Authorization header: the browser WebSocket API exposes no way
// to set request headers on the handshake, so clients pass the token as a query
// parameter instead.
//
// The chain tries the header first and falls back to `?Authorization=<token>`,
// so a non-browser client that *can* set headers keeps the token out of the URL.
// The query form is bare, with no "Bearer " prefix.
//
// A token in a URL is visible to access logs and proxies in a way a header is
// not; that is the cost of gating a browser WebSocket at all, and it is the
// reason to terminate TLS in front of this in any deployment that turns auth on.
func HS256SocketKeyHandler() fiber.Handler {
	return jwtware.New(jwtware.Config{
		SigningKey: jwtware.SigningKey{Key: []byte(env.GetJwtSecret())},
		Extractor: extractors.Chain(
			extractors.FromAuthHeader("Bearer"),
			extractors.FromQuery("Authorization"),
		),
	})
}
