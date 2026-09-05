package sharedkernel

import "encoding/json"

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

const (
	ToolDefParamProperties = "properties"
	ToolDefParamRequired   = "required"
)

type ToolResult struct {
	Error      error  `json:"error"`
	ToolCallID string `json:"tool_call_id"`
	Output     string `json:"output"`
	IsError    bool   `json:"is_error"`
}
