package openai

import (
	"testing"
)

// TestIsContextLengthErrorRecognizesDeepSeekTooLong verifies the DeepSeek
// context-window overflow ("11115 prompt is too long") is classified as an
// overflow so the reason worker answers with compression-and-retry instead of
// treating it as an opaque bad request.
func TestIsContextLengthErrorRecognizesDeepSeekTooLong(t *testing.T) {
	cases := []struct {
		name    string
		ae      apiError
		wantCtx bool
	}{
		{"deepseek prompt too long", apiError{Message: "Prompt is too long", Type: "invalid_request_error"}, true},
		{"deepseek code-only message", apiError{Message: "11115 prompt is too long", Type: "invalid_request_error"}, true},
		{"deepseek numeric code 11115", apiError{Message: "Prompt is too long", Type: "invalid_request_error", Code: float64(11115)}, true},
		{"deepseek quoted string code", apiError{Message: "Prompt is too long", Type: "invalid_request_error", Code: "11115"}, true},
		{"deepseek wrong code stays bad request", apiError{Message: "other request error", Type: "invalid_request_error", Code: float64(40012)}, false},
		{"structured context_length_exceeded", apiError{Message: "you reached the limit", Type: "context_length_exceeded"}, true},
		{"standard openai max context", apiError{Message: "This model's maximum context length is 128000 tokens", Type: ""}, true},
		{"volcan-ark exceeds maximum length", apiError{Message: "Input length 1600084 exceeds the maximum length 1048566", Type: "BadRequest", Code: "InvalidParameter"}, true},
		{"unrelated malformed request stays bad request", apiError{Message: "reasoning_content must be passed back", Type: "invalid_request_error"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isContextLengthError(tc.ae); got != tc.wantCtx {
				t.Fatalf("isContextLengthError(%+v) = %v, want %v", tc.ae, got, tc.wantCtx)
			}
		})
	}
}
