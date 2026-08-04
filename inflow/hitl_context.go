package inflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
)

// WriteHumanTaskContext binds a closed session's outcome into the run's context
// under the HITL node's result key, so the flow that resumes (or any later node)
// reads the person's answers exactly the way it reads any node's output —
// `{{$.<key>}}`.
//
// It is the counterpart to the park: the run stopped at this node without ever
// producing a normal result binding (the handler answered with a stop command),
// so the conversation's result has to be written back by hand at the moment it
// is settled — on close, just before ResumeHumanTask starts the next run on the
// same context.
//
// It is a no-op (nil error) when there is nothing to bind: a node with no result
// key, or a task with no context to write into. A missing or unparseable context
// is reported as an error but is not, on its own, fatal to closing a task — the
// caller decides how loud to be.
func WriteHumanTaskContext(ctx context.Context, store repository.Store, task *models.HumanTask) error {
	if task == nil {
		return nil
	}
	key := strings.TrimSpace(task.Key)
	ctxID := strings.TrimSpace(task.ContextID)
	if key == "" || ctxID == "" {
		return nil
	}

	rec, err := store.Contexts().GetByID(ctx, ctxID)
	if err != nil {
		return fmt.Errorf("hitl context: load context %s: %w", ctxID, err)
	}

	// The context document is a JSON object serialized as a string. Parse it into
	// a map so we can set our key without disturbing the rest; an empty document
	// starts as an empty object. A document that is present but not a JSON object
	// is left untouched — we will not clobber data we cannot safely merge into.
	doc := map[string]any{}
	if s := strings.TrimSpace(rec.Context); s != "" {
		if err := json.Unmarshal([]byte(s), &doc); err != nil {
			return fmt.Errorf("hitl context: context %s is not a JSON object, leaving it unchanged: %w", ctxID, err)
		}
	}

	doc[key] = humanTaskOutcome(task)

	merged, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("hitl context: marshal context %s: %w", ctxID, err)
	}
	rec.Context = string(merged)
	// A flow-driven change, attributed to the node that produced it.
	rec.UpdatedBy = models.LastChange{By: models.ByFlow, Address: task.NodeID}
	if err := store.Contexts().Upsert(ctx, rec); err != nil {
		return fmt.Errorf("hitl context: save context %s: %w", ctxID, err)
	}
	return nil
}

// humanTaskOutcome is the value bound under the node's key: the structured
// answers, the full transcript, and a flat text rendering so a downstream prompt
// can drop it in with `{{$.<key>.text}}` without walking the shape.
func humanTaskOutcome(task *models.HumanTask) map[string]any {
	answers := make([]map[string]any, 0, len(task.Questions))
	for _, q := range task.Questions {
		answers = append(answers, map[string]any{
			"question": q.Text,
			"answer":   q.Answer,
		})
	}
	return map[string]any{
		"status":    string(task.Status),
		"answers":   answers,
		"questions": task.Questions,
		"messages":  task.Messages,
		"text":      transcript(task),
		"closedAt":  task.ClosedAt,
	}
}

// transcript renders the conversation as a plain, readable block.
func transcript(task *models.HumanTask) string {
	var b strings.Builder
	for _, m := range task.Messages {
		label := m.Role
		switch m.Role {
		case "human":
			label = "User"
		case "assistant":
			label = "Assistant"
		case "system":
			label = "System"
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(label)
		b.WriteString(": ")
		b.WriteString(m.Text)
	}
	return b.String()
}
