// Package openai implements llm.LLMProvider for OpenAI and OpenAI-compatible APIs
// (vLLM, Ollama, Groq, DeepSeek, etc.).
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/niq-run/niq/core/llm"
)

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

// Config holds configuration for the OpenAI-compatible provider.
type Config struct {
	// APIKey is the bearer token sent as Authorization: Bearer <key>.
	APIKey string

	// BaseURL is the API root. Defaults to https://api.openai.com/v1.
	BaseURL string

	// Model to use when CompletionRequest.Model is empty.
	Model string

	// Headers are extra HTTP headers sent on every request, applied after the
	// built-in auth headers so they may override them.
	Headers map[string]string

	// Client is an optional *http.Client. Uses http.DefaultClient if nil.
	Client *http.Client
}

func (c *Config) defaults() {
	if c.BaseURL == "" {
		c.BaseURL = "https://api.openai.com/v1"
	}
	if c.Model == "" {
		c.Model = "gpt-4o"
	}
	if c.Client == nil {
		c.Client = http.DefaultClient
	}
}

// ---------------------------------------------------------------------------
// Provider
// ---------------------------------------------------------------------------

// Provider implements llm.LLMProvider for OpenAI-compatible APIs.
type Provider struct {
	apiKey  string
	baseURL string
	model   string
	headers map[string]string
	client  *http.Client
}

// New creates a Provider from Config.
func New(cfg Config) *Provider {
	cfg.defaults()
	return &Provider{
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		model:   cfg.Model,
		headers: cfg.Headers,
		client:  cfg.Client,
	}
}

// ---------------------------------------------------------------------------
// llm.LLMProvider implementation
// ---------------------------------------------------------------------------

// Complete performs a non-streaming chat completion.
func (p *Provider) Complete(ctx context.Context, req *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	body, err := p.buildRequestBody(req, false)
	if err != nil {
		return nil, err
	}

	resp, err := p.do(ctx, http.MethodPost, "/chat/completions", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, p.decodeError(resp)
	}

	var cr chatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return nil, fmt.Errorf("openai: decode response: %w", err)
	}

	return p.toCompletionResponse(&cr), nil
}

// CompleteStream performs a streaming chat completion.
func (p *Provider) CompleteStream(ctx context.Context, req *llm.CompletionRequest) (*llm.EventStream, error) {
	body, err := p.buildRequestBody(req, true)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: create request: %w", err)
	}
	p.setHeaders(httpReq)

	httpResp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: stream request: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		defer httpResp.Body.Close()
		return nil, p.decodeError(httpResp)
	}

	es := llm.NewEventStream()
	go p.readStream(httpResp.Body, es)
	return es, nil
}

// ListModels returns models visible through this provider.
func (p *Provider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	resp, err := p.do(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, p.decodeError(resp)
	}

	var mr modelsListResponse
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, fmt.Errorf("openai: decode models: %w", err)
	}

	models := make([]llm.ModelInfo, 0, len(mr.Data))
	for _, m := range mr.Data {
		models = append(models, llm.ModelInfo{
			ID:            m.ID,
			Name:          m.ID,
			Provider:      "openai",
			ContextWindow: modelContextWindow(m),
		})
	}
	return models, nil
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

func (p *Provider) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, r)
	if err != nil {
		return nil, fmt.Errorf("openai: new request: %w", err)
	}
	p.setHeaders(req)

	return p.client.Do(req)
}

func (p *Provider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	for k, v := range p.headers {
		req.Header.Set(k, v)
	}
}

// ---------------------------------------------------------------------------
// Request building
// ---------------------------------------------------------------------------

