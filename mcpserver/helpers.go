package mcpserver

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/mark3labs/mcp-go/mcp"
)

// jsonResult marshals v to indented JSON and returns it as the tool's text
// result — the same structs the REST layer returns, so an MCP client sees the
// identical wire shape a web-app call would. A marshal failure (should not
// happen for the repository types) becomes a tool error.
func jsonResult(v any) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultErrorFromErr("failed to encode result", err), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}

// repoError maps a repository error to a tool result. A not-found is a clean
// tool error (the model can react to it); anything else is surfaced verbatim.
// Both are returned as CallToolResult errors rather than Go errors so the model
// receives them in-band instead of the call failing at the protocol level.
func repoError(err error, notFoundMsg string) (*mcp.CallToolResult, error) {
	if repository.IsNotFound(err) {
		return mcp.NewToolResultError(notFoundMsg), nil
	}
	return mcp.NewToolResultErrorFromErr("operation failed", err), nil
}

// listParams builds a repository.ListParams from the pagination arguments shared
// by every list tool (page is 1-based, per_page bounded like the REST layer),
// leaving the entity-specific filters (Status, FlowID, …) for the caller to set.
func listParams(req mcp.CallToolRequest) repository.ListParams {
	page := req.GetInt("page", 1)
	if page < 1 {
		page = 1
	}
	perPage := req.GetInt("per_page", 20)
	if perPage < 1 {
		perPage = 1
	}
	if perPage > 100 {
		perPage = 100
	}
	return repository.ListParams{
		Offset: (page - 1) * perPage,
		Limit:  perPage,
		Search: strings.TrimSpace(req.GetString("search", "")),
	}
}

// argObject reads an optional JSON-object argument (e.g. `settings`, `metadata`,
// `context`) as a map. A missing key yields nil; a present non-object yields an
// error so a malformed payload is rejected rather than silently dropped.
func argObject(req mcp.CallToolRequest, key string) (map[string]any, error) {
	raw, ok := req.GetArguments()[key]
	if !ok || raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%q must be a JSON object", key)
	}
	return m, nil
}

// pageArgs adds the shared page/per_page/search properties to a list tool.
func pageArgs() []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithNumber("page", mcp.Description("1-based page number (default 1)")),
		mcp.WithNumber("per_page", mcp.Description("rows per page, 1-100 (default 20)")),
		mcp.WithString("search", mcp.Description("optional case-insensitive search term")),
	}
}

// withOpts appends the shared page args (and any extra opts) onto a base set,
// keeping each tool definition a single expression.
func withOpts(base []mcp.ToolOption, extra ...mcp.ToolOption) []mcp.ToolOption {
	return append(base, extra...)
}

// validReadSQL is re-exported through models so tools_memory keeps the read
// guard call terse; kept here so the dependency is obvious in one place.
func validReadSQL(sql string) (string, error) { return models.ValidateReadSQL(sql) }
