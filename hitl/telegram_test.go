package hitl

import (
	"strings"
	"testing"

	"github.com/FloMorphic/morph-api/models"
)

// newUpdate builds an update the way the gateway delivers one, for the routing
// tests below.
func newUpdate(id, chatID int64, username, text string) TelegramUpdate {
	return TelegramUpdate{
		UpdateID: id,
		Message: &TelegramMessage{
			Text: text,
			Chat: TelegramChat{ID: chatID, Username: username},
		},
	}
}

// Routing a reply to the right session is the whole correctness question of the
// bridge: a chat is bound as a number or as an @username (both are what Telegram
// itself accepts as a chat_id), and anything else must not match — answering the
// wrong session would put one flow's answers into another flow's context.
func TestTelegramUpdateMatchesChat(t *testing.T) {
	numeric := newUpdate(1, 123456789, "", "hello")
	named := newUpdate(2, -100987, "teamroom", "hello")

	cases := []struct {
		name  string
		up    TelegramUpdate
		bound string
		want  bool
	}{
		{name: "numeric id matches", up: numeric, bound: "123456789", want: true},
		{name: "a different numeric id does not", up: numeric, bound: "987654321", want: false},
		{name: "@username matches, case-insensitively", up: named, bound: "@TeamRoom", want: true},
		{name: "a different @username does not", up: named, bound: "@other", want: false},
		{name: "an @username never matches a chat that has none", up: numeric, bound: "@teamroom", want: false},
		{name: "an unbound chat matches nothing", up: numeric, bound: "  ", want: false},
		{name: "a non-message update matches nothing", up: TelegramUpdate{UpdateID: 3}, bound: "123456789", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.up.MatchesChat(tc.bound); got != tc.want {
				t.Fatalf("MatchesChat(%q) = %v, want %v", tc.bound, got, tc.want)
			}
		})
	}
}

// The facilitator reads text. A caption counts (the person attached something and
// wrote under it); a bare sticker or photo does not, and the bridge relies on an
// empty body to say so rather than sending the model nothing.
func TestTelegramUpdateBody(t *testing.T) {
	text := newUpdate(1, 1, "", "  the answer is 42  ")
	if got := text.Body(); got != "the answer is 42" {
		t.Fatalf("Body() = %q, want the trimmed text", got)
	}

	caption := newUpdate(2, 1, "", "")
	caption.Message.Caption = "see the attached invoice"
	if got := caption.Body(); got != "see the attached invoice" {
		t.Fatalf("Body() = %q, want the caption", got)
	}

	empty := newUpdate(3, 1, "", "")
	if got := empty.Body(); got != "" {
		t.Fatalf("Body() = %q, want empty for a media-only message", got)
	}
}

// A facilitator turn can run past Telegram's 4096-char limit (it may be quoting
// the brief), and the API rejects the whole message rather than truncating it. So
// long text is split — on a boundary, with nothing dropped.
func TestSplitForTelegram(t *testing.T) {
	if got := splitForTelegram("short", 4096); len(got) != 1 || got[0] != "short" {
		t.Fatalf("splitForTelegram(short) = %v, want one unchanged part", got)
	}

	// Paragraphs of 30 chars each, well past a 100-char limit.
	para := strings.Repeat("abcdefghij", 3)
	text := para + "\n\n" + para + "\n\n" + para + "\n\n" + para
	parts := splitForTelegram(text, 100)
	if len(parts) < 2 {
		t.Fatalf("splitForTelegram() = %d part(s), want it split", len(parts))
	}
	for i, p := range parts {
		if len([]rune(p)) > 100 {
			t.Fatalf("part %d is %d runes, over the limit", i, len([]rune(p)))
		}
		if p == "" {
			t.Fatalf("part %d is empty", i)
		}
	}
	// Nothing may be lost: the parts must still carry every paragraph.
	if n := strings.Count(strings.Join(parts, "\n\n"), para); n != 4 {
		t.Fatalf("rejoined parts contain %d paragraphs, want 4", n)
	}
}

// A session held in a messenger gets an extra instruction block: the person has
// no Close button, so the bot has to teach them /done, and it has to write for a
// phone. The in-app session must NOT get it — there the button is right there.
func TestBuildMessagesAddsMessengerGuidanceOnlyForMessengers(t *testing.T) {
	base := models.HumanTask{Prompt: "Establish the shipping address."}

	direct := base
	direct.Channel = models.HumanTaskDirect
	if got := BuildMessages(&direct, "")[0].Text; strings.Contains(got, string(CmdDone)) {
		t.Fatal("the in-app session was told to ask for /done, which it has no use for")
	}

	tg := base
	tg.Channel = models.HumanTaskTelegram
	mission := BuildMessages(&tg, "")[0].Text
	if !strings.Contains(mission, string(CmdDone)) {
		t.Fatalf("a Telegram session was not told how to end: %q", mission)
	}
	if !strings.HasPrefix(mission, SystemPrompt) {
		t.Fatal("the messenger addendum replaced the mission prompt instead of extending it")
	}
}

// The brief and the stored thread have to reach the model in order, whatever the
// channel, and a transient nudge must never be mistaken for a stored turn.
func TestBuildMessagesOrdersBriefThenThread(t *testing.T) {
	task := &models.HumanTask{
		Prompt:  "Establish the shipping address.",
		Channel: models.HumanTaskDirect,
		Messages: []models.HumanTaskMessage{
			{Role: "assistant", Text: "Which address should we use?"},
			{Role: "human", Text: "The Berlin office."},
		},
	}
	msgs := BuildMessages(task, OpeningNudge)
	if len(msgs) != 5 {
		t.Fatalf("got %d messages, want mission + brief + 2 turns + nudge", len(msgs))
	}
	if !strings.Contains(msgs[1].Text, task.Prompt) {
		t.Fatalf("message 1 is not the brief: %q", msgs[1].Text)
	}
	if msgs[2].Text != "Which address should we use?" || msgs[3].Text != "The Berlin office." {
		t.Fatalf("the thread arrived out of order: %+v", msgs[2:4])
	}
	if msgs[4].Text != OpeningNudge {
		t.Fatalf("the transient nudge is not last: %q", msgs[4].Text)
	}
	// An empty nudge adds nothing — an ordinary turn must not be padded with one.
	if n := len(BuildMessages(task, "")); n != 4 {
		t.Fatalf("an empty nudge produced %d messages, want 4", n)
	}
}

// A binding with no chat cannot be delivered, and failing before the gateway call
// is what turns "the gateway said 400" into something an operator can act on.
func TestTelegramSendMessageRequiresAChat(t *testing.T) {
	for _, tg := range []*models.TelegramBinding{nil, {Alias: "bot"}, {ChatID: "   "}} {
		// A nil store proves the guard returns before anything is resolved.
		if err := TelegramSendMessage(nil, nil, tg, "hello"); err == nil {
			t.Fatalf("TelegramSendMessage(%+v) error = nil, want a missing-chat error", tg)
		}
	}
}
