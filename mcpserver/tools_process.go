package mcpserver

import (
	"context"
	"strings"

	"github.com/FloMorphic/morph-api/inflow"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerProcessTools exposes the /process surface: list and get runs (to read
// what a run did and how it finished), plus the runtime actions start and stop.
// These launch/cancel real work on the inflow engine, so they need the runtime
// connected (the app serves CRUD-only when it is not).
func registerProcessTools(s *server.MCPServer, store repository.Store) {
	repo := store.Processes()

	s.AddTool(mcp.NewTool("flo_list_processes",
		withOpts(pageArgs(),
			mcp.WithDescription("List workflow runs (Process), newest first. Filter by status, flowId, pid, or instanceId."),
			mcp.WithString("status", mcp.Description("scheduled|running|waiting|finished|stopped|failed")),
			mcp.WithString("flowId", mcp.Description("only runs of this workflow")),
			mcp.WithString("pid", mcp.Description("only rows of this engine process uuid")),
			mcp.WithString("instanceId", mcp.Description("only runs of this logical instance (park→resume chain)")),
		)...,
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p := listParams(req)
		p.Status = strings.TrimSpace(req.GetString("status", ""))
		p.FlowID = strings.TrimSpace(req.GetString("flowId", ""))
		p.PID = strings.TrimSpace(req.GetString("pid", ""))
		p.InstanceID = strings.TrimSpace(req.GetString("instanceId", ""))
		items, total, err := repo.List(ctx, p)
		if err != nil {
			return repoError(err, "processes not found")
		}
		return jsonResult(map[string]any{"list": items, "total": total})
	})

	s.AddTool(mcp.NewTool("flo_get_process",
		mcp.WithDescription("Get one run by its indexId — status, error, the request sent to the engine, backend meta, and timing. Use it to inspect what happened in the last process and its results."),
		mcp.WithNumber("indexId", mcp.Required(), mcp.Description("the run's auto-increment index")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		idx, err := req.RequireInt("indexId")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("indexId is required", err), nil
		}
		rec, err := repo.GetByIndex(ctx, int64(idx))
		if err != nil {
			return repoError(err, "process not found")
		}
		return jsonResult(rec)
	})

	s.AddTool(mcp.NewTool("flo_start_process",
		mcp.WithDescription("Launch a workflow run on the inflow engine (or record it as scheduled when scheduledAt is a future epoch-millis). Requires the inflow runtime to be connected."),
		mcp.WithString("flowId", mcp.Required(), mcp.Description("workflow id to run")),
		mcp.WithString("contextId", mcp.Required(), mcp.Description("context document the run reads/writes")),
		mcp.WithString("startNodeId", mcp.Description("pin the entry node; empty runs the flow's own start node")),
		mcp.WithObject("meta", mcp.Description("optional backend-only record meta kept on the row")),
		mcp.WithNumber("scheduledAt", mcp.Description("epoch millis to launch at; omit/0 runs immediately")),
		mcp.WithNumber("executeTimeoutSec", mcp.Description("override: whole-run timeout (seconds)")),
		mcp.WithNumber("processNodeLimit", mcp.Description("override: max node visits (runaway-loop guard)")),
		mcp.WithNumber("requestTimeoutSec", mcp.Description("override: fallback per-request timeout (seconds)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		flowID, err := req.RequireString("flowId")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("flowId is required", err), nil
		}
		contextID, err := req.RequireString("contextId")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("contextId is required", err), nil
		}
		meta, err := argObject(req, "meta")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("invalid meta", err), nil
		}
		rec, err := inflow.StartWorkflow(ctx, store, inflow.StartParams{
			FlowID:       flowID,
			ContextID:    contextID,
			StartNodeIDs: []string{req.GetString("startNodeId", "")},
			RecordMeta:   meta,
			ScheduledAt:  int64(req.GetInt("scheduledAt", 0)),
			Settings: inflow.RunSettings{
				ExecuteTimeoutSec: int64(req.GetInt("executeTimeoutSec", 0)),
				ProcessNodeLimit:  uint16(req.GetInt("processNodeLimit", 0)),
				RequestTimeoutSec: int64(req.GetInt("requestTimeoutSec", 0)),
			},
		})
		if err != nil {
			// A launch that recorded a `failed` row still returns it, so surface
			// both the row and the error the way the REST handler does.
			if rec != nil {
				return jsonResult(map[string]any{"process": rec, "error": err.Error()})
			}
			return mcp.NewToolResultErrorFromErr("launch failed", err), nil
		}
		return jsonResult(rec)
	})

	s.AddTool(mcp.NewTool("flo_stop_process",
		mcp.WithDescription("Ask the engine to stop a running run and mark the row stopped."),
		mcp.WithNumber("indexId", mcp.Required(), mcp.Description("the run's index to stop")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		idx, err := req.RequireInt("indexId")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("indexId is required", err), nil
		}
		rec, err := inflow.StopWorkflow(ctx, store, int64(idx))
		if err != nil {
			return repoError(err, "process not found")
		}
		return jsonResult(rec)
	})

	s.AddTool(mcp.NewTool("flo_delete_process",
		mcp.WithDescription("Delete a run row by its indexId."),
		mcp.WithNumber("indexId", mcp.Required(), mcp.Description("the run's index")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		idx, err := req.RequireInt("indexId")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("indexId is required", err), nil
		}
		if err := repo.Delete(ctx, int64(idx)); err != nil {
			return repoError(err, "process not found")
		}
		return jsonResult(map[string]any{"indexId": idx, "deleted": true})
	})
}
