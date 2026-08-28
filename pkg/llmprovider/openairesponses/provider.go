// Package openairesponses implements llm.LLMProvider for the OpenAI Responses
// API format, including DeepSeek's Responses-compatible endpoint.
package openairesponses

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

// Config holds the configuration for the Responses API provider.
type Config struct {
	// APIKey is the bearer token sent as Authorization: Bearer <key>.
	APIKey string

	// BaseURL is the API root. Defaults to https://api.openai.com/v1.
	// DeepSeek's Responses-compatible endpoint is https://api.deepseek.com.
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
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
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

// Provider implements llm.LLMProvider for the Responses API.
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
		baseURL: cfg.BaseURL,
		model:   cfg.Model,
		headers: cfg.Headers,
		client:  cfg.Client,
	}
}

// ---------------------------------------------------------------------------
// llm.LLMProvider implementation
// ---------------------------------------------------------------------------

// Complete performs a non-streaming Responses completion.
func (p *Provider) Complete(ctx context.Context, req *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	body, err := p.buildRequestBody(req, false)
	if err != nil {
		return nil, err
	}

	resp, err := p.do(ctx, http.MethodPost, "/responses", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, p.decodeError(resp)
	}

	var rr responsesResponse
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return nil, fmt.Errorf("responses: decode response: %w", err)
	}

	return p.toCompletionResponse(&rr), nil
}

