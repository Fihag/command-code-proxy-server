package proxy

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dev2k6/command-code-proxy-server/internal/api"
	"github.com/dev2k6/command-code-proxy-server/internal/upstream"
	"github.com/dev2k6/command-code-proxy-server/internal/version"
	"github.com/google/uuid"
)

const defaultBaseURL = "https://api.commandcode.ai"
const defaultTimeout = 300 * time.Second
const debugLogLimit = 20000

// fingerprintFile is the local (gitignored) device fingerprint reported to
// /alpha/fingerprint/record at startup — the real CLI uploads it per process.
// Produced by tools/recapture.mjs from a tap capture; absence disables only
// the fingerprint beacon, never the proxy itself.
const fingerprintFile = "fingerprint.json"

// defaultTools holds command-code 1.50.1's built-in tool definitions,
// captured byte-exactly from the real CLI via tools/tap (clitools.json).
// The CLI never sends an empty tools array, so a client that passes no tools
// gets these instead of a behavioral tell.
//
//go:embed clitools.json
var cliToolsRaw []byte

var defaultTools []api.CCTool

func init() {
	if err := json.Unmarshal(cliToolsRaw, &defaultTools); err != nil {
		panic("clitools.json: " + err.Error())
	}
}

func truncateLog(s string) string {
	if len(s) <= debugLogLimit {
		return s
	}
	return s[:debugLogLimit] + fmt.Sprintf("... [已截断 %d 字节]", len(s)-debugLogLimit)
}

// redactSecrets masks bearer-token-shaped strings before any value is echoed
// back to a client or written to a log line. Upstream 4xx bodies are passed
// through verbatim, and some gateways reflect request context (including the
// Authorization header) in their error message.
func redactSecrets(s string) string {
	i := 0
	var b strings.Builder
	for {
		j := strings.Index(s[i:], "Bearer ")
		if j < 0 {
			b.WriteString(s[i:])
			break
		}
		j += i
		b.WriteString(s[i:j])
		k := j + len("Bearer ")
		end := k
		for end < len(s) && s[end] != ' ' && s[end] != '"' && s[end] != '\n' && s[end] != '\r' && s[end] != ',' && s[end] != '}' && end-k < 200 {
			end++
		}
		b.WriteString("Bearer [已脱敏]")
		i = end
	}
	return b.String()
}

func (p *Proxy) debugf(format string, args ...any) {
	if p.Debug {
		log.Printf(format, args...)
	}
}

func (p *Proxy) writeOpenAIError(w http.ResponseWriter, status int, message, errType string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(api.OpenAIErrorResponse{Error: api.OpenAIError{
		Message: message,
		Type:    errType,
		Param:   nil,
		Code:    nil,
	}})
}

func normalizeFinishReason(reason string) string {
	switch reason {
	case "tool_calls", "tool-calls":
		return "tool_calls"
	case "length", "max_tokens":
		return "length"
	case "content_filter", "content-filter":
		return "content_filter"
	default:
		return "stop"
	}
}

// Proxy struct
type Proxy struct {
	APIKey     string
	BaseURL    string
	Client     *http.Client
	Debug      bool
	identity   *identity
	envCfg     *envConfigCache
	beaconOnce sync.Once
	// beaconGate is closed when the startup beacon sequence has run to
	// completion (success or exhausted retries); generate requests wait on it
	// so their birth events are never seen out of order.
	beaconGate chan struct{}
}

func (p *Proxy) beaconCtx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	go func() { defer cancel(); <-ctx.Done() }()
	return ctx
}

// NewProxy creates a new proxy instance
func NewProxy(apiKey string) *Proxy {
	return &Proxy{
		APIKey:     apiKey,
		BaseURL:    defaultBaseURL,
		Client:     &http.Client{Timeout: defaultTimeout, Transport: upstream.New()},
		identity:   newIdentity(""),
		envCfg:     newEnvConfigCache(),
		beaconGate: make(chan struct{}),
	}
}

// SetProjectSlug overrides the x-project-slug value sent upstream (the CLI
// sends the slugified project directory). Empty keeps the auto value.
func (p *Proxy) SetProjectSlug(slug string) {
	p.identity = newIdentity(slug)
}

