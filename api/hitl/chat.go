package hitlControllers

import (
	"context"
	"fmt"
	"strings"

	"github.com/FloMorphic/morph-api/api/wslog"
	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/llm"
	"github.com/FloMorphic/morph-api/models"
	"github.com/gofiber/fiber/v3"
)

// The conversation bot runs on the frontend's terms but the model call lives
// here, so the provider token never leaves the server: the task carries only the
// id of the settings profile that holds the provider config (see
// models.HumanTask.SettingsID), and this controller reads the token from the
// settings store at the moment of the call.

type chatInput struct {
	Text string `json:"text"`
}

// chat handles POST /hitl/id/:id/chat — the person says something and the bot
// answers. It appends the human turn, runs the model configured on the task's
// provider profile against the whole thread (opened by the node's prompt), and
// appends the assistant reply. The updated task is returned and also pushed on
// the `hitl.message` socket event so any other open view of the same task keeps
// up.
func (ctl *controller) chat(c fiber.Ctx) error {
	var in chatInput
	if err := c.Bind().Body(&in); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid chat payload")
	}
	if strings.TrimSpace(in.Text) == "" {
		return etc.Fail(c, fiber.StatusBadRequest, "text is required")
	}

	task, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "human task not found")
	}
	if task.Status == models.HumanTaskClosed {
		return etc.Fail(c, fiber.StatusConflict, "this task is closed")
	}

	cfg, err := ctl.chatConfig(c.Context(), task)
	if err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, err.Error())
	}

	// Record the human turn first, so it is durable even if the model call fails.
	task, err = ctl.repo.AppendMessage(c.Context(), task.ID, models.HumanTaskMessage{Role: "human", Text: in.Text})
	if err != nil {
		return etc.FailFromRepo(c, err, "human task not found")
	}

	reply, err := streamReply(c.Context(), cfg, task.ID, buildMessages(task, ""))
	if err != nil {
		// The human turn is saved; report the model failure but hand back the task
		// so the UI shows the message it already accepted.
		return etc.Send(c, fiber.StatusBadGateway, task, err.Error())
	}

	task, err = ctl.repo.AppendMessage(c.Context(), task.ID, models.HumanTaskMessage{Role: "assistant", Text: reply})
	if err != nil {
		return etc.FailFromRepo(c, err, "human task not found")
	}
	finishStream(task)
	return etc.OK(c, task)
}

// start handles POST /hitl/id/:id/start — open the session by having the bot
// produce the first turn from the node's prompt. It is only meaningful on a
// fresh thread; calling it on a task that already has messages just returns the
// task unchanged, so the frontend can call it idempotently when a panel opens.
func (ctl *controller) start(c fiber.Ctx) error {
	task, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "human task not found")
	}
	if task.Status == models.HumanTaskClosed {
		return etc.Fail(c, fiber.StatusConflict, "this task is closed")
	}
	if len(task.Messages) > 0 {
		return etc.OK(c, task)
	}

	cfg, err := ctl.chatConfig(c.Context(), task)
	if err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, err.Error())
	}

	// No stored turns yet: kick the model with a transient user nudge (not saved)
	// so every provider — including ones that require the thread to open on a user
	// turn — produces the assistant's opening message.
	reply, err := streamReply(c.Context(), cfg, task.ID, buildMessages(task, "Begin the conversation with me."))
	if err != nil {
		return etc.Send(c, fiber.StatusBadGateway, task, err.Error())
	}
	task, err = ctl.repo.AppendMessage(c.Context(), task.ID, models.HumanTaskMessage{Role: "assistant", Text: reply})
	if err != nil {
		return etc.FailFromRepo(c, err, "human task not found")
	}
	finishStream(task)
	return etc.OK(c, task)
}

// ---- Streaming --------------------------------------------------------------

// hitlStreamEvent is the socket event the bot's reply streams on — distinct from
// `hitl.message` (the final, persisted turn) so a client can render tokens as
// they arrive and reconcile against the stored message when the turn completes.
const hitlStreamEvent = "hitl.stream"