// CompleteStream performs a streaming Responses completion.
func (p *Provider) CompleteStream(ctx context.Context, req *llm.CompletionRequest) (*llm.EventStream, error) {
	body, err := p.buildRequestBody(req, true)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("responses: create request: %w", err)
	}
	p.setHeaders(httpReq)

	httpResp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("responses: stream request: %w", err)
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
		return nil, fmt.Errorf("responses: decode models: %w", err)
	}

	models := make([]llm.ModelInfo, 0, len(mr.Data))
	for _, m := range mr.Data {
		models = append(models, llm.ModelInfo{
			ID:            m.ID,
			Name:          m.ID,
			Provider:      "openai-responses",
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
		return nil, fmt.Errorf("responses: new request: %w", err)
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

type responsesRequest struct {
	Model           string              `json:"model"`
	Instructions    string              `json:"instructions,omitempty"`
	Input           []responseInputItem `json:"input,omitempty"`
	Tools           []responseTool      `json:"tools,omitempty"`
	ToolChoice      any                 `json:"tool_choice,omitempty"`
	Temperature     *float32            `json:"temperature,omitempty"`
	MaxOutputTokens *int                `json:"max_output_tokens,omitempty"`
	TopP            *float32            `json:"top_p,omitempty"`
	Stop            []string            `json:"stop,omitempty"`
	Stream          bool                `json:"stream,omitempty"`
	Reasoning       *responsesReasoning `json:"reasoning,omitempty"`
}

type responseInputItem struct {
	Type      string                `json:"type,omitempty"`
	Role      string                `json:"role,omitempty"`
	Content   []responseContentPart `json:"content,omitempty"`
	CallID    string                `json:"call_id,omitempty"`
	Output    string                `json:"output,omitempty"`
	ID        string                `json:"id,omitempty"`
	Name      string                `json:"name,omitempty"`
	Arguments string                `json:"arguments,omitempty"`
}

type responseContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type responseTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type responsesReasoning struct {
	Effort string `json:"effort,omitempty"`
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

	cr := responsesRequest{
		Model:           model,
		Instructions:    buildInstructions(ctx),
		Input:           buildInput(ctx),
		Temperature:     ctx.Temperature,
		MaxOutputTokens: ctx.MaxTokens,
		TopP:            ctx.TopP,
		Stop:            ctx.Stop,
		Stream:          stream,
	}

	if ctx.ReasoningEffort != nil {
		cr.Reasoning = &responsesReasoning{Effort: *ctx.ReasoningEffort}
	}

	if len(ctx.Tools) > 0 {
		cr.Tools = make([]responseTool, len(ctx.Tools))
		for i, t := range ctx.Tools {
			params := t.Parameters
			if params == nil {
				params = map[string]any{"type": "object"}
			}
			cr.Tools[i] = responseTool{
				Type:        "function",
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			}
		}
		cr.ToolChoice = convertToolChoice(req.ToolChoice)
	}

	return json.Marshal(cr)
}

func buildInstructions(ctx *llm.Context) string {
	var parts []string
	if ctx.SystemPrompt != "" {
		parts = append(parts, ctx.SystemPrompt)
	}
	for _, m := range ctx.Messages {
		if m.Role == llm.RoleSystem {
			var sb strings.Builder
			for _, b := range m.Content {
				if b.Type == llm.ContentText {
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

func buildInput(ctx *llm.Context) []responseInputItem {
	var items []responseInputItem
	for _, m := range ctx.Messages {
		switch m.Role {
		case llm.RoleSystem:
			items = append(items, responseInputItem{
				Type:    "message",
				Role:    "system",
				Content: responseSystemContent(m),
			})
		case llm.RoleToolResult:
			items = append(items, responseInputItem{
				Type:   "function_call_output",
				CallID: m.ToolCallID,
				Output: responseOutputText(m),
			})
		case llm.RoleAssistant:
			var parts []responseContentPart
			var calls []responseInputItem
			for _, b := range m.Content {
				switch b.Type {
				case llm.ContentText, llm.ContentThinking:
					parts = append(parts, responseContentPart{Type: "output_text", Text: b.Text})
				case llm.ContentToolCall:
					calls = append(calls, responseInputItem{
						Type:      "function_call",
						ID:        b.ToolCallID,
						CallID:    b.ToolCallID,
						Name:      b.ToolName,
						Arguments: b.ToolArguments,
					})
				}
			}
			if len(parts) > 0 {
				items = append(items, responseInputItem{
					Type:    "message",
					Role:    "assistant",
					Content: parts,
				})
			}
			items = append(items, calls...)
		default:
			items = append(items, responseInputItem{
				Type:    "message",
				Role:    "user",
				Content: responseUserContent(m),
			})
		}
	}
	return items
}

func responseSystemContent(m llm.Message) []responseContentPart {
	var parts []responseContentPart
	for _, b := range m.Content {
		if b.Type == llm.ContentText {
			parts = append(parts, responseContentPart{Type: "input_text", Text: b.Text})
		}
	}
	return parts
}

func responseUserContent(m llm.Message) []responseContentPart {
	var parts []responseContentPart
	for _, b := range m.Content {
		switch b.Type {
		case llm.ContentText, llm.ContentThinking:
			parts = append(parts, responseContentPart{Type: "input_text", Text: b.Text})
		case llm.ContentImage:
			url := b.Data
			if b.MIMEType != "" && !strings.HasPrefix(b.Data, "data:") {
				url = "data:" + b.MIMEType + ";base64," + b.Data
			}
			parts = append(parts, responseContentPart{Type: "input_image", ImageURL: url, Detail: "auto"})
		}
	}
	return parts
}

func responseOutputText(m llm.Message) string {
	var sb strings.Builder
	for _, b := range m.Content {
		if b.Type == llm.ContentText {
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
	case "none":
		return "none"
	case "required":
		return "required"
	case "function":
		if named, ok := tc.(llm.ToolChoiceNamed); ok {
			return map[string]any{"type": "function", "name": named.Name}
		}
		return "auto"
	default:
		return "auto"
	}
}

// ---------------------------------------------------------------------------
// Response types
// ---------------------------------------------------------------------------

type responsesResponse struct {
	ID     string               `json:"id"`
	Status string               `json:"status"`
	Output []responseOutputItem `json:"output"`
	Usage  *responsesUsage      `json:"usage"`
}

type responseOutputItem struct {
	Type      string                  `json:"type"`
	ID        string                  `json:"id"`
	CallID    string                  `json:"call_id"`
	Name      string                  `json:"name"`
	Arguments json.RawMessage         `json:"arguments"`
	Role      string                  `json:"role"`
	Content   []responseOutputContent `json:"content"`
	Status    string                  `json:"status"`
}

type responseOutputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesUsage struct {
	InputTokens   int                   `json:"input_tokens"`
	OutputTokens  int                   `json:"output_tokens"`
	TotalTokens   int                   `json:"total_tokens"`
	InputDetails  *responsesUsageDetail `json:"input_tokens_details"`
	OutputDetails *responsesUsageDetail `json:"output_tokens_details"`
}

type responsesUsageDetail struct {
	CachedTokens    int `json:"cached_tokens"`
	ReasoningTokens int `json:"reasoning_tokens"`
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

func (p *Provider) toCompletionResponse(rr *responsesResponse) *llm.CompletionResponse {
	msg := llm.Message{
		Role:       llm.RoleAssistant,
		StopReason: statusToStopReason(rr.Status),
	}

	for _, out := range rr.Output {
		switch out.Type {
		case "message":
			for _, c := range out.Content {
				if c.Type == "output_text" || c.Type == "text" {
					msg.Content = append(msg.Content, llm.ContentBlock{Type: llm.ContentText, Text: c.Text})
				}
			}
		case "reasoning":
			for _, c := range out.Content {
				if c.Text != "" {
					msg.Content = append(msg.Content, llm.ContentBlock{Type: llm.ContentThinking, Text: c.Text})
				}
			}
		case "function_call":
			msg.Content = append(msg.Content, llm.ContentBlock{
				Type:          llm.ContentToolCall,
				ToolCallID:    outputCallID(out),
				ToolName:      out.Name,
				ToolArguments: rawResponseArguments(out.Arguments),
			})
		}
	}

	resp := &llm.CompletionResponse{Message: msg}
	if rr.Usage != nil {
		resp.Usage = usageFromResponses(rr.Usage)
	}
	return resp
}

func statusToStopReason(status string) string {
	switch status {
	case "completed":
		return "stop"
	case "incomplete":
		return "length"
	case "failed":
		return "error"
	default:
		return status
	}
}

func outputCallID(out responseOutputItem) string {
	if out.CallID != "" {
		return out.CallID
	}
	return out.ID
}

func rawResponseArguments(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func usageFromResponses(u *responsesUsage) llm.Usage {
	usage := llm.Usage{
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		TotalTokens:  u.TotalTokens,
	}
	if u.InputDetails != nil && u.InputDetails.CachedTokens > 0 {
		n := u.InputDetails.CachedTokens
		usage.CacheReadTokens = &n
	}
	return usage
}

// ---------------------------------------------------------------------------
// SSE streaming
// ---------------------------------------------------------------------------

type responseStreamEvent struct {
	Type        string              `json:"type"`
	Delta       string              `json:"delta"`
	ItemID      string              `json:"item_id"`
	OutputIndex int                 `json:"output_index"`
	Item        *responseOutputItem `json:"item"`
	Arguments   json.RawMessage     `json:"arguments"`
	Response    *responsesResponse  `json:"response"`
	Error       *responseError      `json:"error"`
}

type responseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type streamCall struct {
	id       string
	name     string
	nameDone bool
	args     strings.Builder
	argsDone bool
}

type streamState struct {
	textStarted     bool
	thinkingStarted bool
	textBuf         strings.Builder
	thinkingBuf     strings.Builder
	calls           map[string]*streamCall
	usage           *llm.Usage
	stopReason      string
}

func (s *streamState) callFor(outputIndex int, itemID string) *streamCall {
	key := callKey(outputIndex, itemID)
	if pc, ok := s.calls[key]; ok {
		return pc
	}
	pc := &streamCall{}
	s.calls[key] = pc
	return pc
}

func callKey(outputIndex int, itemID string) string {
	if itemID != "" {
		return "id:" + itemID
	}
	return fmt.Sprintf("idx:%d", outputIndex)
}

func (p *Provider) readStream(body io.ReadCloser, es *llm.EventStream) {
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	state := &streamState{calls: make(map[string]*streamCall)}

	finish := func() {
		if state.thinkingStarted {
			es.Push(llm.EventThinkingEnd{})
		}
		if state.textStarted {
			es.Push(llm.EventTextEnd{})
		}
		for _, pc := range state.calls {
			if !pc.argsDone {
				es.Push(llm.EventToolCallEnd{Arguments: pc.args.String()})
			}
		}

		msg := llm.Message{
			Role:       llm.RoleAssistant,
			Usage:      state.usage,
			StopReason: statusToStopReason(state.stopReason),
		}
		if state.thinkingBuf.Len() > 0 {
			msg.Content = append(msg.Content, llm.ContentBlock{Type: llm.ContentThinking, Text: state.thinkingBuf.String()})
		}
		if state.textBuf.Len() > 0 {
			msg.Content = append(msg.Content, llm.ContentBlock{Type: llm.ContentText, Text: state.textBuf.String()})
		}
		for _, pc := range state.calls {
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
		var evt responseStreamEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			es.Abort(fmt.Errorf("responses: parse chunk: %w", err))
			return
		}

		switch evt.Type {
		case "response.output_text.delta":
			if !state.textStarted {
				es.Push(llm.EventTextStart{})
				state.textStarted = true
			}
			state.textBuf.WriteString(evt.Delta)
			es.Push(llm.EventTextDelta{Delta: evt.Delta})
		case "response.output_text.done":
			if state.textStarted {
				es.Push(llm.EventTextEnd{})
				state.textStarted = false
			}
		case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
			if !state.thinkingStarted {
				es.Push(llm.EventThinkingStart{})
				state.thinkingStarted = true
			}
			state.thinkingBuf.WriteString(evt.Delta)
			es.Push(llm.EventThinkingDelta{Delta: evt.Delta})
		case "response.reasoning_text.done", "response.reasoning_summary_text.done":
			if state.thinkingStarted {
				es.Push(llm.EventThinkingEnd{})
				state.thinkingStarted = false
			}
		case "response.output_item.added":
			if evt.Item != nil && evt.Item.Type == "function_call" {
				pc := state.callFor(evt.OutputIndex, evt.ItemID)
				if pc.id == "" {
					pc.id = outputCallID(*evt.Item)
				}
				if evt.Item.Name != "" && !pc.nameDone {
					pc.name = evt.Item.Name
					pc.nameDone = true
					es.Push(llm.EventToolCallStart{ToolName: pc.name})
				}
			}
		case "response.function_call_arguments.delta":
			pc := state.callFor(evt.OutputIndex, evt.ItemID)
			if !pc.nameDone {
				pc.nameDone = true
				es.Push(llm.EventToolCallStart{ToolName: pc.name})
			}
			pc.args.WriteString(evt.Delta)
			es.Push(llm.EventToolCallDelta{Delta: evt.Delta})
		case "response.function_call_arguments.done":
			pc := state.callFor(evt.OutputIndex, evt.ItemID)
			if !pc.nameDone {
				pc.nameDone = true
				es.Push(llm.EventToolCallStart{ToolName: pc.name})
			}
			if len(evt.Arguments) > 0 {
				pc.args.Reset()
				pc.args.WriteString(rawResponseArguments(evt.Arguments))
			}
			if !pc.argsDone {
				es.Push(llm.EventToolCallEnd{Arguments: pc.args.String()})
				pc.argsDone = true
			}
		case "response.completed":
			if evt.Response != nil && evt.Response.Usage != nil {
				u := usageFromResponses(evt.Response.Usage)
				state.usage = &u
			}
			state.stopReason = "completed"
			finish()
			return
		case "response.incomplete":
			if evt.Response != nil && evt.Response.Usage != nil {
				u := usageFromResponses(evt.Response.Usage)
				state.usage = &u
			}
			state.stopReason = "incomplete"
			finish()
			return
		case "response.failed":
			msg := "responses stream failed"
			if evt.Error != nil && evt.Error.Message != "" {
				msg = evt.Error.Message
			}
			es.Abort(&llm.LLMError{Type: llm.ErrorProvider, Message: msg})
			return
		case "error":
			msg := "responses stream error"
			if evt.Error != nil && evt.Error.Message != "" {
				msg = evt.Error.Message
			}
			es.Abort(&llm.LLMError{Type: errorTypeForMessage(evt.Error), Message: msg})
			return
		}
	}

	if err := scanner.Err(); err != nil {
		es.Abort(fmt.Errorf("responses: read stream: %w", err))
		return
	}

	finish()
}

func errorTypeForMessage(err *responseError) llm.ErrorType {
	if err == nil {
		return llm.ErrorProvider
	}
	switch err.Code {
	case "authentication_error":
		return llm.ErrorAuthFailed
	case "rate_limit_exceeded":
		return llm.ErrorRateLimit
	case "context_length_exceeded":
		return llm.ErrorContextLength
	default:
		return llm.ErrorProvider
	}
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
func classifyError(code int, ae apiError) llm.ErrorType {
	switch {
	case code == 401 || code == 403:
		return llm.ErrorAuthFailed
	case code == 429:
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
	case code == 408 || code == 504:
		return llm.ErrorTimeout
	case code >= 500:
		return llm.ErrorProvider
	default:
		return llm.ErrorBadRequest
	}
}
