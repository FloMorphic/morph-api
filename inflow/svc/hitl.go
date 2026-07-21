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

// hitlPayload is the compile-time `op` payload the HITL node carries (set in the
// compiler's NODE_HITL case as ExtrinsicRule.Data): the node's title/questions
// and its nodeId. The runtime delivers it under the request envelope's `op`
// field. Fields are read defensively — any that are absent stay empty.
type hitlPayload struct {
	Title     string `json:"title"`
	PID       string `json:"pid"`
	FlowID    string `json:"flowId"`
	NodeID    string `json:"nodeId"`
	ContextID string `json:"contextId"`
	// Questions accepts either ["ask?", ...] or [{"id","text"}, ...].
	Questions []questionInput `json:"questions"`
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

// HandleHumanTask persists (upserts) the task for a HITL node execution, captures
// the node's outbound edges for a future resume, and replies with a `stop`
// command so the runtime parks the flow at this node.
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
		Questions: questions,
		// The main scoped data at the moment the flow parked, kept for traceability.
		Data:  scopedDataMap(body.Data),
		Nexts: nexts,
	}
	if err := store.HumanTasks().Upsert(context.Background(), task); err != nil {
		return nil, fmt.Errorf("record human task: %w", err)
	}
	fmt.Printf("hitl: recorded task %s (pid=%s flow=%s node=%s) with %d question(s), %d next(s)\n",
		task.ID, pid, flowID, nodeID, len(questions), len(nexts))

	// Reply with a stop command: the runtime clears this node's next and does not
	// continue — the flow resumes later from the captured nexts.
	return svcHandler.StopHereResponse(map[string]any{"taskId": task.ID, "status": task.Status})
}

func hitlTaskID(pid, nodeID string) string {
	if pid != "" && nodeID != "" {
		return fmt.Sprintf("%s_%s_%s", repository.HumanTaskIDPrefix, pid, nodeID)
	}
	return "" // let the repository assign one
}
