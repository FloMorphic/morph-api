package models

import fuseModels "github.com/Inflowenger/inflow-fusion/models"

// HumanTaskStatus is the lifecycle of a Human-in-the-Loop task.
//
//   - open:     created by a running workflow, awaiting human answers.
//   - answered: every question has an answer; the person can close the session.
//   - closed:   the person is done. For a `park` task this is what releases the
//     flow: a fresh run is entered on the captured next nodes.
type HumanTaskStatus string

const (
	HumanTaskOpen     HumanTaskStatus = "open"
	HumanTaskAnswered HumanTaskStatus = "answered"
	HumanTaskClosed   HumanTaskStatus = "closed"
)

// HumanTaskQuestion is one question put to the person during the session.
//
// Questions are NOT declared on the node: a flow hands over to a human exactly
// when it could not settle something itself, so what has to be asked is not
// knowable at design time. They are worked out in the conversation — from the
// run's history, against the node's prompt — and appended here as they are
// raised, so a freshly recorded task has none. When every one carries an answer
// the task flips to `answered`.
type HumanTaskQuestion struct {
	ID         string `json:"id"`
	Text       string `json:"text"`
	Answer     string `json:"answer"`
	AnsweredAt int64  `json:"answeredAt"`
}

// HumanTaskMode is what the hitl svc handler answered the runtime with when the
// flow reached the node — decided at design time and carried in the node's
// compile-time `op` payload.
//
//   - park:     the handler replied with a `stop` command, so the runtime dropped
//     the node's next and the run finished here. The task's Nexts are
//     what a resume run rebuilds its start from once the session closes.
//   - continue: the handler only recorded the task and replied plainly (no
//     command), so the flow carried straight on through the node's next.
type HumanTaskMode string

const (
	HumanTaskPark     HumanTaskMode = "park"
	HumanTaskContinue HumanTaskMode = "continue"
)

// HumanTaskChannel is where the conversation with the person is held. `direct`
// (the in-app chat under Operate -> Human Tasks) and `telegram` (the same
// facilitator conversation, delivered to a Telegram chat through OpenConnector —
// see TelegramBinding) are served end to end. `whatsapp` is recorded so a flow
// can already declare its intent, but needs a provider integration before a
// session is delivered on it.
//
// The channel only moves the conversation: whatever it is, the task is recorded
// here and listed under Human Tasks, and closing it is what releases a parked
// flow. A messenger channel mirrors every turn onto the task, so the in-app view
// stays the record of what was said.
type HumanTaskChannel string

const (
	HumanTaskDirect   HumanTaskChannel = "direct"
	HumanTaskTelegram HumanTaskChannel = "telegram"
	HumanTaskWhatsapp HumanTaskChannel = "whatsapp"
)

// TelegramBinding is a `telegram` task's delivery binding plus the small bit
// of runtime state the bridge keeps on it.
//
// FloMorphic holds no bot token: the bot is an account connected in
// OpenConnector (oomol's hosted gateway or a self-hosted one), reached through
// the stored Connect connection. So a Telegram HITL node names three things —
// which Connect connection, which connected bot on it, and which chat to talk
// to — and every Telegram call the bridge makes is an OpenConnector action run
// as that account.
//
// Connection / Alias / ChatID are design-time config carried in the node's `op`
// payload (ChatID may be written as a `{{$.path}}` and arrives resolved, so a
// flow can route the session to whoever it just looked up). Cursor / Opened are
// written by the bridge as the session runs.
type TelegramBinding struct {
	// Connection is the Connect connection id whose gateway holds the bot, or
	// empty for the default connection.
	Connection string `json:"connection,omitempty"`
	// Alias selects the connected Telegram account on that gateway (the `alias`
	// GET /v1/connections reports). Empty means the gateway's default account.
	Alias string `json:"alias,omitempty"`
	// ChatID is the Telegram chat the session is held in — a numeric chat id or
	// an `@channelusername`.
	ChatID string `json:"chatId,omitempty"`
	// Cursor is the highest Telegram update id the bridge has consumed for this
	// account. It is the resume point after a restart, and it is set when the
	// session opens (to the account's newest update) so a session never replays
	// messages that predate it.
	Cursor int64 `json:"cursor,omitempty"`
	// Opened records that the facilitator's first turn was delivered to the chat,
	// so the bridge opens each session exactly once.
	Opened bool `json:"opened,omitempty"`
}

