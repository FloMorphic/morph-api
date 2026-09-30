package hitl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/openconnector"
	"github.com/FloMorphic/morph-api/repository"
)

// The Telegram side of a HITL session.
//
// FloMorphic never holds a bot token. The bot is an account connected in
// OpenConnector (oomol's hosted gateway or a self-hosted one), and every call
// here is an OpenConnector ACTION run as that account through the Connect
// connection the node named — the same path the telegram-oc plugin takes, except
// in-process: this is the backend that owns the credential, so it calls the
// gateway client directly instead of going back out over the NATS proxy.
//
// Only three of the gateway's telegram actions are used. The session needs to
// talk (send_message), to listen (get_updates), and to know where the update
// stream currently is so a new session does not answer messages that predate it
// (get_updates with a negative offset).

// TelegramService is the OpenConnector service id for Telegram bots — the
// `service` field GET /v1/connections reports, and the prefix on every action id
// (`telegram.send_message`).
const TelegramService = "telegram"

// telegramMaxText is Telegram's per-message text limit. A facilitator turn can
// run past it (it may be quoting the brief), so outbound text is split rather
// than rejected by the API.
const telegramMaxText = 4096

// TelegramUpdate is one entry of the bot's update stream, narrowed to what a
// session needs: who said what, in which chat. The gateway's normalized shape
// carries a great deal more (media, polls, forwards); a HITL conversation is
// text, so everything else is ignored and a non-text update is skipped.
type TelegramUpdate struct {
	UpdateID int64            `json:"updateId"`
	Message  *TelegramMessage `json:"message"`
}

// TelegramMessage is one message inside an update.
type TelegramMessage struct {
	MessageID int64         `json:"messageId"`
	Date      int64         `json:"date"`
	Text      string        `json:"text"`
	Caption   string        `json:"caption"`
	Chat      TelegramChat  `json:"chat"`
	From      *TelegramUser `json:"from"`
}

// TelegramChat identifies where a message was sent. Every naming field Telegram
// populates is carried, because they are how a recipient is LABELLED in the picker
// and Telegram fills different ones per chat kind: Title for a group or channel,
// First/LastName for a private chat, Username when it is public. `Username` also
// matters for routing — a node may bind its chat as `@username`.
type TelegramChat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Username  string `json:"username"`
	Title     string `json:"title"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
}

// TelegramUser is who sent a message. The session does not route on it — a chat is
// the session — but it is kept so a transcript can say who spoke.
type TelegramUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"isBot"`
	FirstName string `json:"firstName"`
	Username  string `json:"username"`
}

// Body returns the human-readable text of an update — the message text, or a
// media caption when the person attached something and wrote under it. Empty
// means there is nothing for the facilitator to read.
func (u TelegramUpdate) Body() string {
	if u.Message == nil {
		return ""
	}
	if t := strings.TrimSpace(u.Message.Text); t != "" {
		return t
	}
	return strings.TrimSpace(u.Message.Caption)
}

// ChatKey renders the update's chat id the way a task's binding stores it, so
// routing is a string compare against HumanTaskTelegram.ChatID. A chat bound by
// `@username` also matches on the username the update carries.
func (u TelegramUpdate) ChatKey() string {
	if u.Message == nil {
		return ""
	}
	return fmt.Sprintf("%d", u.Message.Chat.ID)
}

// MatchesChat reports whether this update belongs to the chat a task is bound to.
// A binding is either a numeric chat id or an `@username`; both are accepted
// because both are what Telegram itself accepts as a chat_id.
func (u TelegramUpdate) MatchesChat(bound string) bool {
	if u.Message == nil {
		return false
	}
	bound = strings.TrimSpace(bound)
	if bound == "" {
		return false
	}
	if bound == u.ChatKey() {
		return true
	}
	if strings.HasPrefix(bound, "@") {
		return strings.EqualFold(strings.TrimPrefix(bound, "@"), u.Message.Chat.Username)
	}
	return false
}

