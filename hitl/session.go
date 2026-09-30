// Package hitl is the Human-in-the-Loop session core: the facilitator bot's
// prompt contract, its provider resolution, and the messenger bridges that hold
// the same conversation somewhere other than the app.
//
// It exists because a session is no longer reached one way. The in-app chat runs
// it over HTTP (api/hitl), and the Telegram bridge runs it off a poll loop
// (inflow/hitl_telegram.go) — but it must be the SAME conversation either way:
// the same mission prompt, the same brief, the same thread stored on the task.
// Those pieces live here so neither caller owns them, and so a task's transcript
// is identical whichever channel produced it.
//
// This package deliberately does not import inflow: closing a session resumes a
// parked flow, which is inflow's job, so the callers own the close and this
// package only owns the conversation.
package hitl

import (
	"context"
	"fmt"
	"strings"

	"github.com/FloMorphic/morph-api/llm"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
)

// SystemPrompt frames the bot's role and guardrails so it stays a
// Human-in-the-Loop facilitator rather than a general assistant. The node's
// prompt (below it) is only the *brief* — what this particular flow got stuck on
// — so without this the model has no sense of its mission and drifts. Kept in
// step with the frontend's local-mode copy (flomorphic-wapp src/api/hitl.ts).
const SystemPrompt = `You are a Human-in-the-Loop assistant inside an automated workflow. The workflow paused because it could not settle something on its own and needs a person's input before it can continue. Your only job is to help that person reach the answers the workflow needs — nothing else.

A brief follows describing what must be established and the context the workflow built up to this point. Work from it:
- Read the brief and context and identify the specific question(s) that must be answered for the workflow to continue.
- In plain language, tell the person what the workflow is stuck on and what you need from them.
- Ask focused questions, a few at a time, and keep every turn aimed at reaching those answers.
- Stay strictly on this task. Do not answer unrelated requests, do not invent facts, and do not make decisions that are the person's to make.
- When you have what the workflow needs, briefly restate the answer(s) so the person can confirm and close the session.

Be concise and clear. The brief follows.`

// messengerAddendum is appended to the mission prompt for a session held in a
// messenger rather than the app. There is no "close the session" button in a chat
// window, so the bot has to teach the person the commands that stand in for the
// UI — and it has to write for a phone: no markdown scaffolding, short turns.
//
// The commands themselves are handled by the bridge, not by the model (see
// commands.go), so the model is told to point at them and told NOT to answer them:
// a model improvising a reply to /done would tell the person the workflow was
// released when nothing had happened.
var messengerAddendum = `

This conversation is happening in a messenger app, not in the FloMorphic web app:
- Write for a phone screen. Short paragraphs, no markdown headings, no tables, no code fences.
- The person cannot click anything. Everything happens by replying in this chat.
- These commands are handled by the system, not by you. Never answer one yourself and never claim to have acted on one:
` + CommandList() + `
- In your opening message, after you have said what you need, tell the person they can reply ` + string(CmdDone) + ` when they are finished. Only ` + string(CmdDone) + ` ends the session — do not claim anything else will.`

// OpeningNudge is the transient user turn used to make a provider produce the
// bot's opening message on an empty thread. It is never persisted — some
// providers refuse a thread that does not open on a user turn, so this stands in
// for the person who has not said anything yet.
const OpeningNudge = "Begin the conversation with me."

// BuildMessages turns a task into the model's message list: the fixed mission
// prompt (plus the messenger addendum when the session is not held in the app),
// then the node's prompt as the brief, then the stored thread in order. An
// optional transient user turn is appended for kicking off an empty thread; it
// is never persisted.
func BuildMessages(task *models.HumanTask, transientUser string) []llm.Message {
	mission := SystemPrompt
	if task.Channel != "" && task.Channel != models.HumanTaskDirect {
		mission += messengerAddendum
	}
	msgs := make([]llm.Message, 0, len(task.Messages)+3)
	msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Text: mission})
	if strings.TrimSpace(task.Prompt) != "" {
		msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Text: "Brief for this session:\n\n" + task.Prompt})
	}
	for _, m := range task.Messages {
		msgs = append(msgs, llm.Message{Role: m.Role, Text: m.Text})
	}
	if strings.TrimSpace(transientUser) != "" {
		msgs = append(msgs, llm.Message{Role: llm.RoleUser, Text: transientUser})
	}
	return msgs
}

// ChatConfig resolves the provider config for a task's conversation from its
// bound settings profile. The token stays in the settings store and is read at
// the moment of the call, so it never rides on the task or in the flow graph.
//
// The error is a plain error (not a repository one) so an HTTP caller can surface
// it as a 400 with a message the operator can act on.
func ChatConfig(ctx context.Context, store repository.Store, task *models.HumanTask) (llm.Config, error) {
	if strings.TrimSpace(task.SettingsID) == "" {
		return llm.Config{}, fmt.Errorf("this Human-in-the-Loop node has no chat provider configured — bind an LLM settings profile to the node")
	}
	profile, err := store.NodeSettings().GetByID(ctx, task.SettingsID)
	if err != nil {
		return llm.Config{}, fmt.Errorf("chat provider profile not found (%s)", task.SettingsID)
	}
	cfg := llm.ConfigFromSettings(profile.Settings)
	if err := cfg.Validate(); err != nil {
		return llm.Config{}, err
	}
	return cfg, nil
}
