package inflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/FloMorphic/morph-api/api/wslog"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
)

// ResumeHumanTask restarts a flow that parked at a Human-in-the-Loop node, now
// that the person is done with it.
//
// The park is a real stop: HandleHumanTask answered the runtime with a `stop`
// command, so the original run finished at the node and the engine holds nothing
// open. What it kept instead is the node's outbound edge list on the task, and
// the resume is an ordinary new run entered on every one of those nodes at once
// — the engine takes a set of entry points, so a node that fanned out to three
// branches resumes as three branches rather than the first of them.
//
// It is a no-op (nil run, nil error) for a task with nothing to resume: a
// `continue` node never stopped its flow, and a node with no outbound edges was
// the end of the line. Both are ordinary outcomes of closing a session, not
// failures.
func ResumeHumanTask(ctx context.Context, store repository.Store, task *models.HumanTask) (*models.Process, error) {
	if task == nil {
		return nil, nil
	}
	if task.Mode == models.HumanTaskContinue {
		return nil, nil
	}
	startNodeIDs := nextNodeIDs(task.Nexts)
	if len(startNodeIDs) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(task.FlowID) == "" || strings.TrimSpace(task.ContextID) == "" {
		return nil, fmt.Errorf("hitl resume: task %s has no flowId/contextId to resume (flow=%q ctx=%q)",
			task.ID, task.FlowID, task.ContextID)
	}

	// Backend-only meta: the origin tag makes the row legible in the process list
	// ("this came from task X of flow Y"), and the edge list is kept whole so the
	// row explains its own entry points, tags included.
	rec, err := StartWorkflow(ctx, store, StartParams{
		FlowID:       task.FlowID,
		ContextID:    task.ContextID,
		StartNodeIDs: startNodeIDs,
		RecordMeta: map[string]any{
			"origin":       "hitl_resume",
			"sourceFlowId": task.FlowID,
			"sourceNodeId": task.NodeID,
			"sourcePid":    task.PID,
			"humanTaskId":  task.ID,
			"nextNodes":    task.Nexts,
		},
	})
	if err != nil {
		return rec, fmt.Errorf("hitl resume: start run for task %s: %w", task.ID, err)
	}
	fmt.Printf("hitl: resumed task %s as process %d (flow=%s nodes=%v)\n",
		task.ID, rec.IndexID, task.FlowID, startNodeIDs)
	wslog.Notify(wslog.LevelSuccess, "Human task closed",
		fmt.Sprintf("Flow %s resumed at %d node(s) after %q", task.FlowID, len(startNodeIDs), task.Title))
	return rec, nil
}