// streamChunk is one `hitl.stream` payload: an incremental token `delta` for a
// task, or a terminal `done` marker. `seq` orders the deltas within a turn.
type streamChunk struct {
	TaskID string `json:"taskId"`
	Seq    int    `json:"seq,omitempty"`
	Delta  string `json:"delta,omitempty"`
	Done   bool   `json:"done,omitempty"`
}

// streamReply runs the model with token streaming, pushing each delta on the
// `hitl.stream` socket event, and returns the full reply once complete. The user
// turn already arrived over HTTP; only the assistant's reply streams.
func streamReply(ctx context.Context, cfg llm.Config, taskID string, msgs []llm.Message) (string, error) {
	seq := 0
	return llm.ChatStream(ctx, cfg, msgs, func(delta string) {
		seq++
		wslog.Emit(hitlStreamEvent, streamChunk{TaskID: taskID, Seq: seq, Delta: delta})
	})
}

// finishStream closes out a streamed turn: a terminal `done` marker so clients
// drop the live buffer, then the full persisted task on `hitl.message`.
func finishStream(task *models.HumanTask) {
	wslog.Emit(hitlStreamEvent, streamChunk{TaskID: task.ID, Done: true})
	wslog.Emit("hitl.message", task)
}

// chatConfig resolves the provider config for a task's conversation from its
// bound settings profile. It is a plain error (not a repo error) so the caller
// surfaces it as a 400 with a message the operator can act on.
func (ctl *controller) chatConfig(ctx context.Context, task *models.HumanTask) (llm.Config, error) {
	if strings.TrimSpace(task.SettingsID) == "" {
		return llm.Config{}, fmt.Errorf("this Human-in-the-Loop node has no chat provider configured — bind an LLM settings profile to the node")
	}
	profile, err := ctl.store.NodeSettings().GetByID(ctx, task.SettingsID)
	if err != nil {
		return llm.Config{}, fmt.Errorf("chat provider profile not found (%s)", task.SettingsID)
	}
	cfg := llm.ConfigFromSettings(profile.Settings)
	if err := cfg.Validate(); err != nil {
		return llm.Config{}, err
	}
	return cfg, nil
}

// hitlSystemPrompt frames the bot's role and guardrails so it stays a
// Human-in-the-Loop facilitator rather than a general assistant. The node's
// prompt (below it) is only the *brief* — what this particular flow got stuck on
// — so without this the model has no sense of its mission and drifts. Kept in
// step with the frontend's local-mode copy (flomorphic-wapp src/api/hitl.ts).
const hitlSystemPrompt = `You are a Human-in-the-Loop assistant inside an automated workflow. The workflow paused because it could not settle something on its own and needs a person's input before it can continue. Your only job is to help that person reach the answers the workflow needs — nothing else.

A brief follows describing what must be established and the context the workflow built up to this point. Work from it:
- Read the brief and context and identify the specific question(s) that must be answered for the workflow to continue.
- In plain language, tell the person what the workflow is stuck on and what you need from them.
- Ask focused questions, a few at a time, and keep every turn aimed at reaching those answers.
- Stay strictly on this task. Do not answer unrelated requests, do not invent facts, and do not make decisions that are the person's to make.
- When you have what the workflow needs, briefly restate the answer(s) so the person can confirm and close the session.

Be concise and clear. The brief follows.`

// buildMessages turns a task into the model's message list: the fixed mission
// prompt, then the node's prompt as the brief (what to establish with the person
// and the questions to work out), then the stored thread in order. An optional
// transient user turn is appended for kicking off an empty thread; it is never
// persisted.
func buildMessages(task *models.HumanTask, transientUser string) []llm.Message {
	msgs := make([]llm.Message, 0, len(task.Messages)+3)
	msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Text: hitlSystemPrompt})
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
