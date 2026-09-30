package hitl

import (
	"strings"
	"testing"

	"github.com/FloMorphic/morph-api/models"
)

// A messenger session has no UI, so a command IS the control — and the bridge acts
// on what this returns (closing a task and releasing a parked flow for /done). It
// has to accept the forms Telegram actually produces and refuse anything that only
// resembles one.
func TestParseCommand(t *testing.T) {
	cases := map[string]Command{
		"/done":                CmdDone,
		"/DONE":                CmdDone,
		"  /done  ":            CmdDone,
		"/done.":               CmdDone,
		"/done!":               CmdDone,
		"/done@flomorphic_bot": CmdDone,
		"/done and thanks":     CmdDone,
		"/status":              CmdStatus,
		"/help":                CmdHelp,
		// Telegram renders /start as a button in a fresh chat, so it is answered as
		// help rather than met with "I don't know that".
		"/start":                CmdHelp,
		"/start@flomorphic_bot": CmdHelp,
		// Not commands: a bare word, a command that is not the first word, a longer
		// word that merely starts the same way, and one this session does not have.
		"done":               "",
		"i am not /done yet": "",
		"/donenow":           "",
		"/cancel":            "",
		"":                   "",
		"   ":                "",
		"that is everything": "",
	}
	for text, want := range cases {
		if got := ParseCommand(text); got != want {
			t.Fatalf("ParseCommand(%q) = %q, want %q", text, got, want)
		}
	}
}

// Something that was clearly meant as a command has to be recognised as such even
// when it is not one we have, so the person gets the list back instead of having
// their typo answered as if it were prose.
func TestLooksLikeCommand(t *testing.T) {
	for _, text := range []string{"/finish", "/cancel now", "/done"} {
		if !LooksLikeCommand(text) {
			t.Fatalf("LooksLikeCommand(%q) = false, want true", text)
		}
	}
	for _, text := range []string{"", "  ", "done", "the ratio is 1/2", "/"} {
		if LooksLikeCommand(text) {
			t.Fatalf("LooksLikeCommand(%q) = true, want false", text)
		}
	}
}

// /status is answered from the task record, not from the model, so it has to be
// true: an early session has no questions yet (the node never declares them) and
// must say so rather than report an empty checklist as if nothing were needed.
func TestStatusMessage(t *testing.T) {
	early := StatusMessage(&models.HumanTask{Title: "Confirm the address"})
	if !strings.Contains(early, "still working out") {
		t.Fatalf("early status does not explain the empty question set: %q", early)
	}
	if !strings.Contains(early, string(CmdDone)) {
		t.Fatal("status never says how to finish")
	}

	partial := StatusMessage(&models.HumanTask{
		Questions: []models.HumanTaskQuestion{
			{Text: "Which address?", Answer: "The Berlin office."},
			{Text: "Which courier?"},
		},
	})
	if !strings.Contains(partial, "1 of 2") {
		t.Fatalf("status miscounts answers: %q", partial)
	}
	if !strings.Contains(partial, "[answered] Which address?") || !strings.Contains(partial, "[open] Which courier?") {
		t.Fatalf("status does not mark each question: %q", partial)
	}
}

// The opening turn is the only place the controls are guaranteed to be stated: the
// mission prompt asks the model to mention /done, but a model may not, and Telegram
// will not show a command menu (no setMyCommands on the gateway). So the footer has
// to name every command itself — without it they are undiscoverable.
func TestOpeningFooterNamesEveryCommand(t *testing.T) {
	footer := OpeningFooter()
	for _, c := range Commands {
		if !strings.Contains(footer, string(c.Name)) {
			t.Fatalf("OpeningFooter() omits %s: %q", c.Name, footer)
		}
	}
	// It is appended to a turn, so it has to start on its own line rather than run
	// into the last sentence of the bot's prose.
	if !strings.HasPrefix(footer, "\n\n") {
		t.Fatalf("OpeningFooter() does not separate itself from the turn: %q", footer)
	}
}

// Every command must appear in the list the person is shown, or it is unreachable:
// there is no Telegram command menu behind this (the gateway exposes no
// setMyCommands), so this text is the only place they are documented.
func TestCommandListCoversEveryCommand(t *testing.T) {
	list := CommandList()
	for _, c := range Commands {
		if !strings.Contains(list, string(c.Name)) {
			t.Fatalf("CommandList() omits %s: %q", c.Name, list)
		}
	}
	// And the model is told the same set, so it never invents a fourth.
	mission := BuildMessages(&models.HumanTask{Channel: models.HumanTaskTelegram}, "")[0].Text
	for _, c := range Commands {
		if !strings.Contains(mission, string(c.Name)) {
			t.Fatalf("the mission prompt omits %s", c.Name)
		}
	}
}
