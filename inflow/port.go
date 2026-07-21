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
// HitlSubject the subject it answers on. The nodeId travels in the request body
// (see compiler.go's hitl case) so the handler can recover the node.
const (
	SvcHitl     = "hitl"
	HitlSubject = "svc.hitl.add"

	// Store + scheduling service names/subjects. inflo-fusion owns the real
	// document/vector read-write; morph-api registers ack-only stubs so flows
	// that use these nodes don't hang when the store service is absent.
	SvcStoreDoc     = "store_doc"
	StoreDocSubject = "svc.store.doc.*"
	SvcStoreVec     = "store_vec"
	StoreVecSubject = "svc.store.vec.*"
	SvcContinueAt   = "continue_at"
	ContinueSubj    = "svc.continue.at"
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

	// Document store service. A `read` runs a validated, read-only SQL query
	// against the store's database; a `write` stores the request's JSON object
	// as a document. The concrete action is the last subject token
	// (svc.store.doc.{read,write}).
	if err := svcHandler.ImplHandlerOnSubject(SvcStoreDoc, svcHandler.SvcTopic(StoreDocSubject), func(header nats.Header, data []byte) ([]byte, error) {
		return handleDocStore(store, header, data)
	}); err != nil {
		return fmt.Errorf("failed to create %s service node : %v", SvcStoreDoc, err)
	}
	fmt.Println("New SVC handler registered On  ", svcHandler.SvcTopic(StoreDocSubject).ConvertToSubscribe())

	// Vector store service. Similarity search/index is genuinely different from
	// SQL and is delegated to inflo-fusion; keep an ack-only stub so a flow using
	// a Vector Store node completes rather than timing out.
	// TODO: implement real vector search / index (delegated to inflo-fusion).
	if err := svcHandler.ImplHandlerOnSubject(SvcStoreVec, svcHandler.SvcTopic(StoreVecSubject), func(header nats.Header, data []byte) ([]byte, error) {
		subject := header.Get("recv_subject")
		fmt.Printf("vector store svc stub: %s data=%s\n", subject, string(data))
		return []byte(`{"status":"accepted","items":[]}`), nil
	}); err != nil {
		return fmt.Errorf("failed to create %s service node : %v", SvcStoreVec, err)
	}
	fmt.Println("New SVC handler registered On  ", svcHandler.SvcTopic(StoreVecSubject).ConvertToSubscribe())

	// Continue-After: park-and-resume at a scheduled time. Ack-only stub for now.
	// TODO: record a scheduled process (StartWorkflow with ScheduledAt) whose
	// virtual start node resumes the captured `nextNodes`.
	if err := svcHandler.ImplHandlerOnSubject(SvcContinueAt, svcHandler.SvcTopic(ContinueSubj), func(header nats.Header, data []byte) ([]byte, error) {
		fmt.Printf("continue.at svc stub: data=%s\n", string(data))
		return []byte(`{"status":"scheduled"}`), nil
	}); err != nil {
		return fmt.Errorf("failed to create continue service node : %v", err)
	}
	fmt.Println("New SVC handler registered On  ", svcHandler.SvcTopic(ContinueSubj).ConvertToSubscribe())

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

// storeRequest is the payload delivered to a store svc subject: the store the
// node references plus the action-specific fields the compiler carried
// (`query` for a read, `input` for a write).
type storeRequest struct {
	Action  string `json:"action"`
	StoreID string `json:"storeId"`
	Query   string `json:"query"`
	Input   any    `json:"input"`
	Scope   string `json:"scope"`
	Key     string `json:"key"`
}

// handleDocStore serves svc.store.doc.{read,write}. It resolves the referenced
// document store and dispatches: a read runs a validated, read-only SQL query;
// a write stores the request's JSON object as a document. A rejected or failing
// request returns an error so the run fails visibly rather than looking like an
// empty success.
func handleDocStore(store repository.Store, header nats.Header, data []byte) ([]byte, error) {
	var req storeRequest
	_ = sonic.Unmarshal(data, &req)

	action := strings.ToLower(strings.TrimSpace(actionFromSubject(header, req.Action)))
	if strings.TrimSpace(req.StoreID) == "" {
		return nil, fmt.Errorf("doc store %s: request has no storeId", action)
	}
	// The store is resolved server-side; it — not the request — decides which
	// database/table is touched.
	rec, err := store.Memory().GetByID(context.Background(), req.StoreID)
	if err != nil {
		return nil, fmt.Errorf("doc store %s: store %q not found: %w", action, req.StoreID, err)
	}
	if rec.Type != models.MemoryDocument || rec.Document == nil {
		return nil, fmt.Errorf("doc store %s: store %q is not a document store", action, req.StoreID)
	}

	switch action {
	case "read":
		safe, err := models.ValidateReadSQL(req.Query)
		if err != nil {
			// A rejected query is a hard failure — never fall through to a read.
			return nil, fmt.Errorf("doc store read rejected: %w", err)
		}
		rows, err := store.Memory().RunReadQuery(context.Background(), rec, safe)
		if err != nil {
			return nil, fmt.Errorf("doc store read: %w", err)
		}
		resp, _ := sonic.Marshal(map[string]any{"status": "ok", "count": len(rows), "items": rows})
		return resp, nil
	case "write":
		id, err := store.Memory().WriteDocument(context.Background(), rec, documentPayload(req, data))
		if err != nil {
			return nil, fmt.Errorf("doc store write: %w", err)
		}
		resp, _ := sonic.Marshal(map[string]any{"status": "ok", "id": id})
		return resp, nil
	default:
		return nil, fmt.Errorf("doc store: unsupported action %q", action)
	}
}

// documentPayload derives the JSON object a write should store. When the engine
// has resolved the node's `input` JSONPath to an object, that object is the
// document; otherwise the request body (minus the control fields the compiler
// adds) is stored, so a runtime value is never silently dropped.
func documentPayload(req storeRequest, raw []byte) map[string]any {
	if obj, ok := req.Input.(map[string]any); ok && len(obj) > 0 {
		return obj
	}
	var body map[string]any
	if err := sonic.Unmarshal(raw, &body); err != nil || body == nil {
		return map[string]any{}
	}
	for _, k := range []string{"action", "storeId", "query", "input", "scope", "key"} {
		delete(body, k)
	}
	return body
}

// actionFromSubject recovers the store action from the concrete subject the
// message arrived on (svc.store.doc.<action>), falling back to the body value.
func actionFromSubject(header nats.Header, fallback string) string {
	if subject := header.Get("recv_subject"); subject != "" {
		if parts := strings.Split(subject, "."); len(parts) > 0 {
			if last := parts[len(parts)-1]; last != "" && last != "*" {
				return last
			}
		}
	}
	return fallback
}
