package hitl

import (
	"fmt"
	"strings"

	"github.com/FloMorphic/morph-api/models"
)

// The slash commands a messenger session understands.
//
// A session held in a chat has no UI: no Close button, no task panel, no list of
// what has been answered. Everything the app offers around the conversation has
// to be reachable as something the person can type, which is what these are.
//
// They are handled by the bridge, not by the model, because each one is a fact or
// an action rather than a turn of conversation: `/done` closes the session and
// releases a parked flow, `/status` reports the record, `/help` explains the
// session itself. Asking the model to do any of that would make it guess at state
// it cannot see, and a hallucinated "yes, the workflow is released" would be
// actively harmful.
//
// They are NOT registered with Telegram's command menu: that needs
// `setMyCommands`, which the OpenConnector gateway does not expose (its telegram
// surface is get_me / get_webhook_info / get_updates / get_chat / send_message /
// send_photo / send_document). So the bot states them instead — in its opening
// turn, and whenever someone types something that looks like a command it does
// not know.
type Command string

const (
	// CmdDone finishes the session. For a parked flow this is what releases the
	// run, which is why it is the one command the mission prompt promises.
	CmdDone Command = "/done"
	// CmdStatus reports where the session stands — what has been asked and
	// answered so far — from the task record rather than the model's memory.
	CmdStatus Command = "/status"
	// CmdHelp explains what this conversation is and lists these commands.
	CmdHelp Command = "/help"
)

// Commands is the set in the order they are shown to the person: what ends the
// session first, because that is the one they need and cannot guess.
var Commands = []struct {
	Name Command
	What string
}{
	{CmdDone, "finish — you are done answering and the workflow can continue"},
	{CmdStatus, "show what has been asked and answered so far"},
	{CmdHelp, "show this list"},
}

// CmdStart is Telegram's own conventional entry command. It is deliberately NOT in
// Commands — it is not a session control, and listing it would invite someone to
// press it mid-conversation expecting something to restart. But Telegram renders it
// as a START button in a fresh chat and every bot is expected to answer it, so it is
// recognised as an alias for /help rather than met with "I don't know that".
const CmdStart Command = "/start"

// ParseCommand reads the command a message starts with, or "" when it is ordinary
// text for the facilitator.
//
// It tolerates the forms Telegram actually produces: `/done@botname` (added
// automatically in groups), trailing punctuation, and trailing words — someone
// typing "/done thanks!" means /done. A command must be the FIRST word, so "I am
// not /done yet" stays a sentence.
func ParseCommand(text string) Command {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	first := strings.ToLower(strings.TrimRight(fields[0], ".,!?;:"))
	if !strings.HasPrefix(first, "/") {
		return ""
	}
	// Strip the @botname suffix Telegram appends in group chats.
	if i := strings.IndexByte(first, '@'); i > 0 {
		first = first[:i]
	}
	for _, c := range Commands {
		if first == string(c.Name) {
			return c.Name
		}
	}
	if first == string(CmdStart) {
		return CmdHelp
	}
	return ""
}

// LooksLikeCommand reports whether a message was meant as a command, whether or
// not it is one this session knows. It is what lets an unknown `/finish` get the
// command list back instead of being answered as if it were a sentence — the
// person is clearly reaching for a control, and there is no menu to look it up in.
func LooksLikeCommand(text string) bool {
	fields := strings.Fields(text)
	return len(fields) > 0 && strings.HasPrefix(fields[0], "/") && len(fields[0]) > 1
}

// CommandList renders the commands as lines for a chat message.
func CommandList() string {
	var b strings.Builder
	for _, c := range Commands {
		fmt.Fprintf(&b, "%s — %s\n", c.Name, c.What)
	}
	return strings.TrimRight(b.String(), "\n")
}

// HelpMessage is the reply to /help (and to an unrecognised command): what this
// conversation is for, and how to control it.
func HelpMessage(task *models.HumanTask) string {
	title := strings.TrimSpace(task.Title)
	if title == "" {
		title = "a step that needs your input"
	}
	return fmt.Sprintf(
		"This is an automated workflow that paused at %q and needs your input before it can carry on. "+
			"Answer in your own words and I will work through it with you.\n\n%s",
		title, CommandList())
}

// UnknownCommandMessage is the reply to something that was clearly meant as a
// command but is not one. It names what was typed so the person can see the typo.
func UnknownCommandMessage(text string) string {
	typed := strings.Fields(text)[0]
	return fmt.Sprintf("I don't know %s. What I understand:\n\n%s\n\nAnything else you type is an answer to the question above.", typed, CommandList())
}

// OpeningFooter is appended verbatim to the facilitator's first turn.
//
// The mission prompt also tells the model to mention /done, but an instruction to a
// model is a hope, not a guarantee — and these commands are the only controls the
// person has. Worse, Telegram will not surface them in its own command menu (that
// needs setMyCommands, which the gateway does not expose), so if the bot forgets to
// say them they are genuinely undiscoverable.
//
// So the bridge states them itself, deterministically, once. It is appended rather
// than sent as a second message to keep the session to one notification, and it is
// recorded on the task with the turn it belongs to so the transcript matches what
// the person actually saw.
func OpeningFooter() string {
	return "\n\n— — —\n" + string(CmdDone) + " when you're finished · " +
		string(CmdStatus) + " what's left · " + string(CmdHelp)
}

// StatusMessage reports where the session stands, read off the task record rather
// than asked of the model — the point of the command is to be the truth, not a
// recollection of it.
//
// A task with no questions yet is the normal early state, not an empty report: the
// node never declares questions, they are worked out in the conversation, so
// "still working out what to ask" is the accurate thing to say.
func StatusMessage(task *models.HumanTask) string {
	var b strings.Builder
	if len(task.Questions) == 0 {
		b.WriteString("Nothing is pinned down yet — we are still working out what needs answering.")
	} else {
		answered := 0
		for _, q := range task.Questions {
			if strings.TrimSpace(q.Answer) != "" {
				answered++
			}
		}
		fmt.Fprintf(&b, "%d of %d question(s) answered:\n", answered, len(task.Questions))
		for _, q := range task.Questions {
			mark := "open"
			if strings.TrimSpace(q.Answer) != "" {
				mark = "answered"
			}
			fmt.Fprintf(&b, "\n· [%s] %s", mark, q.Text)
		}
	}
	b.WriteString("\n\nReply ")
	b.WriteString(string(CmdDone))
	b.WriteString(" when you are finished and the workflow will continue.")
	return b.String()
}
