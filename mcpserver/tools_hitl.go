package mcpserver

import (
	"context"
	"strings"

	"github.com/FloMorphic/morph-api/inflow"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerHumanTaskTools exposes the /hitl surface. Tasks are created only by a
// running workflow reaching a humanInLoop node, so there is no create tool —
// only list/get, the answer/message actions, and close (which, for a parked
// flow, resumes it: the one action with a consequence beyond the record).
func registerHumanTaskTools(s *server.MCPServer, store repository.Store) {
	repo := store.HumanTasks()

	s.AddTool(mcp.NewTool("flo_list_human_tasks",
		withOpts(pageArgs(),
			mcp.WithDescription("List Human-in-the-Loop tasks, newest first. Filter by status."),
			mcp.WithString("status", mcp.Description("open|answered|closed")),
		)...,
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p := listParams(req)
		p.Status = strings.TrimSpace(req.GetString("status", ""))
		items, total, err := repo.List(ctx, p)
		if err != nil {
			return repoError(err, "human tasks not found")
		}
		return jsonResult(map[string]any{"list": items, "total": total})
	})

	s.AddTool(mcp.NewTool("flo_get_human_task",
		mcp.WithDescription("Get one human task by id — its prompt, questions, chat thread, and the run it belongs to."),
		mcp.WithString("id", mcp.Required(), mcp.Description("human task id (hitl_…)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("id is required", err), nil
		}
		rec, err := repo.GetByID(ctx, id)
		if err != nil {
			return repoError(err, "human task not found")
		}
		return jsonResult(rec)
	})

	s.AddTool(mcp.NewTool("flo_answer_human_task",
		mcp.WithDescription("Record an answer to one of a task's questions. The task flips to `answered` once every question has an answer."),
		mcp.WithString("id", mcp.Required(), mcp.Description("human task id")),
		mcp.WithString("questionId", mcp.Required(), mcp.Description("id of the question being answered")),
		mcp.WithString("answer", mcp.Required(), mcp.Description("the answer text")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("id is required", err), nil
		}
		qid, err := req.RequireString("questionId")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("questionId is required", err), nil
		}
		answer := req.GetString("answer", "")
		rec, err := repo.Answer(ctx, id, qid, answer)
		if err != nil {
			return repoError(err, "human task or question not found")
		}
		return jsonResult(rec)
	})

	s.AddTool(mcp.NewTool("flo_message_human_task",
		mcp.WithDescription("Append one message turn to a task's chat thread (no bot reply is generated)."),
		mcp.WithString("id", mcp.Required(), mcp.Description("human task id")),
		mcp.WithString("text", mcp.Required(), mcp.Description("the message text")),
		mcp.WithString("role", mcp.Description("human|assistant|system (default human)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("id is required", err), nil
		}
		text, err := req.RequireString("text")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("text is required", err), nil
		}
		role := strings.TrimSpace(req.GetString("role", "human"))
		if role == "" {
			role = "human"
		}
		rec, err := repo.AppendMessage(ctx, id, models.HumanTaskMessage{Role: role, Text: text})
		if err != nil {
			return repoError(err, "human task not found")
		}
		return jsonResult(rec)
	})

	s.AddTool(mcp.NewTool("flo_close_human_task",
		mcp.WithDescription("Close a human task. For a task that PARKED its flow this resumes the workflow from the captured next nodes (binding the session outcome into the run's context first). Closing is idempotent; an already-closed task is not resumed again."),
		mcp.WithString("id", mcp.Required(), mcp.Description("human task id")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("id is required", err), nil
		}
		before, err := repo.GetByID(ctx, id)
		if err != nil {
			return repoError(err, "human task not found")
		}
		alreadyClosed := before.Status == models.HumanTaskClosed

		rec, err := repo.Close(ctx, id)
		if err != nil {
			return repoError(err, "human task not found")
		}
		if alreadyClosed {
			return jsonResult(rec)
		}
		// Mirror the REST close (hitl/crud.go): bind the outcome into the run's
		// context, then resume. A failure at either step still leaves the task
		// closed, so report it but hand back the task.
		if err := inflow.WriteHumanTaskContext(ctx, store, rec); err != nil {
			return jsonResult(map[string]any{"task": rec, "error": err.Error()})
		}
		if _, err := inflow.ResumeHumanTask(ctx, store, rec); err != nil {
			return jsonResult(map[string]any{"task": rec, "error": err.Error()})
		}
		return jsonResult(rec)
	})

	s.AddTool(mcp.NewTool("flo_delete_human_task",
		mcp.WithDescription("Delete a human task by id."),
		mcp.WithString("id", mcp.Required(), mcp.Description("human task id")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("id is required", err), nil
		}
		if err := repo.Delete(ctx, id); err != nil {
			return repoError(err, "human task not found")
		}
		return jsonResult(map[string]any{"id": id, "deleted": true})
	})
}
