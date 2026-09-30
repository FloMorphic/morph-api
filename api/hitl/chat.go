package hitlControllers

import (
	"context"
	"fmt"
	"strings"

	"github.com/FloMorphic/morph-api/api/wslog"
	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/hitl"
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
	if err := elsewhere(task); err != nil {
		return etc.Fail(c, fiber.StatusConflict, err.Error())
	}

	cfg, err := hitl.ChatConfig(c.Context(), ctl.store, task)
	if err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, err.Error())
	}

	// Record the human turn first, so it is durable even if the model call fails.
	task, err = ctl.repo.AppendMessage(c.Context(), task.ID, models.HumanTaskMessage{Role: "human", Text: in.Text})
	if err != nil {
		return etc.FailFromRepo(c, err, "human task not found")
	}

	reply, err := streamReply(c.Context(), cfg, task.ID, hitl.BuildMessages(task, ""))
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
	if err := elsewhere(task); err != nil {
		return etc.Fail(c, fiber.StatusConflict, err.Error())
	}

	cfg, err := hitl.ChatConfig(c.Context(), ctl.store, task)
	if err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, err.Error())
	}

	// No stored turns yet: kick the model with a transient user nudge (not saved)
	// so every provider — including ones that require the thread to open on a user
	// turn — produces the assistant's opening message.
	reply, err := streamReply(c.Context(), cfg, task.ID, hitl.BuildMessages(task, hitl.OpeningNudge))
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

// elsewhere refuses an in-app conversation turn on a task whose session is held
// in a messenger. Only one facilitator may drive a thread: for a `telegram` task
// that is the bridge (inflow/hitl_telegram.go), which is already talking to the
// person and appending their turns here. Letting this endpoint run the model too
// would interleave two conversations into one transcript.
//
// The app is still the record of the session — the whole thread is mirrored onto
// the task and closing it from the app works as always.
func elsewhere(task *models.HumanTask) error {
	if task.Channel == models.HumanTaskTelegram {
		return fmt.Errorf("this session is held in Telegram — the person answers in the chat, and every turn is mirrored here")
	}
	return nil
}
