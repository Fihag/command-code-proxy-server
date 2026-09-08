package api

import "encoding/json"

// CommandCode API types (internal)

type CCToolOutput struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type CCContentPart struct {
	Type       string        `json:"type"`
	Text       *string       `json:"text,omitempty"`
	ID         *string       `json:"id,omitempty"`
	Name       *string       `json:"name,omitempty"`
	Input      any           `json:"input,omitempty"`
	ToolCallID *string       `json:"toolCallId,omitempty"`
	ToolName   *string       `json:"toolName,omitempty"`
	Output     *CCToolOutput `json:"output,omitempty"`
	ToolUseID  *string       `json:"tool_use_id,omitempty"`
	Content    any           `json:"content,omitempty"`
}

type CCMessage struct {
	Role    string          `json:"role"`
	Content []CCContentPart `json:"content"`
}

// CCTool mirrors the CLI's toWireTools output: exactly name, description,
// input_schema in that order. InputSchema is kept as raw bytes so the JSON
// Schema's own key order survives the round trip untouched.
type CCTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// CCSystemBlock is one system prompt content block ({type:"text",text:...});
// the CLI always sends system as an array of such blocks.
type CCSystemBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type CCChatParams struct {
	// Order mirrors the CLI wire params: stream precedes temperature, and
	// temperature is only present when the caller set it.
	Model       string          `json:"model"`
	Messages    []CCMessage     `json:"messages"`
	Tools       []CCTool        `json:"tools"`
	System      []CCSystemBlock `json:"system"`
	MaxTokens   int             `json:"max_tokens"`
	Stream      bool            `json:"stream"`
	Temperature *float64        `json:"temperature,omitempty"`
}

type CCConfig struct {
	WorkingDir    string   `json:"workingDir"`
	Date          string   `json:"date"`
	Environment   string   `json:"environment"`
	Structure     []string `json:"structure"`
	IsGitRepo     bool     `json:"isGitRepo"`
	CurrentBranch string   `json:"currentBranch"`
	MainBranch    string   `json:"mainBranch"`
	GitStatus     string   `json:"gitStatus"`
	RecentCommits []string `json:"recentCommits"`
}

type CCRequestBody struct {
	// Field order and nullability mirror the official CLI's /alpha/generate
	// wire body exactly (authoritative tap capture of command-code 1.50.1):
	// memory/taste/skills are JSON null when unused, threadId carries the
	// per-conversation uuid (identical to the x-session-id header), there is
	// no mode key on chat calls, and promptCache is absent.
	Config         CCConfig     `json:"config"`
	Memory         *string      `json:"memory"`
	Taste          *string      `json:"taste"`
	Skills         *string      `json:"skills"`
	PermissionMode string       `json:"permissionMode"`
	ThreadID       string       `json:"threadId"`
	Params         CCChatParams `json:"params"`
}

type CCStreamEvent struct {
	Type         string         `json:"type"`
	Text         string         `json:"text"`
	ID           string         `json:"id"`
	Delta        string         `json:"delta"`
	Input        map[string]any `json:"input"`
	ToolCallID   string         `json:"toolCallId"`
	ToolName     string         `json:"toolName"`
	FinishReason string         `json:"finishReason"`
	Error        *struct {
		Message    string `json:"message"`
		StatusCode *int   `json:"statusCode"`
	} `json:"error"`
	TotalUsage *struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
	} `json:"totalUsage"`
}