// TelegramBot is the bot's own profile (telegram.get_me). The username is the
// useful part at design time: a bot cannot message a stranger, so the one
// instruction a designer needs to pass on is "message @thisbot first".
type TelegramBot struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"isBot"`
	FirstName string `json:"firstName"`
	Username  string `json:"username"`
}

// Handle renders the bot as something a person can be told to message.
func (b TelegramBot) Handle() string {
	if b.Username != "" {
		return "@" + b.Username
	}
	return b.FirstName
}

// TelegramWebhookInfo is the bot's webhook status (telegram.get_webhook_info).
//
// It matters here for one reason: a bot with a webhook set CANNOT be polled.
// Telegram treats getUpdates and webhook delivery as mutually exclusive and simply
// returns nothing to a poller — no error, no hint. Since polling is how this whole
// channel hears from people (FloMorphic is on-prem, so Telegram cannot reach in),
// a webhook silently kills it: the bot would ask its question and never receive an
// answer. So it is checked wherever that silence would otherwise be mistaken for
// "nobody has said anything".
type TelegramWebhookInfo struct {
	URL                string `json:"url"`
	PendingUpdateCount int    `json:"pendingUpdateCount"`
	LastErrorMessage   string `json:"lastErrorMessage"`
}

// Active reports whether a webhook is configured, and therefore whether polling is
// disabled for this bot.
func (w TelegramWebhookInfo) Active() bool { return strings.TrimSpace(w.URL) != "" }

// WebhookConflict is the one explanation both the bridge and the settings editor
// give, so an operator reads the same diagnosis wherever they hit it.
func (w TelegramWebhookInfo) WebhookConflict() string {
	return fmt.Sprintf(
		"this bot has a webhook set (%s), which disables the polling FloMorphic uses — Telegram delivers every update to that URL instead, so the bot can ask but never hear an answer. Remove the webhook on the bot, or give Human-in-the-Loop a bot of its own.",
		strings.TrimSpace(w.URL))
}

// TelegramWebhook returns the bound bot's webhook status.
func TelegramWebhook(ctx context.Context, store repository.Store, tg *models.TelegramBinding) (*TelegramWebhookInfo, error) {
	raw, err := telegramAction(ctx, store, tg, "get_webhook_info", map[string]any{})
	if err != nil {
		return nil, err
	}
	info := &TelegramWebhookInfo{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, info); err != nil {
			return nil, fmt.Errorf("telegram: decode webhook info: %w", err)
		}
	}
	return info, nil
}

// TelegramAccount is one connected bot as the gateway reports it (GET
// /v1/connections, filtered to Telegram). The UI lists these so a node can be
// bound to a bot, and the bridge uses `Alias` to act as it.
type TelegramAccount struct {
	ID           string `json:"id"`
	Service      string `json:"service"`
	Status       string `json:"status"`
	AccountLabel string `json:"accountLabel"`
	Alias        string `json:"alias"`
	AuthType     string `json:"authType"`
	IsDefault    bool   `json:"isDefault"`
}

// TelegramSendMessage delivers one facilitator turn to the bound chat, splitting
// text that exceeds Telegram's per-message limit. No parse mode is set: the
// facilitator writes plain prose for a messenger (see messengerAddendum), and
// asking Telegram to parse markdown would fail the whole send on one stray
// underscore.
func TelegramSendMessage(ctx context.Context, store repository.Store, tg *models.TelegramBinding, text string) error {
	if tg == nil || strings.TrimSpace(tg.ChatID) == "" {
		return fmt.Errorf("this Human-in-the-Loop node has no Telegram chat configured")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	for _, part := range splitForTelegram(text, telegramMaxText) {
		if _, err := telegramAction(ctx, store, tg, "send_message", map[string]any{
			"chatId": tg.ChatID,
			"text":   part,
		}); err != nil {
			return err
		}
	}
	return nil
}

// TelegramUpdates consumes the bot's pending updates from `after` onwards. It is
// a short poll (timeout 0): the bridge owns the cadence, and a long poll would
// hold a gateway request open per bound account.
//
// Telegram's `offset` is an ACK — asking for `after+1` is what tells the server
// the bridge is done with everything up to `after`, so an update is delivered
// once and survives a restart until it has been consumed.
func TelegramUpdates(ctx context.Context, store repository.Store, tg *models.TelegramBinding, after int64, limit int) ([]TelegramUpdate, error) {
	input := map[string]any{
		"limit":          limit,
		"timeout":        0,
		"allowedUpdates": []string{"message"},
	}
	if after > 0 {
		input["offset"] = after + 1
	}
	raw, err := telegramAction(ctx, store, tg, "get_updates", input)
	if err != nil {
		return nil, err
	}
	var out struct {
		Updates []TelegramUpdate `json:"updates"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("telegram: decode updates: %w", err)
		}
	}
	return out.Updates, nil
}

