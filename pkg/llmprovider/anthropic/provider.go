// Package anthropic implements llm.LLMProvider for Anthropic-compatible
// Messages APIs, including DeepSeek's Anthropic-compatible endpoint.
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/54c1/niq/core/llm"
)

// Config holds the configuration for the Anthropic-compatible provider.
type Config struct {
	// APIKey is sent as x-api-key.
	APIKey string

	// BaseURL is the API root. Defaults to https://api.anthropic.com.
	// DeepSeek's Anthropic-compatible endpoint is https://api.deepseek.com/anthropic.
	BaseURL string

	// Model to use when CompletionRequest.Model is empty.
	Model string

	// AnthropicVersion is sent as anthropic-version.
	AnthropicVersion string

	// Headers are extra HTTP headers sent on every request, applied after the
	// built-in auth headers so they may override them.
	Headers map[string]string

	// Client is an optional *http.Client. Uses http.DefaultClient if nil.
	Client *http.Client
}

func (c *Config) defaults() {
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if strings.HasSuffix(c.BaseURL, "/v1") {
		c.BaseURL = strings.TrimSuffix(c.BaseURL, "/v1")
	}
	if c.BaseURL == "" {
		c.BaseURL = "https://api.anthropic.com"
	}
	if c.Model == "" {
		c.Model = "claude-sonnet-4-20250514"
	}
	if c.AnthropicVersion == "" {
		c.AnthropicVersion = "2023-06-01"
	}
	if c.Client == nil {
		c.Client = http.DefaultClient
	}
}

// Provider implements llm.LLMProvider for Anthropic-compatible Messages APIs.
type Provider struct {
	apiKey           string
	baseURL          string
	model            string
	anthropicVersion string
	headers          map[string]string
	client           *http.Client
}

// New creates a Provider from Config.
func New(cfg Config) *Provider {
	cfg.defaults()
	return &Provider{
		apiKey:           cfg.APIKey,
		baseURL:          cfg.BaseURL,
		model:            cfg.Model,
		anthropicVersion: cfg.AnthropicVersion,
		headers:          cfg.Headers,
		client:           cfg.Client,
	}
}

// ---------------------------------------------------------------------------
// llm.LLMProvider implementation
// ---------------------------------------------------------------------------

// Complete performs a non-streaming Anthropic Messages completion.
func (p *Provider) Complete(ctx context.Context, req *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	body, err := p.buildRequestBody(req, false)
	if err != nil {
		return nil, err
	}

	resp, err := p.do(ctx, http.MethodPost, "/v1/messages", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, p.decodeError(resp)
	}

	var mr messagesResponse
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, fmt.Errorf("anthropic: decode response: %w", err)
	}

	return p.toCompletionResponse(&mr), nil
}

