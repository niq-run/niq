package llm

import (
	"context"
	"fmt"
)

// LLMProvider is the core abstraction for calling language models.
type LLMProvider interface {
	// Complete performs a non-streaming completion.
	Complete(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error)

	// CompleteStream performs a streaming completion.
	// Consumer pulls events via EventStream.Next().
	CompleteStream(ctx context.Context, req *CompletionRequest) (*EventStream, error)

	// ListModels returns models known by this provider.
	ListModels(ctx context.Context) ([]ModelInfo, error)
}

// CompletionRequest wraps all inputs for an LLM call.
type CompletionRequest struct {
	Model      string
	Context    *Context
	ToolChoice ToolChoice
}

// CompletionResponse wraps the result of a non-streaming call.
type CompletionResponse struct {
	Message Message
	Usage   Usage
	Cost    Cost
}

// ---------------------------------------------------------------------------

// ErrorType categorises an LLM error.
type ErrorType string

const (
	ErrorAuthFailed    ErrorType = "authentication"
	ErrorRateLimit     ErrorType = "rate_limit"
	ErrorContextLength ErrorType = "context_length"
	ErrorBadRequest    ErrorType = "bad_request"
	ErrorProvider      ErrorType = "provider"
	ErrorTimeout       ErrorType = "timeout"
	ErrorAborted       ErrorType = "aborted"
)

// LLMError is a structured error returned by an LLM provider.
type LLMError struct {
	Type    ErrorType
	Message string
	Cause   error
}

func (e *LLMError) Error() string {
	return fmt.Sprintf("llm error [%s]: %s", e.Type, e.Message)
}

func (e *LLMError) Unwrap() error {
	return e.Cause
}
