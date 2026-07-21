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
	Questions []HumanTaskQuestion `json:"questions"`
	Messages  []HumanTaskMessage  `json:"messages"`
	Data      map[string]any      `json:"data,omitempty"`
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
