package models

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
)

// TelegramRecipient is someone a Telegram Human-in-the-Loop session can be sent
// to: one chat a bot is known to be able to reach.
//
// It exists because the Telegram Bot API has NO way to list a bot's users. A bot
// only learns a chat exists when someone interacts with it, and Telegram keeps
// undelivered updates for about 24 hours — after which, or after anything has
// consumed them, that knowledge is simply gone. So there is nothing to enumerate
// at the moment a designer is picking a recipient, and a node would be left
// asking them to paste a numeric chat id from somewhere.
//
// This is that missing directory, and it is durable where the update stream is
// not: every chat the HITL bridge sees is recorded here and stays recorded, so a
// person who has messaged the bot once is pickable forever. The Connect page's
// discovery action fills it the same way from whatever is still pending on the
// stream.
//
// Scope is per (Connection, Alias): a chat id is only meaningful to the bot that
// saw it, and two bots on two gateways know different people.
type TelegramRecipient struct {
	// ID is derived from the scope + chat id (see TelegramRecipientID) so seeing
	// the same chat again updates the row instead of adding another.
	ID string `json:"id"`
	// Connection is the Connect connection whose gateway holds the bot ('' for the
	// default connection), and Alias the connected bot ('' for its default account)
	// — the same pair a node's binding carries.
	Connection string `json:"connection"`
	Alias      string `json:"alias"`
	// ChatID is what a node's binding stores and what Telegram is called with.
	ChatID string `json:"chatId"`
	// Type is Telegram's chat type: private / group / supergroup / channel. It is
	// what tells a designer whether they are about to address a person or a room.
	Type string `json:"type"`
	// The naming fields Telegram populates differently per chat kind: Title for a
	// group or channel, First/LastName for a private chat, Username when public.
	Title     string `json:"title,omitempty"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"firstName,omitempty"`
	LastName  string `json:"lastName,omitempty"`
	// LastSeenAt is when the bot last heard from this chat — the useful sort order
	// for a picker, and the honest answer to "is this still someone we can reach?"
	LastSeenAt int64 `json:"lastSeenAt"`
	CreatedAt  int64 `json:"createdAt"`
	UpdatedAt  int64 `json:"updatedAt"`
}

// Label renders a recipient for a picker, falling back through the fields
// Telegram populates for the different chat kinds and ending at the bare id — so
// there is always something to show.
func (r TelegramRecipient) Label() string {
	if name := strings.TrimSpace(r.Title); name != "" {
		return name
	}
	if name := strings.TrimSpace(r.FirstName + " " + r.LastName); name != "" {
		return name
	}
	if r.Username != "" {
		return "@" + r.Username
	}
	return r.ChatID
}

// TelegramRecipientID derives the stable row id for a chat within one bot's scope.
// Hashed rather than concatenated because a connection id, an alias and a chat id
// are all free-form and would otherwise need escaping to stay unambiguous.
func TelegramRecipientID(connection, alias, chatID string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(connection) + "\x00" + strings.TrimSpace(alias) + "\x00" + strings.TrimSpace(chatID)))
	return "tgr_" + hex.EncodeToString(sum[:])
}