// CompleteStream performs a streaming Anthropic Messages completion.
func (p *Provider) CompleteStream(ctx context.Context, req *llm.CompletionRequest) (*llm.EventStream, error) {
	body, err := p.buildRequestBody(req, true)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: create request: %w", err)
	}
	p.setHeaders(httpReq)

	httpResp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: stream request: %w", err)
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
	resp, err := p.do(ctx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, p.decodeError(resp)
	}

	var mr modelsListResponse
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, fmt.Errorf("anthropic: decode models: %w", err)
	}

	models := make([]llm.ModelInfo, 0, len(mr.Data))
	for _, m := range mr.Data {
		models = append(models, llm.ModelInfo{
			ID:            m.ID,
			Name:          m.ID,
			Provider:      "anthropic",
			ContextWindow: m.ContextWindow,
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
		return nil, fmt.Errorf("anthropic: new request: %w", err)
	}
	p.setHeaders(req)

	return p.client.Do(req)
}

func (p *Provider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", p.anthropicVersion)
	if p.apiKey != "" {
		req.Header.Set("x-api-key", p.apiKey)
	}
	for k, v := range p.headers {
		req.Header.Set(k, v)
	}
}

// ---------------------------------------------------------------------------
// Request building
// ---------------------------------------------------------------------------

type messagesRequest struct {
	Model         string             `json:"model"`
	MaxTokens     int                `json:"max_tokens"`
	System        string             `json:"system,omitempty"`
	Messages      []anthropicMessage `json:"messages"`
	Temperature   *float32           `json:"temperature,omitempty"`
	TopP          *float32           `json:"top_p,omitempty"`
	StopSequences []string           `json:"stop_sequences,omitempty"`
	Stream        bool               `json:"stream,omitempty"`
	Tools         []anthropicTool    `json:"tools,omitempty"`
	ToolChoice    any                `json:"tool_choice,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
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

	maxTokens := 4096
	if ctx.MaxTokens != nil {
		maxTokens = *ctx.MaxTokens
	}

	cr := messagesRequest{
		Model:         model,
		MaxTokens:     maxTokens,
		System:        buildSystem(ctx),
		Messages:      buildMessages(ctx),
		Temperature:   ctx.Temperature,
		TopP:          ctx.TopP,
		StopSequences: ctx.Stop,
		Stream:        stream,
	}

	if len(ctx.Tools) > 0 {
		cr.Tools = make([]anthropicTool, len(ctx.Tools))
		for i, t := range ctx.Tools {
			params := t.Parameters
			if params == nil {
				params = map[string]any{"type": "object"}
			}
			cr.Tools[i] = anthropicTool{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: params,
			}
		}
		cr.ToolChoice = convertToolChoice(req.ToolChoice)
	}

	return json.Marshal(cr)
}

func buildSystem(ctx *llm.Context) string {
	var parts []string
	if ctx.SystemPrompt != "" {
		parts = append(parts, ctx.SystemPrompt)
	}
	for _, m := range ctx.Messages {
		if m.Role == llm.RoleSystem {
			var sb strings.Builder
			for _, b := range m.Content {
				if b.Type == llm.ContentText || b.Type == llm.ContentThinking {
					sb.WriteString(b.Text)
				}
			}
			if sb.Len() > 0 {
				parts = append(parts, sb.String())
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func buildMessages(ctx *llm.Context) []anthropicMessage {
	var msgs []anthropicMessage
	for _, m := range ctx.Messages {
		switch m.Role {
		case llm.RoleSystem:
			continue
		case llm.RoleToolResult:
			msgs = append(msgs, anthropicMessage{
				Role:    "user",
				Content: anthropicToolResultContent(m),
			})
		case llm.RoleAssistant:
			msgs = append(msgs, anthropicMessage{
				Role:    "assistant",
				Content: anthropicAssistantContent(m),
			})
		default:
			msgs = append(msgs, anthropicMessage{
				Role:    "user",
				Content: anthropicUserContent(m),
			})
		}
	}
	return msgs
}

func anthropicUserContent(m llm.Message) any {
	var sb strings.Builder
	var parts []map[string]any
	for _, b := range m.Content {
		switch b.Type {
		case llm.ContentText, llm.ContentThinking:
			sb.WriteString(b.Text)
		case llm.ContentImage:
			if sb.Len() > 0 {
				parts = append(parts, map[string]any{"type": "text", "text": sb.String()})
				sb.Reset()
			}
			mime := b.MIMEType
			if mime == "" {
				mime = "image/png"
			}
			parts = append(parts, map[string]any{
				"type": "image",
				"source": map[string]any{
					"type":       "base64",
					"media_type": mime,
					"data":       b.Data,
				},
			})
		}
	}
	if sb.Len() > 0 || len(parts) == 0 {
		return sb.String()
	}
	parts = append(parts, map[string]any{"type": "text", "text": sb.String()})
	return parts
}

func anthropicAssistantContent(m llm.Message) []map[string]any {
	var parts []map[string]any
	for _, b := range m.Content {
		switch b.Type {
		case llm.ContentText:
			parts = append(parts, map[string]any{"type": "text", "text": b.Text})
		case llm.ContentThinking:
			item := map[string]any{"type": "thinking", "thinking": b.Text}
			if b.Signature != "" {
				item["signature"] = b.Signature
			}
			parts = append(parts, item)
		case llm.ContentToolCall:
			parts = append(parts, map[string]any{
				"type":  "tool_use",
				"id":    b.ToolCallID,
				"name":  b.ToolName,
				"input": toolInput(b.ToolArguments),
			})
		}
	}
	return parts
}

func anthropicToolResultContent(m llm.Message) []map[string]any {
	var sb strings.Builder
	for _, b := range m.Content {
		if b.Type == llm.ContentText {
			sb.WriteString(b.Text)
		}
	}
	content := sb.String()
	if content == "" {
		content = "[empty tool result]"
	}
	return []map[string]any{{
		"type":        "tool_result",
		"tool_use_id": m.ToolCallID,
		"content":     content,
		"is_error":    m.IsError,
	}}
}

func toolInput(args string) json.RawMessage {
	if args == "" || !json.Valid([]byte(args)) {
		return json.RawMessage("{}")
	}
	return json.RawMessage(args)
}

func convertToolChoice(tc llm.ToolChoice) any {
	if tc == nil {
		return map[string]string{"type": "auto"}
	}
	switch tc.Code() {
	case "none":
		return map[string]string{"type": "none"}
	case "required":
		return map[string]string{"type": "any"}
	case "function":
		if named, ok := tc.(llm.ToolChoiceNamed); ok {
			return map[string]any{"type": "tool", "name": named.Name}
		}
		return map[string]string{"type": "auto"}
	default:
		return map[string]string{"type": "auto"}
	}
}

// ---------------------------------------------------------------------------
// Response types
// ---------------------------------------------------------------------------

type messagesResponse struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Role       string             `json:"role"`
	Content    []respContentBlock `json:"content"`
	StopReason *string            `json:"stop_reason"`
	Usage      *anthropicUsage    `json:"usage"`
}

type respContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

type modelsListResponse struct {
	Data []modelEntry `json:"data"`
}

type modelEntry struct {
	ID            string `json:"id"`
	ContextWindow int    `json:"context_window"`
}

func (p *Provider) toCompletionResponse(mr *messagesResponse) *llm.CompletionResponse {
	msg := llm.Message{
		Role:       llm.RoleAssistant,
		StopReason: normalizeStopReason(mr.StopReason),
	}

	for _, b := range mr.Content {
		switch b.Type {
		case "thinking":
			msg.Content = append(msg.Content, llm.ContentBlock{
				Type:      llm.ContentThinking,
				Text:      b.Thinking,
				Signature: b.Signature,
			})
		case "text":
			msg.Content = append(msg.Content, llm.ContentBlock{
				Type: llm.ContentText,
				Text: b.Text,
			})
		case "tool_use":
			msg.Content = append(msg.Content, llm.ContentBlock{
				Type:          llm.ContentToolCall,
				ToolCallID:    b.ID,
				ToolName:      b.Name,
				ToolArguments: rawArguments(b.Input),
			})
		}
	}

	resp := &llm.CompletionResponse{Message: msg}
	if mr.Usage != nil {
		resp.Usage = usageFromAnthropic(mr.Usage)
	}
	return resp
}

func normalizeStopReason(reason *string) string {
	if reason == nil {
		return ""
	}
	switch *reason {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "max_tokens"
	case "tool_use":
		return "tool_calls"
	default:
		return *reason
	}
}

func rawArguments(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func usageFromAnthropic(u *anthropicUsage) llm.Usage {
	usage := llm.Usage{
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		TotalTokens:  u.InputTokens + u.OutputTokens,
	}
	if u.CacheCreationInputTokens > 0 {
		n := u.CacheCreationInputTokens
		usage.CacheCreationTokens = &n
	}
	if u.CacheReadInputTokens > 0 {
		n := u.CacheReadInputTokens
		usage.CacheReadTokens = &n
	}
	return usage
}

// ---------------------------------------------------------------------------
// SSE streaming
// ---------------------------------------------------------------------------

type anthropicStreamEvent struct {
	Type         string             `json:"type"`
	Index        int                `json:"index"`
	ContentBlock *respContentBlock  `json:"content_block"`
	Delta        *streamDelta       `json:"delta"`
	Message      *messagesResponse  `json:"message"`
	Usage        *anthropicUsage    `json:"usage"`
	Error        *anthropicAPIError `json:"error"`
}

type streamDelta struct {
	Type        string  `json:"type"`
	Text        string  `json:"text"`
	Thinking    string  `json:"thinking"`
	PartialJSON string  `json:"partial_json"`
	Signature   string  `json:"signature"`
	StopReason  *string `json:"stop_reason"`
}

type inProgressCall struct {
	id       string
	name     string
	args     strings.Builder
	argsDone bool
}

func (p *Provider) readStream(body io.ReadCloser, es *llm.EventStream) {
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		textStarted     bool
		thinkingStarted bool
		textBuf         strings.Builder
		thinkingBuf     strings.Builder
		calls           = make(map[int]*inProgressCall)
		usage           *llm.Usage
		stopReason      string
	)

	finish := func() {
		if thinkingStarted {
			es.Push(llm.EventThinkingEnd{})
		}
		if textStarted {
			es.Push(llm.EventTextEnd{})
		}
		for idx := 0; ; idx++ {
			pc, ok := calls[idx]
			if !ok {
				break
			}
			if !pc.argsDone {
				es.Push(llm.EventToolCallEnd{Arguments: pc.args.String()})
			}
		}

		msg := llm.Message{
			Role:       llm.RoleAssistant,
			Usage:      usage,
			StopReason: normalizeStopReason(&stopReason),
		}
		if thinkingBuf.Len() > 0 {
			msg.Content = append(msg.Content, llm.ContentBlock{Type: llm.ContentThinking, Text: thinkingBuf.String()})
		}
		if textBuf.Len() > 0 {
			msg.Content = append(msg.Content, llm.ContentBlock{Type: llm.ContentText, Text: textBuf.String()})
		}
		for idx := 0; ; idx++ {
			pc, ok := calls[idx]
			if !ok {
				break
			}
			msg.Content = append(msg.Content, llm.ContentBlock{
				Type:          llm.ContentToolCall,
				ToolCallID:    pc.id,
				ToolName:      pc.name,
				ToolArguments: pc.args.String(),
			})
		}
		es.End(msg)
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		var evt anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			es.Abort(fmt.Errorf("anthropic: parse chunk: %w", err))
			return
		}

		switch evt.Type {
		case "message_start":
			if evt.Message != nil && evt.Message.Usage != nil {
				u := usageFromAnthropic(evt.Message.Usage)
				usage = &u
			}
		case "content_block_start":
			b := evt.ContentBlock
			if b == nil {
				continue
			}
			switch b.Type {
			case "text":
				es.Push(llm.EventTextStart{})
				textStarted = true
			case "thinking":
				es.Push(llm.EventThinkingStart{Signature: b.Signature})
				thinkingStarted = true
			case "tool_use":
				calls[evt.Index] = &inProgressCall{id: b.ID, name: b.Name}
				es.Push(llm.EventToolCallStart{ToolName: b.Name})
			}
		case "content_block_delta":
			d := evt.Delta
			if d == nil {
				continue
			}
			switch d.Type {
			case "text_delta":
				if !textStarted {
					es.Push(llm.EventTextStart{})
					textStarted = true
				}
				textBuf.WriteString(d.Text)
				es.Push(llm.EventTextDelta{Delta: d.Text})
			case "thinking_delta":
				if !thinkingStarted {
					es.Push(llm.EventThinkingStart{})
					thinkingStarted = true
				}
				thinkingBuf.WriteString(d.Thinking)
				es.Push(llm.EventThinkingDelta{Delta: d.Thinking})
			case "input_json_delta":
				pc := calls[evt.Index]
				if pc == nil {
					pc = &inProgressCall{}
					calls[evt.Index] = pc
				}
				pc.args.WriteString(d.PartialJSON)
				es.Push(llm.EventToolCallDelta{Delta: d.PartialJSON})
			}
		case "content_block_stop":
			if thinkingStarted {
				es.Push(llm.EventThinkingEnd{})
				thinkingStarted = false
			}
			if textStarted {
				es.Push(llm.EventTextEnd{})
				textStarted = false
			}
			if pc, ok := calls[evt.Index]; ok && !pc.argsDone {
				es.Push(llm.EventToolCallEnd{Arguments: pc.args.String()})
				pc.argsDone = true
			}
		case "message_delta":
			if evt.Delta != nil && evt.Delta.StopReason != nil {
				stopReason = *evt.Delta.StopReason
			}
			if evt.Usage != nil {
				if usage == nil {
					u := usageFromAnthropic(evt.Usage)
					usage = &u
				} else {
					if evt.Usage.OutputTokens > 0 {
						usage.OutputTokens = evt.Usage.OutputTokens
					}
					usage.TotalTokens = usage.InputTokens + usage.OutputTokens
				}
			}
		case "message_stop":
			finish()
			return
		case "error":
			if evt.Error != nil {
				es.Abort(&llm.LLMError{Type: llm.ErrorProvider, Message: evt.Error.Message})
			} else {
				es.Abort(&llm.LLMError{Type: llm.ErrorProvider, Message: "anthropic stream error"})
			}
			return
		}
	}

	if err := scanner.Err(); err != nil {
		es.Abort(fmt.Errorf("anthropic: read stream: %w", err))
		return
	}

	finish()
}

// ---------------------------------------------------------------------------
// Error handling
// ---------------------------------------------------------------------------

type anthropicAPIError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type anthropicErrorWrapper struct {
	Type  string            `json:"type"`
	Error anthropicAPIError `json:"error"`
}

func (p *Provider) decodeError(resp *http.Response) error {
	var wrapper anthropicErrorWrapper
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
// compression-and-retry. Every other client error (e.g. a malformed-request
// contract violation) is reported as ErrorBadRequest so the round fails
// cleanly instead of compressing-and-looping forever.
func classifyError(code int, ae anthropicAPIError) llm.ErrorType {
	switch {
	case code == 401 || code == 403:
		return llm.ErrorAuthFailed
	case code == 429 || code == 529:
		return llm.ErrorRateLimit
	case code == 408 || code == 504:
		return llm.ErrorTimeout
	case code >= 500:
		return llm.ErrorProvider
	case code >= 400:
		if isContextLengthError(ae) {
			return llm.ErrorContextLength
		}
		return llm.ErrorBadRequest
	default:
		return llm.ErrorProvider
	}
}

// isContextLengthError reports whether a provider error denotes a context
// window overflow — the only 4xx the reason worker should answer with
// compression. Detection keys off both the structured error type and common
// message phrasing, because providers vary in how they flag the overflow.
func isContextLengthError(ae anthropicAPIError) bool {
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
	case code == 429 || code == 529:
		return llm.ErrorRateLimit
	case code == 408 || code == 504:
		return llm.ErrorTimeout
	case code >= 500:
		return llm.ErrorProvider
	default:
		return llm.ErrorBadRequest
	}
}