// HumanTaskMessage is one turn of the free-form chat thread the human uses to
// understand the task's context. The LLM assistant reply is produced on the
// frontend; the backend only stores/returns the thread.
type HumanTaskMessage struct {
	ID   string `json:"id"`
	Role string `json:"role"` // "human" | "assistant" | "system"
	Text string `json:"text"`
	At   int64  `json:"at"`
}

// HumanTask is a Human-in-the-Loop (HITL) task. It is created (upserted) by the
// inflow svc handler when a running workflow reaches a `humanInLoop` node — never
// through the REST API. The web app then lists it, opens it as a chat-style
// conversation, answers its questions, and closes it.
//
// PID/FlowID/NodeID/ContextID tie the task back to the exact process step so the
// inflow runtime can resume (or terminate) the flow based on the outcome. `Data`
// is the raw node-data snapshot recorded at creation for traceability.
//
// JSON tags mirror flomorphic-wapp's HumanTask interface so the wire shape is
// drop-in for the web app.
type HumanTask struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	Status    HumanTaskStatus `json:"status"`
	PID       string          `json:"pid"`
	FlowID    string          `json:"flowId"`
	NodeID    string          `json:"nodeId"`
	ContextID string          `json:"contextId"`
	// Mode / Channel / Prompt are the node's design-time session config, carried
	// in the extrinsic's `op` payload.
	Mode    HumanTaskMode    `json:"mode,omitempty"`
	Channel HumanTaskChannel `json:"channel,omitempty"`
	// Telegram is set when Channel is `telegram`: the bot/chat the session is
	// delivered to, and the bridge's own cursor over that bot's updates.
	Telegram *TelegramBinding `json:"telegram,omitempty"`
	// Prompt is the conversation opener, ready to show a person: the node writes
	// it with `{{$.path}}` variables and the runtime resolves them against the
	// run's context before the svc handler is called, so what lands here is the
	// subject matter itself — the message stack an upstream node built, the
	// question it ended on.
	Prompt string `json:"prompt,omitempty"`
	// SettingsID points at the node-settings profile that holds the conversation
	// bot's LLM provider config (provider / model / token / base URL). The chat
	// service loads it from the settings store at run time, so the token stays in
	// the store and never rides along in the flow graph or on the task. Carried in
	// the node's compile-time `op` payload; empty when the node bound no profile.
	SettingsID string `json:"settingsId,omitempty"`
	// Key is the HITL node's result binding. Closing the session writes the
	// conversation outcome into the run's context under `$.<Key>`, so a resumed or
	// downstream node reads the person's answers the same way it reads any node's
	// output. Carried in the `op` payload; empty when the node has no key.
	Key string `json:"key,omitempty"`
	// InstanceID is the correlation id of the logical workflow instance this task
	// belongs to (the parked run's InstanceID). Closing the task starts the resume
	// run under the same InstanceID, so the whole park→resume chain — and this
	// pause in it — is reportable as one instance. See models.Process.InstanceID.
	InstanceID string `json:"instanceId,omitempty"`
	// Questions are raised during the session, not by the node — see
	// HumanTaskQuestion. Empty on a freshly recorded task.
	Questions []HumanTaskQuestion `json:"questions"`
	Messages  []HumanTaskMessage  `json:"messages"`
	Data      map[string]any      `json:"data,omitempty"`
	// Nexts is the parked node's outbound edge list, captured from the svc
	// request's Node.Next when the flow reached this HITL node. Closing the task
	// starts a fresh run entered on every one of these nodes — see
	// inflow.ResumeHumanTask — which is the whole reason a `park` can tell the
	// runtime to stop and still not lose the rest of the workflow.
	Nexts     []fuseModels.Next `json:"nexts,omitempty"`
	CreatedAt int64             `json:"createdAt"`
	UpdatedAt int64             `json:"updatedAt"`
	ClosedAt  int64             `json:"closedAt"`
}

// AllAnswered reports whether every question carries a non-empty answer. An
// empty question set is treated as answered (nothing to ask).
func (t *HumanTask) AllAnswered() bool {
	for i := range t.Questions {
		if t.Questions[i].Answer == "" {
			return false
		}
	}
	return true
}
