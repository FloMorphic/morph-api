package svc

import (
	"context"
	"fmt"
	"strings"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	inflowModels "github.com/Inflowenger/inflow-fusion/models"
	svcHandler "github.com/Inflowenger/inflow-fusion/svcHandler"
	"github.com/bytedance/sonic"
	"github.com/nats-io/nats.go"
)

// hitlPayload is the compile-time `op` payload the HITL node carries (set by
// buildHitlNode as ExtrinsicRule.Data): the node's whole session config plus its
// nodeId. The runtime delivers it under the request envelope's `op` field.
// Fields are read defensively — any that are absent stay empty.
//
// Note what is deliberately NOT resolved here. This handler runs outside the
// flow's expression scope, so Prompt arrives as a template with its
// `{{$.path}}` variables intact and Refs as bare paths; both are recorded as-is
// and rendered later, when a person opens the session, against the Data
// snapshot captured below.
type hitlPayload struct {
	Title     string `json:"title"`
	PID       string `json:"pid"`
	FlowID    string `json:"flowId"`
	NodeID    string `json:"nodeId"`
	ContextID string `json:"contextId"`
	// Mode is "park" (reply with a stop command; the run finishes here) or
	// "continue" (reply plainly; the flow carries on through this node's next).
	Mode string `json:"mode"`
	// Channel is where the conversation is held: direct / telegram / whatsapp.
	Channel string     `json:"channel"`
	Prompt  string     `json:"prompt"`
	Refs    []refInput `json:"refs"`
	// Questions accepts either ["ask?", ...] or [{"id","text"}, ...].
	Questions []questionInput `json:"questions"`
}

// refInput is one declared context pointer. The node editor also stamps a local
// `id` on each row; it is a canvas concern and not read here.
type refInput struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// questionInput unmarshals a question given either as a bare string or an object.
type questionInput struct {
	ID   string
	Text string
}

func (q *questionInput) UnmarshalJSON(b []byte) error {
	var s string
	if err := sonic.Unmarshal(b, &s); err == nil {
		q.Text = s
		return nil
	}
	var obj struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	if err := sonic.Unmarshal(b, &obj); err != nil {
		return err
	}
	q.ID, q.Text = obj.ID, obj.Text
	return nil
}

// HandleHumanTask persists (upserts) the task for a HITL node execution and
// captures the node's outbound edges for a future resume.
//
// What it answers the runtime with is the node's `mode`:
//
//   - park (default): a `stop` command. The runtime drops this node's next and
//     the run finishes here; the flow resumes later from the captured nexts.
//   - continue: a plain success reply with no command, so the runtime carries
//     straight on through this node's next. The task is still recorded — a
//     person picks it up out of band rather than the run waiting on them.
func HandleHumanTask(store repository.Store, header nats.Header, data []byte) ([]byte, error) {
	// inflow-fusion delivers every extrinsic svc request as an envelope: the
	// flow's live scoped `Data`, the compile-time `op` payload we set on the node
	// (ExtrinsicRule.Data — here the title/questions/nodeId), and the full `Node`
	// whose Next edges let a parked flow resume. A malformed body is not fatal —
	// we still record what we can.
	body := parseRequest(data)

	req := decodeOp[hitlPayload](body.OperationData)

	// Prefer explicit header values (set by the runtime) over the payload.
	pid := firstNonEmpty(header.Get("pid"), req.PID)
	flowID := firstNonEmpty(header.Get("flowId"), req.FlowID)
	contextID := firstNonEmpty(header.Get("contextId"), req.ContextID)
	// nodeId comes from the node model first, then the payload, then the concrete
	// subject the message arrived on.
	nodeID := req.NodeID
	if body.Node != nil && body.Node.ID != "" {
		nodeID = body.Node.ID
	}
	if subject := header.Get("recv_subject"); subject != "" {
		if parts := strings.Split(subject, "."); len(parts) > 0 {
			if last := parts[len(parts)-1]; last != "" && nodeID == "" {
				nodeID = last
			}
		}
	}

	title := req.Title
	if strings.TrimSpace(title) == "" {
		title = "Human task"
	}

	questions := make([]models.HumanTaskQuestion, 0, len(req.Questions))
	for i, q := range req.Questions {
		id := q.ID
		if strings.TrimSpace(id) == "" {
			id = fmt.Sprintf("q%d", i+1)
		}
		questions = append(questions, models.HumanTaskQuestion{ID: id, Text: q.Text})
	}

	// The declared context pointers, kept as written. A ref with no path points
	// nowhere and is dropped; an unnamed one is labelled by its position so the
	// session still has something to show it under.
	refs := make([]models.HumanTaskRef, 0, len(req.Refs))
	for i, r := range req.Refs {
		path := strings.TrimSpace(r.Path)
		if path == "" {
			continue
		}
		name := strings.TrimSpace(r.Name)
		if name == "" {
			name = fmt.Sprintf("ref%d", i+1)
		}
		refs = append(refs, models.HumanTaskRef{Name: name, Path: path})
	}

	mode := models.HumanTaskMode(req.Mode)
	if mode != models.HumanTaskContinue {
		mode = models.HumanTaskPark
	}
	channel := models.HumanTaskChannel(req.Channel)
	switch channel {
	case models.HumanTaskTelegram, models.HumanTaskWhatsapp:
	default:
		channel = models.HumanTaskDirect
	}

	// Capture the node's outbound edges so a future run can resume the flow from
	// exactly these nexts (we tell the runtime to stop after this node below).
	var nexts []inflowModels.Next
	if body.Node != nil {
		nexts = body.Node.Next
	}

	task := &models.HumanTask{
		// Deterministic id per (process, node) so a node re-entry updates the
		// same task instead of creating a duplicate. Falls back to a generated
		// id when the runtime did not supply a pid.
		ID:        hitlTaskID(pid, nodeID),
		Title:     title,
		Status:    models.HumanTaskOpen,
		PID:       pid,
		FlowID:    flowID,
		NodeID:    nodeID,
		ContextID: contextID,
		Mode:      mode,
		Channel:   channel,
		// The prompt template, unresolved on purpose (see hitlPayload).
		Prompt:    req.Prompt,
		Refs:      refs,
		Questions: questions,
		// The main scoped data at the moment the flow parked. It is both the
		// traceability record and what the prompt/refs are resolved against when
		// the session opens, so a node whose `scope` is narrower than the paths
		// its prompt uses will render them as empty.
		Data:  scopedDataMap(body.Data),
		Nexts: nexts,
	}
	if err := store.HumanTasks().Upsert(context.Background(), task); err != nil {
		return nil, fmt.Errorf("record human task: %w", err)
	}
	fmt.Printf("hitl: recorded task %s mode=%s channel=%s (pid=%s flow=%s node=%s) with %d question(s), %d ref(s), %d next(s)\n",
		task.ID, mode, channel, pid, flowID, nodeID, len(questions), len(refs), len(nexts))

	reply := map[string]any{"taskId": task.ID, "status": task.Status, "mode": string(mode)}
	if mode == models.HumanTaskContinue {
		// No command: the runtime treats this as an ordinary successful extrinsic
		// and continues into this node's next.
		return sonic.Marshal(reply)
	}
	// Park: the runtime clears this node's next and does not continue — the flow
	// resumes later from the captured nexts.
	return svcHandler.StopHereResponse(reply)
}

func hitlTaskID(pid, nodeID string) string {
	if pid != "" && nodeID != "" {
		return fmt.Sprintf("%s_%s_%s", repository.HumanTaskIDPrefix, pid, nodeID)
	}
	return "" // let the repository assign one
}
