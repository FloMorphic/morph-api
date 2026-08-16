package inflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/FloMorphic/morph-api/repository"
	inflowModels "github.com/Inflowenger/inflow-fusion/models"
	svcHandler "github.com/Inflowenger/inflow-fusion/svcHandler"
	"github.com/bytedance/sonic"
	"github.com/nats-io/nats.go"
)

// continuePayload is the compile-time `op` the Continue After node carries (set
// in compiler.go's NODE_UNTIL case as ExtrinsicRule.Data). The two schedule
// shapes are already collapsed by the compiler: an absolute `at` becomes
// ContinueAt (epoch millis), and a relative delay becomes DelaySeconds. Exactly
// one is non-zero for a well-formed node.
type continuePayload struct {
	Mode         string `json:"mode"`
	At           string `json:"at"`
	ContinueAt   int64  `json:"continueAt"`   // absolute epoch millis (mode "at")
	DelaySeconds int64  `json:"delaySeconds"` // relative seconds (delay modes)
}

// HandleContinueAfter parks the current run at a Continue After node and records
// a scheduled process that resumes its outbound nodes at the computed time.
//
// The schedule is resolved to a single epoch-millis: an absolute ContinueAt is
// used as-is; a relative DelaySeconds is added to now (the moment the node is
// reached at run time). The parked node's outbound edges are read from the
// request body's Node.Next — the resumed run starts from them — and an origin
// tag is kept on the scheduled row's Meta so the process list shows which flow /
// node it came from. Finally we reply with a `stop` command so the live run
// parks here rather than continuing inline.
func HandleContinueAfter(store repository.Store, header nats.Header, data []byte) ([]byte, error) {
	body := parseExtRequest(data)
	op := decodeContinueOp(body.OperationData)

	// Identity travels on the runtime headers (as it does for HITL); the nodeId
	// comes off the request body's Node, which the runtime always supplies.
	flowID := header.Get("flowId")
	contextID := header.Get("contextId")
	pid := header.Get("pid")
	// The logical-instance correlation id of the run reaching this node (set by
	// StartWorkflow in the request meta). The scheduled resume continues the same
	// instance, so the whole park→resume chain shares one id — mirrors HITL. Falls
	// back to the pid, which is what a fresh instance's id is anyway.
	instanceID := header.Get("instanceId")
	if strings.TrimSpace(instanceID) == "" {
		instanceID = pid
	}
	nodeID := ""
	if body.Node != nil {
		nodeID = body.Node.ID
	}

	// Collapse the schedule to an absolute epoch-millis. An absolute ContinueAt
	// wins; otherwise it is `now + delaySeconds`.
	scheduledAt := op.ContinueAt
	if scheduledAt <= 0 {
		scheduledAt = nowMillis() + op.DelaySeconds*1000
	}

	// The outbound nodes captured for the resume — the resumed run starts from
	// every one of them, so a Continue After that fanned out resumes as the
	// branches it actually had.
	var nexts []inflowModels.Next
	if body.Node != nil {
		nexts = body.Node.Next
	}
	startNodeIDs := nextNodeIDs(nexts)

	if strings.TrimSpace(flowID) == "" || strings.TrimSpace(contextID) == "" {
		return nil, fmt.Errorf("continue.at: missing flowId/contextId on request (flow=%q ctx=%q)", flowID, contextID)
	}

	// Backend-only meta kept on the scheduled row: the origin tag makes the row
	// legible in the process list ("this came from flow X's Continue After node"),
	// and nextNodes is what a resume rebuilds its start from.
	recordMeta := map[string]any{
		"origin":       "continue_after",
		"sourceFlowId": flowID,
		"sourceNodeId": nodeID,
		"sourcePid":    pid,
		"mode":         op.Mode,
		"scheduledAt":  scheduledAt,
		"nextNodes":    nexts,
	}

	rec, err := StartWorkflow(context.Background(), store, StartParams{
		FlowID:       flowID,
		ContextID:    contextID,
		StartNodeIDs: startNodeIDs,
		ScheduledAt:  scheduledAt,
		RecordMeta:   recordMeta,
		// Continue the same logical instance so the scheduled resume ties back to
		// the run that parked here (each run still keeps its own pid).
		InstanceID: instanceID,
		// A Continue After resumes the same context after the delay: seed the
		// parked run's traversal state so a join past this node does not lock.
		// Resume Fill in updateDocContext message
		Resume: nil,
	})
	if err != nil {
		return nil, fmt.Errorf("continue.at: schedule resume: %w", err)
	}
	fmt.Printf("continue.at: scheduled process %d (flow=%s node=%s) at %d with %d next(s)\n",
		rec.IndexID, flowID, nodeID, scheduledAt, len(nexts))

	// Stop the live run here; the scheduled row resumes the flow later.
	return svcHandler.StopHereResponse(map[string]any{
		"indexId":     rec.IndexID,
		"status":      rec.Status,
		"scheduledAt": scheduledAt,
	})
}

// parseExtRequest unmarshals the extrinsic svc request envelope (data / op /
// node). A malformed body yields a zero envelope so the handler can still fail
// with a clear message rather than a decode panic.
func parseExtRequest(data []byte) inflowModels.ExtSvcRequestBody {
	var body inflowModels.ExtSvcRequestBody
	_ = sonic.Unmarshal(data, &body)
	return body
}

// decodeContinueOp re-decodes the envelope's `op` map into the typed payload.
func decodeContinueOp(op map[string]any) continuePayload {
	var out continuePayload
	if len(op) == 0 {
		return out
	}
	if b, err := sonic.Marshal(op); err == nil {
		_ = sonic.Unmarshal(b, &out)
	}
	return out
}
