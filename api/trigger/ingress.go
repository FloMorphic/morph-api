package triggerControllers

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"hash"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/inflow"
	"github.com/FloMorphic/morph-api/models"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gofiber/fiber/v3"
)

// ingress handles ALL /hooks/:slug — the public webhook endpoint. It resolves an
// enabled webhook by slug, enforces its method allow-list and per-hook auth, then
// launches the bound flow with the request payload as the run's initial context.
// Every outcome (accepted or rejected) is recorded on the hook's delivery log.
func (ctl *controller) ingress(c fiber.Ctx) error {
	hook, err := ctl.store.Triggers().GetBySlug(c.Context(), c.Params("slug"))
	if err != nil || hook.Kind != models.TriggerWebhook {
		return etc.Fail(c, fiber.StatusNotFound, "webhook not found")
	}

	method := c.Method()
	// CORS preflight: acknowledge without auth or launch.
	if method == fiber.MethodOptions {
		return c.SendStatus(fiber.StatusOK)
	}

	if len(hook.Methods) > 0 && !containsFold(hook.Methods, method) {
		ctl.recordHit(c, hook, fiber.StatusMethodNotAllowed, "method not allowed")
		return etc.Fail(c, fiber.StatusMethodNotAllowed, "method not allowed")
	}

	if status, msg := authorize(c, hook); status != fiber.StatusOK {
		ctl.recordHit(c, hook, status, msg)
		return etc.Fail(c, status, msg)
	}

	proc, err := inflow.LaunchTrigger(c.Context(), ctl.store, hook, "webhook: "+hook.Title, ctl.payload(c, hook, method))
	if err != nil {
		ctl.recordHit(c, hook, fiber.StatusBadGateway, "launch failed: "+err.Error())
		return etc.Fail(c, fiber.StatusBadGateway, err.Error())
	}
	ctl.recordHit(c, hook, fiber.StatusAccepted, "OK")

	indexID := int64(0)
	if proc != nil {
		indexID = proc.IndexID
	}
	return etc.Send(c, fiber.StatusAccepted, fiber.Map{"accepted": true, "indexId": indexID}, nil)
}

// authorize runs the hook's IP allow-list and credential check. It returns
// (200, "") when the request may proceed, or an HTTP status + reason otherwise.
// The methods mirror the reference gateway: `none` (IP allow-list only), a static
// token, HTTP Basic, a verified JWT, or an HMAC body signature.
func authorize(c fiber.Ctx, hook *models.Trigger) (int, string) {
	auth := hook.Auth
	if auth == nil {
		auth = &models.WebhookAuth{Method: models.AuthNone}
	}

	// IP allow-list: mandatory for `none` (there is no credential to prove
	// identity), optional otherwise — but enforced when present.
	if auth.Method == models.AuthNone || len(hook.WhitelistIP) > 0 {
		if len(hook.WhitelistIP) == 0 {
			return fiber.StatusUnauthorized, "no ip allow-list configured"
		}
		if !ipAllowed(c.IP(), hook.WhitelistIP) {
			return fiber.StatusForbidden, "ip not in allow-list"
		}
	}
	if auth.Method == models.AuthNone {
		return fiber.StatusOK, ""
	}

	headerVal := c.Get(auth.HeaderKey)
	if headerVal == "" {
		return fiber.StatusUnauthorized, "missing auth header"
	}

	switch auth.Method {
	case models.AuthStatic:
		if subtleEqual(headerVal, auth.Secret) {
			return fiber.StatusOK, ""
		}
		return fiber.StatusUnauthorized, "invalid token"

	case models.AuthBasic:
		user, pass, ok := parseBasic(headerVal)
		if ok && subtleEqual(user+":"+pass, auth.Secret) {
			return fiber.StatusOK, ""
		}
		return fiber.StatusUnauthorized, "invalid credentials"

	case models.AuthJWT:
		token := extractToken(headerVal, patternOr(auth.HeaderPattern, `^Bearer (.+)$`))
		if token == "" {
			return fiber.StatusUnauthorized, "no bearer token"
		}
		if err := verifyJWT(token, auth.Secret); err != nil {
			return fiber.StatusUnauthorized, "invalid jwt"
		}
		return fiber.StatusOK, ""

	case models.AuthHMAC:
		token := extractToken(headerVal, auth.HeaderPattern)
		if token == "" {
			return fiber.StatusUnauthorized, "no signature"
		}
		if err := verifyHMAC(c.Body(), auth.Secret, token, auth.HashAlgo, auth.Digest); err != nil {
			return fiber.StatusUnauthorized, "signature mismatch"
		}
		return fiber.StatusOK, ""
	}
	return fiber.StatusUnauthorized, "unsupported auth method"
}