type chatRequest struct {
	Model           string         `json:"model"`
	Messages        []chatMessage  `json:"messages"`
	Temperature     *float32       `json:"temperature,omitempty"`
	MaxTokens       *int           `json:"max_tokens,omitempty"`
	TopP            *float32       `json:"top_p,omitempty"`
	Stop            []string       `json:"stop,omitempty"`
	Stream          bool           `json:"stream"`
	Tools           []chatTool     `json:"tools,omitempty"`
	ToolChoice      any            `json:"tool_choice,omitempty"`
	StreamOpts      *streamOptions `json:"stream_options,omitempty"`
	ReasoningEffort *string        `json:"reasoning_effort,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
	// assistant messages with thinking (DeepSeek reasoning_content); only set
	// when the message actually carries reasoning.
	ReasoningContent string `json:"reasoning_content,omitempty"`
	// assistant messages with tool calls
	ToolCalls []chatToolCall `json:"tool_calls,omitempty"`
	// tool messages
	ToolCallID string `json:"tool_call_id,omitempty"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type chatToolCall struct {
	ID       string     `json:"id"`
	Type     string     `json:"type"`
	Function chatFnCall `json:"function"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// chatFnCall is the function payload inside a tool-call (response) or tool-call history.
type chatFnCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func (p *Provider) buildRequestBody(req *llm.CompletionRequest, stream bool) ([]byte, error) {
	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	ctx := req.Context
	if ctx == nil {
		ctx = &llm.Context{}
	}

	messages := buildMessages(ctx)

	cr := chatRequest{
		Model:       model,
		Messages:    messages,
		Temperature: ctx.Temperature,
		MaxTokens:   ctx.MaxTokens,
		TopP:        ctx.TopP,
		Stop:        ctx.Stop,
		Stream:      stream,
	}

	if stream {
		cr.StreamOpts = &streamOptions{IncludeUsage: true}
	}

	if ctx.ReasoningEffort != nil {
		cr.ReasoningEffort = ctx.ReasoningEffort
	}

	if len(ctx.Tools) > 0 {
		cr.Tools = make([]chatTool, len(ctx.Tools))
		for i, t := range ctx.Tools {
			cr.Tools[i] = chatTool{
				Type: "function",
				Function: chatFunction{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.Parameters,
				},
			}
		}
		cr.ToolChoice = convertToolChoice(req.ToolChoice)
	}

	return json.Marshal(cr)
}

func buildMessages(ctx *llm.Context) []chatMessage {
	var msgs []chatMessage

	// System prompt becomes the first message.
	if ctx.SystemPrompt != "" {
		msgs = append(msgs, chatMessage{
			Role:    "system",
			Content: ctx.SystemPrompt,
		})
	}

	for _, m := range ctx.Messages {
		msgs = append(msgs, messageToChat(m))
	}

	return msgs
}

func messageToChat(m llm.Message) chatMessage {
	cm := chatMessage{Role: string(m.Role)}

	// Tool result messages carry tool_call_id.
	if m.Role == llm.RoleToolResult {
		cm.ToolCallID = m.ToolCallID
	}

	// Assistant messages may carry tool_calls.
	if m.Role == llm.RoleAssistant {
		cm.ToolCalls = contentToToolCalls(m.Content)
		// Thinking (ContentThinking) must be echoed back in reasoning_content,
		// not folded into content — providers like DeepSeek require the field
		// to be present on any assistant message that originally carried
		// reasoning, and reject it when the text is merged into content.
		if rc := reasoningToPayload(m.Content); rc != "" {
			cm.ReasoningContent = rc
		}
	}

	cm.Content = contentToPayload(m.Content)
	return cm
}

func contentToToolCalls(blocks []llm.ContentBlock) []chatToolCall {
	var calls []chatToolCall
	for _, b := range blocks {
		if b.Type != llm.ContentToolCall {
			continue
		}
		calls = append(calls, chatToolCall{
			ID:   b.ToolCallID,
			Type: "function",
			Function: chatFnCall{
				Name:      b.ToolName,
				Arguments: b.ToolArguments,
			},
		})
	}
	return calls
}

// contentToPayload builds the OpenAI content field from the non-reasoning
// blocks. Returns a plain string when all blocks are text; returns a content
// array when image blocks are present. Thinking blocks (ContentThinking) are
// intentionally excluded — they belong in reasoning_content, not content.
func contentToPayload(blocks []llm.ContentBlock) any {
	hasImage := false
	for _, b := range blocks {
		if b.Type == llm.ContentImage {
			hasImage = true
			break
		}
	}

	if !hasImage {
		// Simple case: plain text only.
		var sb strings.Builder
		for _, b := range blocks {
			if b.Type == llm.ContentText {
				sb.WriteString(b.Text)
			}
		}
		return sb.String()
	}

	// Multimodal content array.
	var parts []map[string]any
	for _, b := range blocks {
		switch b.Type {
		case llm.ContentText:
			parts = append(parts, map[string]any{
				"type": "text",
				"text": b.Text,
			})
		case llm.ContentImage:
			url := b.Data
			if b.MIMEType != "" && !strings.HasPrefix(b.Data, "data:") {
				url = "data:" + b.MIMEType + ";base64," + b.Data
			}
			parts = append(parts, map[string]any{
				"type": "image_url",
				"image_url": map[string]string{
					"url": url,
				},
			})
		}
	}
	return parts
}

// reasoningToPayload concatenates thinking blocks into the reasoning_content
// string. Returns "" when the message carries no thinking.
func reasoningToPayload(blocks []llm.ContentBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type == llm.ContentThinking {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

func convertToolChoice(tc llm.ToolChoice) any {
	if tc == nil {
		return "auto"
	}
	switch tc.Code() {
	case "auto":
		return "auto"
	case "required":
		return "required"
	case "none":
		return "none"
	case "function":
		return map[string]map[string]string{
			"type":     {"type": "function"},
			"function": {"name": tc.(llm.ToolChoiceNamed).Name},
		}
	default:
		return "auto"
	}
}

// ---------------------------------------------------------------------------
// Response types
// ---------------------------------------------------------------------------

type chatCompletionResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []choice `json:"choices"`
	Usage   *usage   `json:"usage"`
}

type choice struct {
	Index        int         `json:"index"`
	Message      chatRespMsg `json:"message"`
	FinishReason *string     `json:"finish_reason"`
}

type chatRespMsg struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content"`
	ReasoningContent string          `json:"reasoning_content"`
	ToolCalls        []chatToolCall  `json:"tool_calls"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type modelsListResponse struct {
	Data []modelEntry `json:"data"`
}

type modelEntry struct {
	ID string `json:"id"`
	// Context window is reported inconsistently across OpenAI-compatible
	// endpoints: some use context_window, others (e.g. OpenRouter) use
	// context_length. Capture both and let modelContextWindow pick a non-zero.
	ContextWindow int `json:"context_window"`
	ContextLength int `json:"context_length"`
}

// modelContextWindow returns the model's context window from whichever field
// the endpoint populated.
func modelContextWindow(m modelEntry) int {
	if m.ContextWindow > 0 {
		return m.ContextWindow
	}
	return m.ContextLength
}

func (p *Provider) toCompletionResponse(cr *chatCompletionResponse) *llm.CompletionResponse {
	msg := llm.Message{
		Role: llm.RoleAssistant,
	}

	if len(cr.Choices) > 0 {
		c := cr.Choices[0]

		// Extract reasoning_content (DeepSeek-style thinking, separate top-level field).
		if c.Message.ReasoningContent != "" {
			msg.Content = append(msg.Content, llm.ContentBlock{
				Type: llm.ContentThinking,
				Text: c.Message.ReasoningContent,
			})
		}

		// Parse content blocks (may be string, text+thinking array, or null).
		blocks := parseContentBlocks(c.Message.Content)
		msg.Content = append(msg.Content, blocks...)

		// Tool calls.
		for i, tc := range c.Message.ToolCalls {
			id := tc.ID
			if id == "" {
				id = fmt.Sprintf("call_%d", i)
			}
			msg.Content = append(msg.Content, llm.ContentBlock{
				Type:          llm.ContentToolCall,
				ToolCallID:    id,
				ToolName:      tc.Function.Name,
				ToolArguments: tc.Function.Arguments,
			})
		}

		if c.FinishReason != nil {
			msg.StopReason = *c.FinishReason
		}
	}

	resp := &llm.CompletionResponse{Message: msg}

	if cr.Usage != nil {
		resp.Usage = llm.Usage{
			InputTokens:  cr.Usage.PromptTokens,
			OutputTokens: cr.Usage.CompletionTokens,
			TotalTokens:  cr.Usage.TotalTokens,
		}
	}

	return resp
}

// contentPart mirrors a single element in the OpenAI content array.
type contentPart struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

// parseContentBlocks parses an OpenAI response content field (string or array)
// into ContentBlocks, preserving text, thinking, and other typed content.
func parseContentBlocks(raw json.RawMessage) []llm.ContentBlock {
	if len(raw) == 0 {
		return nil
	}

	// String case.
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil
		}
		return []llm.ContentBlock{{Type: llm.ContentText, Text: s}}
	}

	// Array case.
	var parts []contentPart
	if json.Unmarshal(raw, &parts) != nil {
		return nil
	}

	var blocks []llm.ContentBlock
	for _, p := range parts {
		switch p.Type {
		case "text":
			if p.Text != "" {
				blocks = append(blocks, llm.ContentBlock{Type: llm.ContentText, Text: p.Text})
			}
		case "thinking":
			if p.Thinking != "" {
				blocks = append(blocks, llm.ContentBlock{
					Type:      llm.ContentThinking,
					Text:      p.Thinking,
					Signature: p.Signature,
				})
			}
		}
	}
	return blocks
}

// ---------------------------------------------------------------------------
// SSE streaming
// ---------------------------------------------------------------------------

type chatChunk struct {
	Choices []chunkChoice `json:"choices"`
	Usage   *usage        `json:"usage"`
}

type chunkChoice struct {
	Index        int        `json:"index"`
	Delta        chunkDelta `json:"delta"`
	FinishReason *string    `json:"finish_reason"`
}

type chunkDelta struct {
	Role             string          `json:"role"`
	Content          string          `json:"content"`
	ReasoningContent string          `json:"reasoning_content"`
	ToolCalls        []chunkToolCall `json:"tool_calls"`
}

type chunkToolCall struct {
	Index    int         `json:"index"`
	ID       string      `json:"id"`
	Type     string      `json:"type"`
	Function chunkFnCall `json:"function"`
}

type chunkFnCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// inProgressCall accumulates a streaming tool call across chunks.
type inProgressCall struct {
	id       string
	name     string
	nameDone bool
	argsBuf  strings.Builder
	argsDone bool
}

func (p *Provider) readStream(body io.ReadCloser, es *llm.EventStream) {
	defer body.Close()

	scanner := bufio.NewScanner(body)
	// Larger buffer for long SSE lines (tool arguments may be large).
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		textStarted     bool
		textBuf         strings.Builder
		thinkingStarted bool
		thinkingBuf     strings.Builder
		calls           = make(map[int]*inProgressCall)
		usage           *llm.Usage
	)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk chatChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			es.Abort(fmt.Errorf("openai: parse chunk: %w", err))
			return
		}

		for _, c := range chunk.Choices {
			d := c.Delta

			// --- thinking ---
			if d.ReasoningContent != "" {
				if !thinkingStarted {
					es.Push(llm.EventThinkingStart{})
					thinkingStarted = true
				}
				thinkingBuf.WriteString(d.ReasoningContent)
				es.Push(llm.EventThinkingDelta{Delta: d.ReasoningContent})
			}

			// --- text ---
			if d.Content != "" {
				if thinkingStarted {
					es.Push(llm.EventThinkingEnd{})
					thinkingStarted = false
				}
				if !textStarted {
					es.Push(llm.EventTextStart{})
					textStarted = true
				}
				textBuf.WriteString(d.Content)
				es.Push(llm.EventTextDelta{Delta: d.Content})
			}

			// --- tool calls ---
			for _, tc := range d.ToolCalls {
				pc, ok := calls[tc.Index]
				if !ok {
					pc = &inProgressCall{}
					calls[tc.Index] = pc
				}

				if tc.ID != "" {
					pc.id = tc.ID
				}
				if tc.Function.Name != "" && !pc.nameDone {
					pc.name = tc.Function.Name
					pc.nameDone = true
					es.Push(llm.EventToolCallStart{ToolName: tc.Function.Name})
				}
				if tc.Function.Arguments != "" {
					pc.argsBuf.WriteString(tc.Function.Arguments)
					es.Push(llm.EventToolCallDelta{Delta: tc.Function.Arguments})
				}
			}

			// --- finish ---
			if c.FinishReason != nil && *c.FinishReason != "" {
				if thinkingStarted {
					es.Push(llm.EventThinkingEnd{})
					thinkingStarted = false
				}
				if textStarted {
					es.Push(llm.EventTextEnd{})
				}
				for _, pc := range calls {
					es.Push(llm.EventToolCallEnd{Arguments: pc.argsBuf.String()})
				}
			}
		}

		// Usage arrives in the final chunk when stream_options.include_usage is set.
		if chunk.Usage != nil {
			usage = &llm.Usage{
				InputTokens:  chunk.Usage.PromptTokens,
				OutputTokens: chunk.Usage.CompletionTokens,
				TotalTokens:  chunk.Usage.TotalTokens,
			}
		}
	}

	if err := scanner.Err(); err != nil {
		es.Abort(fmt.Errorf("openai: read stream: %w", err))
		return
	}

	// Build final message.
	msg := llm.Message{
		Role:       llm.RoleAssistant,
		Content:    make([]llm.ContentBlock, 0),
		Usage:      usage,
		StopReason: "stop",
	}

	if thinkingBuf.Len() > 0 {
		msg.Content = append(msg.Content, llm.ContentBlock{
			Type: llm.ContentThinking,
			Text: thinkingBuf.String(),
		})
	}

	if textBuf.Len() > 0 {
		msg.Content = append(msg.Content, llm.ContentBlock{
			Type: llm.ContentText,
			Text: textBuf.String(),
		})
	}

	for idx := 0; ; idx++ {
		pc, ok := calls[idx]
		if !ok {
			break
		}
		id := pc.id
		if id == "" {
			// Some models (e.g. THUDM/GLM) omit the tool-call id. OpenAI-format
			// APIs require it on both the call and its result, so synthesize a
			// stable one keyed by the call index within this turn.
			id = fmt.Sprintf("call_%d", idx)
			pc.id = id
		}
		msg.Content = append(msg.Content, llm.ContentBlock{
			Type:          llm.ContentToolCall,
			ToolCallID:    id,
			ToolName:      pc.name,
			ToolArguments: pc.argsBuf.String(),
		})
	}

	es.End(msg)
}

// ---------------------------------------------------------------------------
// Error handling
// ---------------------------------------------------------------------------

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type apiErrorWrapper struct {
	Error apiError `json:"error"`
}

func (p *Provider) decodeError(resp *http.Response) error {
	var wrapper apiErrorWrapper
	// Best effort decoding; fall back to status-based error if body is unparseable.
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return &llm.LLMError{
			Type:    errorTypeFromStatus(resp.StatusCode),
			Message: fmt.Sprintf("HTTP %d", resp.StatusCode),
		}
	}
	return &llm.LLMError{
		Type:    classifyError(resp.StatusCode, wrapper.Error),
		Message: wrapper.Error.Message,
	}
}

// classifyError maps an HTTP status and the provider's error body to a
// structured LLM error type. Only a genuine context-window overflow is
// reported as ErrorContextLength — the one 4xx the reason worker answers with
// compression-and-retry. Every other client error (e.g. DeepSeek's
// "reasoning_content must be passed back", which is a malformed-request
// contract violation, not an overflow) is reported as ErrorBadRequest so the
// round fails cleanly instead of compressing-and-looping forever.
func classifyError(code int, ae apiError) llm.ErrorType {
	switch {
	case code == 401 || code == 403:
		return llm.ErrorAuthFailed
	case code == 429:
		return llm.ErrorRateLimit
	case code >= 500:
		return llm.ErrorProvider
	case code >= 400:
		if isContextLengthError(ae) {
			return llm.ErrorContextLength
		}
		return llm.ErrorBadRequest
	default:
		return llm.ErrorProvider // 2xx/3xx with a decodable error body
	}
}

// isContextLengthError reports whether a provider error denotes a context
// window overflow — the only 4xx the reason worker should answer with
// compression. Detection keys off both the structured error type and common
// message phrasing, because providers vary in how they flag the overflow.
func isContextLengthError(ae apiError) bool {
	if ae.Type == "context_length_exceeded" || ae.Type == "context_length" {
		return true
	}
	msg := strings.ToLower(ae.Message)
	for _, needle := range []string{
		"context length",
		"maximum context",
		"context limit",
		"token limit",
		"exceeds the context",
		"too many tokens",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func errorTypeFromStatus(code int) llm.ErrorType {
	// Used only as a fallback when the error body cannot be decoded, so we
	// cannot distinguish a context overflow from any other 4xx. Default to a
	// non-retriable client error rather than ErrorContextLength, which would
	// otherwise trigger an unbounded compress-and-retry loop.
	switch {
	case code == 401 || code == 403:
		return llm.ErrorAuthFailed
	case code == 429:
		return llm.ErrorRateLimit
	case code >= 500:
		return llm.ErrorProvider
	default:
		return llm.ErrorBadRequest
	}
}