// SetWorkingDir overrides the directory the config snapshot reports upstream
// and, with it, the auto x-project-slug (header and body stay in sync). Call
// before serving; it exists so the proxy can run from anywhere without its
// own repo name showing up in every generate body.
func (p *Proxy) SetWorkingDir(dir string) {
	workDirMu.Lock()
	workDirOverride = dir
	workDirMu.Unlock()
	p.envCfg = newEnvConfigCache()
	if slug := slugPath(dir); slug != "" {
		p.identity = newIdentity(slug)
	}
}

// BuildRequest builds the CommandCode request body
func (p *Proxy) BuildRequest(openAIReq api.OpenAIChatRequest) (api.CCRequestBody, error) {
	model := MapModel(openAIReq.Model)
	system, msgs := ExtractSystem(openAIReq.Messages)
	ccMessages := ConvertMessages(msgs)

	temperature := (*float64)(nil)
	maxTokens := 64000
	if openAIReq.Temperature != nil {
		temperature = openAIReq.Temperature
	}
	if openAIReq.MaxTokens != nil {
		maxTokens = *openAIReq.MaxTokens
	}
	if openAIReq.MaxCompletionTokens != nil {
		maxTokens = *openAIReq.MaxCompletionTokens
	}

	// The CLI always ships its built-in tool set in every generate call.
	// Mirror that: use the client's tools when it passed some, otherwise
	// send the captured real-CLI definitions (clitools.json).
	tools := ConvertTools(openAIReq.Tools)
	if len(tools) == 0 {
		tools = defaultTools
	}

	var systemBlocks []api.CCSystemBlock
	if system != "" {
		systemBlocks = []api.CCSystemBlock{{Type: "text", Text: system}}
	} else {
		systemBlocks = []api.CCSystemBlock{}
	}

	ccBody := api.CCRequestBody{
		Config: p.envCfg.get(),
		// memory/taste/skills: JSON null, as the CLI's postStream sends them
		// when taste learning and project context are unused.
		Memory:         nil,
		Taste:          nil,
		Skills:         nil,
		PermissionMode: "standard",
		ThreadID:       p.identity.sessionFor(openAIReq),
		Params: api.CCChatParams{
			Model:       model,
			Messages:    ccMessages,
			Tools:       tools,
			System:      systemBlocks,
			MaxTokens:   maxTokens,
			Stream:      true,
			Temperature: temperature,
		},
	}

	return ccBody, nil
}

// CreateUpstreamRequest creates a new HTTP request to the CommandCode API
func (p *Proxy) CreateUpstreamRequest(ctx context.Context, ccBody api.CCRequestBody, apiKey string) (*http.Request, error) {
	reqJSON, err := json.Marshal(ccBody)
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}

	p.debugf("[调试] CommandCode 请求体: %s", truncateLog(string(reqJSON)))

	ccReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.BaseURL+"/alpha/generate", bytes.NewReader(reqJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to create upstream request: %w", err)
	}

	ccReq.Header.Set("Content-Type", "application/json")
	ccReq.Header.Set("Authorization", "Bearer "+apiKey)
	ccReq.Header.Set("x-command-code-version", version.GetCommandCodeVersion())
	ccReq.Header.Set("x-cli-environment", "production")
	// 不设置 Accept: text/event-stream —— 真实 CLI 的 fetch 在 wire 上发的是
	// "accept: */*", 该头由传输层 (internal/upstream) 直接按序输出。
	// Headers the official CLI attaches to every /alpha/generate call;
	// without them the upstream sees Go's default User-Agent and no session
	// correlation at all (see internal/proxy/identity.go).
	ccReq.Header.Set("User-Agent", "cli")
	ccReq.Header.Set("x-session-id", ccBody.ThreadID)
	ccReq.Header.Set("x-project-slug", p.identity.slug)
	ccReq.Header.Set("x-taste-learning", "true")

	return ccReq, nil
}

// CallUpstream makes the request to CommandCode API
func (p *Proxy) CallUpstream(req *http.Request) (*http.Response, error) {
	resp, err := p.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream error: %w", err)
	}
	return resp, nil
}

