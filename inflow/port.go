package inflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/FloMorphic/morph-api/env"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	fuse "github.com/Inflowenger/inflow-fusion/inflow"
	svcHandler "github.com/Inflowenger/inflow-fusion/svcHandler"
	"github.com/bytedance/sonic"
	"github.com/nats-io/nats.go"
)

// SvcHitl is the logical service name of the Human-in-the-Loop handler, and
// HitlSubject the subject it answers on. `{nodeId}` is filled at compile time
// (see compiler.go) so the handler can recover the node from the concrete
// subject it arrived on.
const (
	SvcHitl     = "hitl"
	HitlSubject = "svc.hitl.task.{nodeId}"
)

// InitInflowConnection connects to the inflow runtime, wiring the backend
// contract to the given repository store.
func InitInflowConnection(store repository.Store) error {
	return fuse.InitBackend(
		fuse.WithImplementedBackendBy(&InflowWire{store: store}),
		fuse.WithJwtSecretKey(env.GetInfraJWTSecret()), // env INFLOW_INFRA_JWT_SECRET
		fuse.WithInfraApi(env.GetInfraApiUrl()),        // env INFLOW_INFRA_API
	)

}

// LoadSvcNodehandlers registers this backend's extrinsic svc handlers. They need
// the store so a running workflow's request can be persisted.
func LoadSvcNodehandlers(store repository.Store) error {
	svc_sub1 := "svc.add.issue.{TABLE_NAME}"
	err := svcHandler.ImplHandlerOnSubject("exports_db", svcHandler.SvcTopic(svc_sub1), func(header nats.Header, data []byte) ([]byte, error) {
		subject := header.Get("recv_subject")
		fmt.Printf("recieved Message On Subject %s with data %s\n", subject, string(data))
		table := strings.Split(subject, ".")[3]
		return []byte(fmt.Sprintf(`{"status":"saved successfully on %s table"}`, table)), nil
	})
	if err != nil {
		return fmt.Errorf("failed to create service node : %v", err)
	}
	fmt.Println("New SVC handler registered On  ", svcHandler.SvcTopic(svc_sub1).ConvertToSubscribe())

	// Human-in-the-Loop: when a workflow reaches a `humanInLoop` node the engine
	// publishes here. We record the task (pid / flowId / nodeId / contextId +
	// questions) and immediately ack — the flow's blocking/resume is handled by
	// the inflow runtime based on the node data.
	if err := svcHandler.ImplHandlerOnSubject(SvcHitl, svcHandler.SvcTopic(HitlSubject), func(header nats.Header, data []byte) ([]byte, error) {
		return handleHumanTask(store, header, data)
	}); err != nil {
		return fmt.Errorf("failed to create hitl service node : %v", err)
	}
	fmt.Println("New SVC handler registered On  ", svcHandler.SvcTopic(HitlSubject).ConvertToSubscribe())

	// Subscribe to the engine's event log so finished runs close their process
	// rows. Non-fatal: the CRUD + launch paths keep working without it.
	if err := SubscribeProcessEvents(store); err != nil {
		fmt.Printf("warning: process events not subscribed: %v\n", err)
	}
	return nil
}

// hitlRequest is the request payload the engine delivers to the HITL subject:
// the node's static `data` (title/questions) plus the process fields the runtime
// merges in. Fields are read defensively — any that are absent stay empty.
type hitlRequest struct {
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

// handleHumanTask persists (upserts) the task for a HITL node execution and acks.
func handleHumanTask(store repository.Store, header nats.Header, data []byte) ([]byte, error) {
	var req hitlRequest
	// A malformed body is not fatal — we still record what we can.
	_ = sonic.Unmarshal(data, &req)

	// Prefer explicit header values (set by the runtime) over the body.
	pid := firstNonEmpty(header.Get("pid"), req.PID)
	flowID := firstNonEmpty(header.Get("flowId"), req.FlowID)
	contextID := firstNonEmpty(header.Get("contextId"), req.ContextID)
	// nodeId is recoverable from the concrete subject the message arrived on.
	nodeID := req.NodeID
	if subject := header.Get("recv_subject"); subject != "" {
		if parts := strings.Split(subject, "."); len(parts) > 0 {
			nodeID = parts[len(parts)-1]
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

	// Keep the raw request body as a data snapshot for traceability.
	var raw map[string]any
	_ = sonic.Unmarshal(data, &raw)

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
		Data:      raw,
	}
	if err := store.HumanTasks().Upsert(context.Background(), task); err != nil {
		return nil, fmt.Errorf("record human task: %w", err)
	}
	fmt.Printf("hitl: recorded task %s (pid=%s flow=%s node=%s) with %d question(s)\n",
		task.ID, pid, flowID, nodeID, len(questions))

	resp, _ := sonic.Marshal(map[string]any{"taskId": task.ID, "status": task.Status})
	return resp, nil
}

func hitlTaskID(pid, nodeID string) string {
	if pid != "" && nodeID != "" {
		return fmt.Sprintf("%s_%s_%s", repository.HumanTaskIDPrefix, pid, nodeID)
	}
	return "" // let the repository assign one
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