// payload assembles the run's initial context from the request: the parsed JSON
// body (or a raw fallback), headers, query, client ip, method, and a small
// webhook descriptor — the shape downstream nodes read.
func (ctl *controller) payload(c fiber.Ctx, hook *models.Trigger, method string) map[string]any {
	body := map[string]any{}
	if err := c.Bind().Body(&body); err != nil {
		body = map[string]any{"raw": string(c.Body())}
	}
	return map[string]any{
		"body":   body,
		"header": c.GetReqHeaders(),
		"query":  c.Queries(),
		"ip":     c.IP(),
		"method": method,
		"webhook": map[string]any{
			"id":    hook.ID,
			"slug":  hook.Slug,
			"title": hook.Title,
		},
	}
}

// recordHit appends one delivery to the hook's bounded log (best effort).
func (ctl *controller) recordHit(c fiber.Ctx, hook *models.Trigger, status int, msg string) {
	_ = ctl.store.Triggers().RecordHit(c.Context(), hook.ID, models.TriggerHit{
		At:      time.Now().UnixMilli(),
		Status:  status,
		IP:      c.IP(),
		Method:  c.Method(),
		Message: msg,
	})
}

// ---- auth helpers ----------------------------------------------------------

func ipAllowed(ip string, cidrs []string) bool {
	addr := net.ParseIP(ip)
	if addr == nil {
		return false
	}
	for _, entry := range cidrs {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			if bare := net.ParseIP(entry); bare != nil && bare.Equal(addr) {
				return true
			}
			continue
		}
		if _, network, err := net.ParseCIDR(entry); err == nil && network.Contains(addr) {
			return true
		}
	}
	return false
}

// extractToken pulls the credential from a header value. With no pattern the
// whole value is the token; with one, the last capture group is returned.
func extractToken(headerVal, pattern string) string {
	if strings.TrimSpace(pattern) == "" {
		return strings.TrimSpace(headerVal)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return ""
	}
	m := re.FindStringSubmatch(headerVal)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1]
}

func patternOr(pattern, fallback string) string {
	if strings.TrimSpace(pattern) == "" {
		return fallback
	}
	return pattern
}

func parseBasic(headerVal string) (user, pass string, ok bool) {
	token := extractToken(headerVal, `^Basic\s+(.+)$`)
	if token == "" {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return "", "", false
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func verifyJWT(tokenStr, secret string) error {
	_, err := jwt.Parse(tokenStr, func(tok *jwt.Token) (interface{}, error) {
		switch tok.Method.(type) {
		case *jwt.SigningMethodHMAC:
			return []byte(secret), nil
		case *jwt.SigningMethodRSA, *jwt.SigningMethodRSAPSS:
			return jwt.ParseRSAPublicKeyFromPEM([]byte(secret))
		case *jwt.SigningMethodECDSA:
			return jwt.ParseECPublicKeyFromPEM([]byte(secret))
		case *jwt.SigningMethodEd25519:
			return jwt.ParseEdPublicKeyFromPEM([]byte(secret))
		}
		return nil, errors.New("unsupported signing method")
	})
	return err
}

func verifyHMAC(body []byte, secret, token, algo, digest string) error {
	var newHash func() hash.Hash
	switch strings.ToLower(algo) {
	case "sha512":
		newHash = sha512.New
	case "sha384":
		newHash = sha512.New384
	default:
		newHash = sha256.New
	}
	mac := hmac.New(newHash, []byte(secret))
	mac.Write(body)
	expected := mac.Sum(nil)

	var got []byte
	var err error
	switch strings.ToLower(digest) {
	case "base64":
		got, err = base64.StdEncoding.DecodeString(token)
	default:
		got, err = hex.DecodeString(token)
	}
	if err != nil {
		return errors.New("bad signature encoding")
	}
	if !hmac.Equal(got, expected) {
		return errors.New("signature mismatch")
	}
	return nil
}

// subtleEqual is a constant-time string compare for credential checks.
func subtleEqual(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}

func containsFold(list []string, v string) bool {
	for _, item := range list {
		if strings.EqualFold(item, v) {
			return true
		}
	}
	return false
}