// HandleChatCompletions handles the /v1/chat/completions endpoint
func (p *Proxy) HandleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		p.writeOpenAIError(w, http.StatusMethodNotAllowed, "Method not allowed", "invalid_request_error")
		return
	}

	// Get API key from client Authorization header or server default. A
	// present-but-empty bearer token ("Bearer ") must fall back too, not be
	// forwarded upstream as an empty key.
	apiKey := r.Header.Get("Authorization")
	if apiKey != "" {
		apiKey = strings.TrimSpace(strings.TrimPrefix(apiKey, "Bearer "))
	}
	if apiKey == "" {
		apiKey = p.APIKey
	}
	if apiKey == "" {
		p.writeOpenAIError(w, http.StatusUnauthorized, "API key required. Set Authorization header.", "authentication_error")
		return
	}
	p.StartBeacon(apiKey)

	// Read request
	body, err := io.ReadAll(r.Body)
	if err != nil {
		p.writeOpenAIError(w, http.StatusBadRequest, "Failed to read body", "invalid_request_error")
		return
	}

	p.debugf("[调试] 客户端请求体: %s", truncateLog(string(body)))

	var openAIReq api.OpenAIChatRequest
	if err := json.Unmarshal(body, &openAIReq); err != nil {
		p.writeOpenAIError(w, http.StatusBadRequest, fmt.Sprintf("Invalid JSON: %s", err.Error()), "invalid_request_error")
		return
	}

	if len(openAIReq.Messages) == 0 {
		p.writeOpenAIError(w, http.StatusBadRequest, "messages array is required", "invalid_request_error")
		return
	}

	// Build CommandCode request
	ccBody, err := p.BuildRequest(openAIReq)
	if err != nil {
		p.writeOpenAIError(w, http.StatusInternalServerError, "Failed to build request", "server_error")
		return
	}

	// Create upstream request
	ccReq, err := p.CreateUpstreamRequest(r.Context(), ccBody, apiKey)
	if err != nil {
		p.writeOpenAIError(w, http.StatusInternalServerError, "Failed to create upstream request", "server_error")
		return
	}

	// The real CLI always reports its birth (whoami/lifecycle/fingerprint)
	// before any generate: hold the first request until the beacon sequence
	// has run, then forward.
	p.awaitBeacon(r.Context())

	// Call upstream
	ccResp, err := p.CallUpstream(ccReq)
	if err != nil {
		p.writeOpenAIError(w, http.StatusBadGateway, err.Error(), "api_error")
		return
	}
	defer ccResp.Body.Close()

	if ccResp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(ccResp.Body)
		message := fmt.Sprintf("Upstream error: %s", redactSecrets(string(errBody)))
		log.Printf("[错误] 上游返回 %d: %s", ccResp.StatusCode, redactSecrets(string(errBody)))
		status := http.StatusBadGateway
		if ccResp.StatusCode >= http.StatusBadRequest && ccResp.StatusCode < http.StatusInternalServerError {
			status = ccResp.StatusCode
		}
		p.writeOpenAIError(w, status, message, "api_error")
		return
	}

	requestID := "chatcmpl-" + uuid.New().String()[:29]
	created := time.Now().Unix()

	if openAIReq.Stream {
		p.StreamResponse(w, r, ccResp, requestID, ccBody.Params.Model, created)
	} else {
		p.NonStreamResponse(w, ccResp, requestID, ccBody.Params.Model, created)
	}
}