// TelegramLatestUpdateID reports the newest update id on the bot's stream, or 0
// when nothing is pending. It is how a session establishes its starting point:
// everything already on the stream when the flow reached the node is, by
// definition, not an answer to a question that had not been asked yet.
//
// A negative offset is Telegram's own idiom for "the last update", and it does
// NOT acknowledge anything — the backlog stays where it is.
func TelegramLatestUpdateID(ctx context.Context, store repository.Store, tg *models.TelegramBinding) (int64, error) {
	raw, err := telegramAction(ctx, store, tg, "get_updates", map[string]any{
		"offset":         -1,
		"limit":          1,
		"timeout":        0,
		"allowedUpdates": []string{"message"},
	})
	if err != nil {
		return 0, err
	}
	var out struct {
		Updates []TelegramUpdate `json:"updates"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return 0, fmt.Errorf("telegram: decode updates: %w", err)
		}
	}
	if len(out.Updates) == 0 {
		return 0, nil
	}
	return out.Updates[len(out.Updates)-1].UpdateID, nil
}

// TelegramAccounts lists the Telegram bots connected on a Connect connection
// (empty id ⇒ the default connection). The node settings editor calls it through
// the HTTP gateway passthrough; the bridge uses it to explain a binding that no
// longer resolves.
func TelegramAccounts(ctx context.Context, store repository.Store, connectionID string) ([]TelegramAccount, error) {
	client, err := connectClient(ctx, store, connectionID)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(ctx, "GET", "/v1/connections", nil, nil)
	if err != nil {
		return nil, err
	}
	if res.Status >= 400 {
		return nil, fmt.Errorf("OpenConnector returned %d for /v1/connections", res.Status)
	}
	var env struct {
		Data []TelegramAccount `json:"data"`
	}
	if err := json.Unmarshal(res.Body, &env); err != nil {
		return nil, fmt.Errorf("telegram: decode connections: %w", err)
	}
	out := make([]TelegramAccount, 0, len(env.Data))
	for _, a := range env.Data {
		if a.Service == TelegramService {
			out = append(out, a)
		}
	}
	return out, nil
}

// TelegramGetMe returns the bound bot's own profile, which is also the cheapest
// proof that the binding works at all: it is the one Telegram call that needs no
// chat, no permissions and no prior interaction, so a failure here is
// unambiguously about the connection, the token or the alias.
func TelegramGetMe(ctx context.Context, store repository.Store, tg *models.TelegramBinding) (*TelegramBot, error) {
	raw, err := telegramAction(ctx, store, tg, "get_me", map[string]any{})
	if err != nil {
		return nil, err
	}
	bot := &TelegramBot{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, bot); err != nil {
			return nil, fmt.Errorf("telegram: decode bot profile: %w", err)
		}
	}
	return bot, nil
}

// ---- the recipient directory ------------------------------------------------
//
// A Telegram bot cannot list its users. It learns a chat exists only when someone
// interacts with it, and that knowledge arrives once, on the update stream, which
// is consumed and expires in about 24 hours. So "who can this bot talk to?" has no
// answer at the moment a designer needs one — unless something has been writing it
// down. These two functions are that: the bridge records every chat it hears from
// (RecordRecipient), and discovery sweeps whatever is still pending on the stream
// into the same place (TelegramDiscoverRecipients).

// RecordRecipient notes that a bot can reach this chat. Failures are the caller's
// to log and ignore: the directory is a convenience for the next designer, never a
// precondition for the conversation currently happening.
func RecordRecipient(ctx context.Context, store repository.Store, tg *models.TelegramBinding, chat TelegramChat, seenAt int64) error {
	if chat.ID == 0 {
		return nil
	}
	return store.TelegramRecipients().Upsert(ctx, &models.TelegramRecipient{
		Connection: tg.Connection,
		Alias:      tg.Alias,
		ChatID:     strconv.FormatInt(chat.ID, 10),
		Type:       chat.Type,
		Title:      chat.Title,
		Username:   chat.Username,
		FirstName:  chat.FirstName,
		LastName:   chat.LastName,
		LastSeenAt: seenAt * 1000, // Telegram dates are seconds; the store is millis.
	})
}

// TelegramDiscoverRecipients sweeps the bot's pending updates into the directory
// and returns it, whole.
//
// The sweep is deliberately NON-DESTRUCTIVE: it reads with no offset, which is
// Telegram's "show me what is pending" and acknowledges nothing. That matters
// because the HITL bridge may be holding live sessions on this very bot, and
// acknowledging its updates here would swallow a person's answer.
//
// The flip side is that it can only ever see what has not been consumed yet, so on
// a busy bot it often finds nothing new. That is not a failure — the directory it
// returns is the accumulated answer, and the bridge keeps adding to it.
func TelegramDiscoverRecipients(ctx context.Context, store repository.Store, connection, alias string) ([]models.TelegramRecipient, error) {
	tg := &models.TelegramBinding{Connection: connection, Alias: alias}
	updates, err := TelegramUpdates(ctx, store, tg, 0, telegramDiscoverBatch)
	if err != nil {
		return nil, err
	}
	for _, up := range updates {
		if up.Message == nil {
			continue
		}
		if err := RecordRecipient(ctx, store, tg, up.Message.Chat, up.Message.Date); err != nil {
			return nil, err
		}
	}
	return store.TelegramRecipients().List(ctx, connection, alias, 0)
}

// telegramDiscoverBatch is one discovery sweep's page — Telegram's maximum, since
// a sweep wants breadth (as many distinct chats as possible) rather than order.
const telegramDiscoverBatch = 100

// ---- gateway plumbing -------------------------------------------------------

// telegramAction runs one `telegram.<name>` OpenConnector action as the bound
// account and returns the gateway's unwrapped `data` payload. The alias selects
// the account; omitting it lets the gateway use its default connection for the
// service.
func telegramAction(ctx context.Context, store repository.Store, tg *models.TelegramBinding, name string, input map[string]any) (json.RawMessage, error) {
	if tg == nil {
		return nil, fmt.Errorf("no Telegram binding on this task")
	}
	client, err := connectClient(ctx, store, tg.Connection)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{"input": input})
	if err != nil {
		return nil, fmt.Errorf("telegram: encode %s input: %w", name, err)
	}
	var query url.Values
	if alias := strings.TrimSpace(tg.Alias); alias != "" {
		query = url.Values{"alias": []string{alias}}
	}
	path := "/v1/actions/" + TelegramService + "." + name
	res, err := client.Do(ctx, "POST", path, query, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if res.Status >= 400 {
		return nil, fmt.Errorf("telegram %s: %s", name, gatewayMessage(res.Body, res.Status))
	}
	// Unwrap the gateway's {success,message,data} envelope when present.
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(res.Body, &env) == nil && len(env.Data) > 0 {
		return env.Data, nil
	}
	return json.RawMessage(res.Body), nil
}

// connectClient builds the gateway client for a Connect connection (the default
// one when the id is empty), using its runtime token — action execution lives on
// the `/v1` surface, which the runtime token authenticates.
func connectClient(ctx context.Context, store repository.Store, connectionID string) (*openconnector.Client, error) {
	var (
		conn *models.ConnectConnection
		err  error
	)
	if strings.TrimSpace(connectionID) != "" {
		conn, err = store.Connect().GetByID(ctx, connectionID)
	} else {
		conn, err = store.Connect().Default(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("no OpenConnector connection available — configure one under Connect")
	}
	if strings.TrimSpace(conn.Token) == "" {
		return nil, fmt.Errorf("OpenConnector connection %q has no runtime token", conn.Label)
	}
	return openconnector.New(conn.BaseURL, conn.Token), nil
}

// gatewayMessage digs the most specific message out of a gateway error body —
// Telegram's own "Bad Request: chat not found" rather than a bare status — and
// falls back to the status code.
func gatewayMessage(body []byte, status int) string {
	var env struct {
		Message     string `json:"message"`
		Error       string `json:"error"`
		Description string `json:"description"`
		Data        struct {
			Message     string `json:"message"`
			Description string `json:"description"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &env) == nil {
		for _, m := range []string{env.Description, env.Message, env.Error, env.Data.Description, env.Data.Message} {
			if strings.TrimSpace(m) != "" {
				return fmt.Sprintf("%s (HTTP %d)", strings.TrimSpace(m), status)
			}
		}
	}
	if s := strings.TrimSpace(string(body)); s != "" && len(s) <= 300 {
		return fmt.Sprintf("HTTP %d: %s", status, s)
	}
	return fmt.Sprintf("HTTP %d", status)
}

// splitForTelegram cuts text into chunks of at most max runes, preferring a
// paragraph then a line then a space boundary so a split does not land mid-word.
func splitForTelegram(text string, max int) []string {
	runes := []rune(text)
	if len(runes) <= max {
		return []string{text}
	}
	var parts []string
	for len(runes) > max {
		cut := max
		for _, sep := range []string{"\n\n", "\n", " "} {
			if i := strings.LastIndex(string(runes[:max]), sep); i > max/2 {
				cut = len([]rune(string(runes[:max])[:i]))
				break
			}
		}
		parts = append(parts, strings.TrimSpace(string(runes[:cut])))
		runes = runes[cut:]
	}
	if tail := strings.TrimSpace(string(runes)); tail != "" {
		parts = append(parts, tail)
	}
	return parts
}
