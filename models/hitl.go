package models

import fuseModels "github.com/Inflowenger/inflow-fusion/models"

// HumanTaskStatus is the lifecycle of a Human-in-the-Loop task.
//
//   - open:     created by a running workflow, awaiting human answers.
//   - answered: every question has an answer; the flow may continue.
//   - closed:   force-finished by the human; the workflow ends at this step.
type HumanTaskStatus string

const (
	HumanTaskOpen     HumanTaskStatus = "open"
	HumanTaskAnswered HumanTaskStatus = "answered"
	HumanTaskClosed   HumanTaskStatus = "closed"
)

// HumanTaskQuestion is one question the workflow posed to the human. The human
// answers each; when all are answered the task flips to `answered`.
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

// HumanTaskChannel is where the conversation with the person is held. Only
// `direct` (the in-app chat) is served today; the messenger channels are
// recorded so a flow can already declare its intent, but need a provider
// integration before a session is delivered on them.
type HumanTaskChannel string

const (
	HumanTaskDirect   HumanTaskChannel = "direct"
	HumanTaskTelegram HumanTaskChannel = "telegram"
	HumanTaskWhatsapp HumanTaskChannel = "whatsapp"
)

// HumanTaskRef is one named pointer into the run's context that the node
// declared. An extrinsic svc handler runs outside the flow's expression scope
// and cannot resolve paths at run time, so the path is recorded as written and
// resolved on read (see the hitl controller) against the Data snapshot the node
// captured. `Value` is that resolution — computed per read, never persisted.
type HumanTaskRef struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
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
	ID        string              `json:"id"`
	Title     string              `json:"title"`
	Status    HumanTaskStatus     `json:"status"`
	PID       string              `json:"pid"`
	FlowID    string              `json:"flowId"`
	NodeID    string              `json:"nodeId"`
	ContextID string              `json:"contextId"`
	// Mode / Channel / Prompt / Refs are the node's design-time session config,
	// shipped whole in the extrinsic's `op` payload because the handler cannot
	// evaluate anything against the flow at run time.
	Mode    HumanTaskMode    `json:"mode,omitempty"`
	Channel HumanTaskChannel `json:"channel,omitempty"`
	// Prompt is the conversation opener as authored, `{{$.path}}` variables
	// intact — it is a template, not text ready to show a person.
	Prompt string `json:"prompt,omitempty"`
	// PromptResolved is Prompt with those variables filled in from Data. It is
	// produced when the task is read and is deliberately NOT persisted: the
	// template plus the snapshot are the durable facts, the rendering is not.
	PromptResolved string              `json:"promptResolved,omitempty"`
	Refs           []HumanTaskRef      `json:"refs,omitempty"`
	Questions      []HumanTaskQuestion `json:"questions"`
	Messages       []HumanTaskMessage  `json:"messages"`
	Data           map[string]any      `json:"data,omitempty"`
	// Nexts is the parked node's outbound edge list, captured from the svc
	// request's Node.Next when the flow reached this HITL node. It is kept so a
	// future run can resume the flow from exactly these next nodes (the HITL
	// handler tells the runtime to stop after the node, dropping its next).
	Nexts []fuseModels.Next `json:"nexts,omitempty"`
	CreatedAt int64               `json:"createdAt"`
	UpdatedAt int64               `json:"updatedAt"`
	ClosedAt  int64               `json:"closedAt"`
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
