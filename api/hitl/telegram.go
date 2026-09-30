package hitlControllers

import (
	"strings"

	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/hitl"
	"github.com/FloMorphic/morph-api/models"
	"github.com/gofiber/fiber/v3"
)

// Design-time support for the Human-in-the-Loop node's Telegram channel: who a
// bot can be asked to talk to, and which bot that is.
//
// These are not part of a task's lifecycle — they are what the node's settings
// editor needs to offer a recipient to PICK rather than a numeric chat id to
// paste. They live here rather than under /connect because the directory they read
// is written by the HITL bridge and means nothing outside a HITL session.
//
// The reason a directory exists at all: the Telegram Bot API cannot list a bot's
// users. A bot only learns a chat exists when someone interacts with it, and that
// arrives once, on an update stream that is consumed and expires. So the bridge
// writes every chat it hears from into a durable table, and `discover` sweeps
// whatever is still pending into the same place. See models.TelegramRecipient.

// telegramScope is the bot a request is about: a Connect connection and an alias,
// both optional (empty ⇒ the default connection / the gateway's default bot),
// exactly as a node's binding carries them.
type telegramScope struct {
	Connection string `json:"connection"`
	Alias      string `json:"alias"`
}

func (s telegramScope) binding() *models.TelegramBinding {
	return &models.TelegramBinding{
		Connection: strings.TrimSpace(s.Connection),
		Alias:      strings.TrimSpace(s.Alias),
	}
}

// scopeFromQuery reads the bot scope off the query string, for the GET routes.
func scopeFromQuery(c fiber.Ctx) telegramScope {
	return telegramScope{
		Connection: strings.TrimSpace(c.Query("connection")),
		Alias:      strings.TrimSpace(c.Query("alias")),
	}
}

// listRecipients handles GET /hitl/telegram/recipients — the chats this bot is
// known to be able to reach, most recently heard from first.
//
// It touches no gateway: it is the accumulated record, so it answers instantly and
// still answers when OpenConnector is unreachable. That is deliberate — a designer
// editing a node should not depend on a live third party to see a list.
func (ctl *controller) listRecipients(c fiber.Ctx) error {
	scope := scopeFromQuery(c)
	list, err := ctl.store.TelegramRecipients().List(c.Context(), scope.Connection, scope.Alias, 0)
	if err != nil {
		return etc.FailFromRepo(c, err, "telegram recipients not found")
	}
	return etc.OK(c, list)
}

// discoverRecipients handles POST /hitl/telegram/recipients/discover — sweep the
// bot's pending updates into the directory and return it, along with the bot's own
// identity so the UI can tell the operator which bot to have people message.
//
// The sweep acknowledges nothing (see hitl.TelegramDiscoverRecipients), so it is
// safe to run while the bridge is holding live sessions on the same bot. It also
// means it can only see what has not been consumed yet: finding nothing new is an
// ordinary outcome, and the directory returned is still the full answer.
func (ctl *controller) discoverRecipients(c fiber.Ctx) error {
	var in telegramScope
	// A body is optional — the default bot on the default connection needs none.
	_ = c.Bind().Body(&in)
	in.Connection = strings.TrimSpace(in.Connection)
	in.Alias = strings.TrimSpace(in.Alias)

	// The bot profile first: it is the one Telegram call that needs no chat and no
	// prior interaction, so when the binding is broken this is what says so plainly
	// instead of an empty list the operator would read as "nobody has messaged it".
	bot, err := hitl.TelegramGetMe(c.Context(), ctl.store, in.binding())
	if err != nil {
		return etc.Fail(c, fiber.StatusBadGateway, err.Error())
	}

	// A webhook makes polling return nothing, for ever, with no error. Without this
	// check an empty sweep reads as "nobody has messaged the bot" and sends the
	// operator to ask people to message it again — which cannot possibly help.
	webhook, err := hitl.TelegramWebhook(c.Context(), ctl.store, in.binding())
	if err != nil {
		return etc.Fail(c, fiber.StatusBadGateway, err.Error())
	}

	list, err := hitl.TelegramDiscoverRecipients(c.Context(), ctl.store, in.Connection, in.Alias)
	if err != nil {
		return etc.Fail(c, fiber.StatusBadGateway, err.Error())
	}
	return etc.OK(c, fiber.Map{"bot": bot, "recipients": list, "webhook": webhook})
}

// deleteRecipient handles DELETE /hitl/telegram/recipients/:id — forget one chat.
// The directory only ever grows on its own (a chat is recorded the first time it is
// heard from and never expires), so removing a stale entry has to be possible or a
// picker slowly fills with people who left.
func (ctl *controller) deleteRecipient(c fiber.Ctx) error {
	id := c.Params("id")
	if err := ctl.store.TelegramRecipients().Delete(c.Context(), id); err != nil {
		return etc.FailFromRepo(c, err, "telegram recipient not found")
	}
	return etc.Send(c, fiber.StatusAccepted, fiber.Map{"id": id}, nil)
}
