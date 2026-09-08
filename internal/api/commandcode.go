package api

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

type CCChatParams struct {
	// Order mirrors the CLI wire params: stream precedes temperature, and
	// temperature is only present when the caller set it.
	Model       string      `json:"model"`
	Messages    []CCMessage `json:"messages"`
	Tools       []any       `json:"tools"`
	System      string      `json:"system"`
	MaxTokens   int         `json:"max_tokens"`
	Stream      bool        `json:"stream"`
	Temperature *float64    `json:"temperature,omitempty"`
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
	// wire body exactly (see buildCommandAuthHeaders/postStream in
	// command-code dist/cli.mjs): memory/taste/skills are JSON null when
	// unused, threadId is omitted (toWireThreadId drops non-uuid ids, and
	// the CLI session id "sess_..." never qualifies), and promptCache is
	// absent on chat calls.
	Config         CCConfig     `json:"config"`
	Memory         *string      `json:"memory"`
	Taste          *string      `json:"taste"`
	Skills         *string      `json:"skills"`
	PermissionMode string       `json:"permissionMode"`
	ThreadID       string       `json:"threadId,omitempty"`
	Mode           string       `json:"mode"`
	Params         CCChatParams `json:"params"`

	// Session carries the CLI-style "sess_..." id for the x-session-id
	// header. It is deliberately not serialized: the real CLI sends
	// threadId only when it is a uuid, and its session id never is.
	Session string `json:"-"`
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