// StreamResponse handles streaming response from CommandCode to OpenAI SSE
func (p *Proxy) StreamResponse(w http.ResponseWriter, r *http.Request, ccResp *http.Response, requestID, model string, created int64) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		p.writeOpenAIError(w, http.StatusInternalServerError, "Streaming not supported", "server_error")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	scanner := bufio.NewScanner(ccResp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	sentRole := false
	toolCallIndex := 0
	toolCallIndexes := map[string]int{}
	finishSent := false
	sawToolCalls := false

	for scanner.Scan() {
		select {
		case <-r.Context().Done():
			return
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		p.debugf("[调试] CommandCode 流式数据行: %s", truncateLog(line))

		var event api.CCStreamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		switch event.Type {
		case "text-delta":
			delta := api.OpenAIDelta{Content: event.Text}
			if !sentRole {
				delta.Role = "assistant"
				sentRole = true
			}
			p.WriteSSE(w, flusher, api.OpenAIChatResponse{
				ID:      requestID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   model,
				Choices: []api.OpenAIChoice{{Index: 0, Delta: &delta}},
			})

		case "tool-use":
			// Register the id just like tool-input-start/tool-call do, so an
			// aggregated tool-call event for the same call is recognised as
			// already streamed instead of emitted again on a new index.
			if _, ok := toolCallIndexes[event.ToolCallID]; ok {
				continue
			}
			toolCallIndexes[event.ToolCallID] = toolCallIndex
			idx := toolCallIndex
			toolCallIndex++
			sawToolCalls = true
			toolCalls := []api.OpenAIDeltaToolCall{{
				Index:    idx,
				ID:       event.ToolCallID,
				Type:     "function",
				Function: &api.OpenAIDeltaFunction{Name: event.ToolName},
			}}
			delta := api.OpenAIDelta{ToolCalls: toolCalls}
			if !sentRole {
				delta.Role = "assistant"
				sentRole = true
			}
			p.WriteSSE(w, flusher, api.OpenAIChatResponse{
				ID:      requestID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   model,
				Choices: []api.OpenAIChoice{{Index: 0, Delta: &delta}},
			})

		case "tool-delta":
			toolCalls := []api.OpenAIDeltaToolCall{{
				Index:    toolCallIndex - 1,
				Function: &api.OpenAIDeltaFunction{Arguments: event.Text},
			}}
			p.WriteSSE(w, flusher, api.OpenAIChatResponse{
				ID:      requestID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   model,
				Choices: []api.OpenAIChoice{{Index: 0, Delta: &api.OpenAIDelta{ToolCalls: toolCalls}}},
			})

		case "tool-input-start":
			sawToolCalls = true
			if _, ok := toolCallIndexes[event.ID]; !ok {
				toolCallIndexes[event.ID] = toolCallIndex
				toolCallIndex++
			}
			delta := api.OpenAIDelta{ToolCalls: []api.OpenAIDeltaToolCall{{
				Index: toolCallIndexes[event.ID],
				ID:    event.ID,
				Type:  "function",
				Function: &api.OpenAIDeltaFunction{
					Name: event.ToolName,
				},
			}}}
			if !sentRole {
				delta.Role = "assistant"
				sentRole = true
			}
			p.WriteSSE(w, flusher, api.OpenAIChatResponse{
				ID:      requestID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   model,
				Choices: []api.OpenAIChoice{{Index: 0, Delta: &delta}},
			})

		case "tool-input-delta":
			idx, ok := toolCallIndexes[event.ID]
			if !ok {
				idx = toolCallIndex
				toolCallIndexes[event.ID] = idx
				toolCallIndex++
			}
			p.WriteSSE(w, flusher, api.OpenAIChatResponse{
				ID:      requestID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   model,
				Choices: []api.OpenAIChoice{{Index: 0, Delta: &api.OpenAIDelta{ToolCalls: []api.OpenAIDeltaToolCall{{
					Index:    idx,
					Function: &api.OpenAIDeltaFunction{Arguments: event.Delta},
				}}}}},
			})

		case "tool-call":
			sawToolCalls = true
			if _, alreadyStreamed := toolCallIndexes[event.ToolCallID]; alreadyStreamed {
				continue
			}
			idx := toolCallIndex
			toolCallIndexes[event.ToolCallID] = idx
			toolCallIndex++
			args := ""
			if event.Input != nil {
				if data, err := json.Marshal(event.Input); err == nil {
					args = string(data)
				}
			}
			delta := api.OpenAIDelta{ToolCalls: []api.OpenAIDeltaToolCall{{
				Index: idx,
				ID:    event.ToolCallID,
				Type:  "function",
				Function: &api.OpenAIDeltaFunction{
					Name:      event.ToolName,
					Arguments: args,
				},
			}}}
			if !sentRole {
				delta.Role = "assistant"
				sentRole = true
			}
			p.WriteSSE(w, flusher, api.OpenAIChatResponse{
				ID:      requestID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   model,
				Choices: []api.OpenAIChoice{{Index: 0, Delta: &delta}},
			})

		case "finish":
			reason := normalizeFinishReason(event.FinishReason)
			p.WriteSSE(w, flusher, api.OpenAIChatResponse{
				ID:      requestID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   model,
				Choices: []api.OpenAIChoice{{
					Index:        0,
					Delta:        &api.OpenAIDelta{},
					FinishReason: &reason,
				}},
			})
			fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()
			finishSent = true

		case "error":
			log.Printf("[错误] 流式响应出错: %v", event.Error)
			msg := "upstream stream error"
			if event.Error != nil && event.Error.Message != "" {
				msg = redactSecrets(event.Error.Message)
			}
			// Error frames inside the stream are how litellm-style gateways
			// report mid-stream failures; OpenAI SDKs surface them as API
			// errors. The finish/[DONE] tail below still closes the stream.
			frame, _ := json.Marshal(map[string]any{
				"error": map[string]any{"message": msg, "type": "api_error"},
			})
			fmt.Fprintf(w, "data: %s\n\n", frame)
			flusher.Flush()
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		log.Printf("[错误] 流读取失败: %v", err)
	}

	// Upstream ended without a finish event (connection dropped mid-stream,
	// or the server closed right after an error event): a stream that never
	// says [DONE] leaves well-behaved OpenAI clients hanging on read. Close
	// the protocol on our own so the client always terminates cleanly.
	if !finishSent {
		reason := "stop"
		if sawToolCalls {
			reason = "tool_calls"
		}
		p.WriteSSE(w, flusher, api.OpenAIChatResponse{
			ID:      requestID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []api.OpenAIChoice{{
				Index:        0,
				Delta:        &api.OpenAIDelta{},
				FinishReason: &reason,
			}},
		})
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}
}

// WriteSSE writes a Server-Sent Event
func (p *Proxy) WriteSSE(w io.Writer, flusher http.Flusher, resp api.OpenAIChatResponse) {
	data, _ := json.Marshal(resp)
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

// NonStreamResponse handles non-streaming response
func (p *Proxy) NonStreamResponse(w http.ResponseWriter, ccResp *http.Response, requestID, model string, created int64) {
	scanner := bufio.NewScanner(ccResp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var content strings.Builder
	var inputTokens, outputTokens int
	var hasToolCalls bool
	var toolCalls []api.ToolCall
	var upstreamErrMsg string
	toolCallByID := map[string]int{}
	toolInputBuffers := map[string]*strings.Builder{}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		p.debugf("[调试] CommandCode 流式数据行: %s", truncateLog(line))

		var event api.CCStreamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		switch event.Type {
		case "text-delta":
			content.WriteString(event.Text)
		case "tool-use":
			hasToolCalls = true
			toolCallByID[event.ToolCallID] = len(toolCalls)
			toolCalls = append(toolCalls, api.ToolCall{
				ID:   event.ToolCallID,
				Type: "function",
				Function: api.FunctionCall{
					Name:      event.ToolName,
					Arguments: "",
				},
			})
		case "tool-delta":
			if len(toolCalls) > 0 {
				toolCalls[len(toolCalls)-1].Function.Arguments += event.Text
			}
		case "tool-input-start":
			hasToolCalls = true
			toolCallByID[event.ID] = len(toolCalls)
			toolInputBuffers[event.ID] = &strings.Builder{}
			toolCalls = append(toolCalls, api.ToolCall{
				ID:   event.ID,
				Type: "function",
				Function: api.FunctionCall{
					Name:      event.ToolName,
					Arguments: "",
				},
			})
		case "tool-input-delta":
			if b := toolInputBuffers[event.ID]; b != nil {
				b.WriteString(event.Delta)
			}
			if idx, ok := toolCallByID[event.ID]; ok {
				toolCalls[idx].Function.Arguments += event.Delta
			}
		case "tool-call":
			hasToolCalls = true
			args := ""
			if event.Input != nil {
				if data, err := json.Marshal(event.Input); err == nil {
					args = string(data)
				}
			}
			if idx, ok := toolCallByID[event.ToolCallID]; ok {
				toolCalls[idx].Function.Name = event.ToolName
				if args != "" {
					toolCalls[idx].Function.Arguments = args
				}
			} else {
				toolCallByID[event.ToolCallID] = len(toolCalls)
				toolCalls = append(toolCalls, api.ToolCall{
					ID:   event.ToolCallID,
					Type: "function",
					Function: api.FunctionCall{
						Name:      event.ToolName,
						Arguments: args,
					},
				})
			}
		case "finish":
			if event.TotalUsage != nil {
				inputTokens = event.TotalUsage.InputTokens
				outputTokens = event.TotalUsage.OutputTokens
			}
		case "error":
			log.Printf("[错误] 流式响应出错: %v", event.Error)
			if upstreamErrMsg == "" {
				if event.Error != nil && event.Error.Message != "" {
					upstreamErrMsg = redactSecrets(event.Error.Message)
				} else {
					upstreamErrMsg = "upstream stream error"
				}
			}
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		log.Printf("[错误] 流读取失败: %v", err)
	}

	// An upstream error must never be answered with an HTTP 200 holding an
	// empty message and zero usage — clients would record it as a real
	// completion. Propagate it as an error instead.
	if upstreamErrMsg != "" {
		p.writeOpenAIError(w, http.StatusBadGateway, "Upstream error: "+upstreamErrMsg, "api_error")
		return
	}

	msg := &api.OpenAIMessage{
		Role:    "assistant",
		Content: content.String(),
	}
	finishReason := "stop"
	if hasToolCalls {
		msg.Content = nil
		msg.ToolCalls = toolCalls
		finishReason = "tool_calls"
	}

	response := api.OpenAIChatResponse{
		ID:      requestID,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: []api.OpenAIChoice{{
			Index:        0,
			Message:      msg,
			FinishReason: &finishReason,
		}},
		Usage: &api.OpenAIUsage{
			PromptTokens:     inputTokens,
			CompletionTokens: outputTokens,
			TotalTokens:      inputTokens + outputTokens,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (p *Proxy) HandleResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		p.writeOpenAIError(w, http.StatusMethodNotAllowed, "Method not allowed", "invalid_request_error")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		p.writeOpenAIError(w, http.StatusBadRequest, "Failed to read body", "invalid_request_error")
		return
	}

	p.debugf("[调试] 客户端 Responses 请求体: %s", truncateLog(string(body)))

	var responsesReq api.OpenAIResponsesRequest
	if err := json.Unmarshal(body, &responsesReq); err != nil {
		p.writeOpenAIError(w, http.StatusBadRequest, fmt.Sprintf("Invalid JSON: %s", err.Error()), "invalid_request_error")
		return
	}

	chatReq := responsesToChatRequest(responsesReq)
	rewritten, err := json.Marshal(chatReq)
	if err != nil {
		p.writeOpenAIError(w, http.StatusInternalServerError, "Failed to build request", "server_error")
		return
	}

	r.Body = io.NopCloser(bytes.NewReader(rewritten))
	r.ContentLength = int64(len(rewritten))
	p.HandleChatCompletions(w, r)
}

func responsesToChatRequest(req api.OpenAIResponsesRequest) api.OpenAIChatRequest {
	messages := responsesInputToMessages(req.Input)
	if req.Instructions != nil {
		messages = append([]api.OpenAIMessage{{Role: "system", Content: req.Instructions}}, messages...)
	}

	maxTokens := req.MaxCompletionTokens
	if maxTokens == nil {
		maxTokens = req.MaxOutputTokens
	}
	if maxTokens == nil {
		maxTokens = req.MaxTokens
	}

	return api.OpenAIChatRequest{
		Model:               req.Model,
		Messages:            messages,
		Temperature:         req.Temperature,
		MaxTokens:           req.MaxTokens,
		MaxCompletionTokens: maxTokens,
		Stream:              req.Stream,
		Tools:               req.Tools,
		ToolChoice:          req.ToolChoice,
		ParallelToolCalls:   req.ParallelToolCalls,
		ResponseFormat:      req.ResponseFormat,
		Stop:                req.Stop,
		TopP:                req.TopP,
		User:                req.User,
	}
}

func responsesInputToMessages(input any) []api.OpenAIMessage {
	switch v := input.(type) {
	case nil:
		return nil
	case string:
		return []api.OpenAIMessage{{Role: "user", Content: v}}
	case []any:
		if messages := responseItemsToMessages(v); len(messages) > 0 {
			return messages
		}
		return []api.OpenAIMessage{{Role: "user", Content: v}}
	default:
		return []api.OpenAIMessage{{Role: "user", Content: v}}
	}
}

func responseItemsToMessages(items []any) []api.OpenAIMessage {
	messages := make([]api.OpenAIMessage, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		if role == "" {
			role = "user"
		}
		content := m["content"]
		if content == nil {
			content = m["text"]
		}
		if content == nil {
			content = m["input"]
		}
		messages = append(messages, api.OpenAIMessage{Role: role, Content: content})
	}
	return messages
}

// HandleModels handles the /v1/models endpoint
func (p *Proxy) HandleModels(w http.ResponseWriter, r *http.Request) {
	data := staticModels()
	if catalog.isLoaded() {
		data = catalog.openAIModels()
	}
	models := api.OpenAIModelList{
		Object: "list",
		Data:   data,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(models)
}
