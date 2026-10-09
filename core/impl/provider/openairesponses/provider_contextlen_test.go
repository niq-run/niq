package openairesponses

import "testing"

// TestIsContextLengthErrorRecognizesDeepSeekCode verifies DeepSeek's stable
// "11115 prompt is too long" code is treated as a context overflow on the
// Responses-compatible endpoint too, so the reason worker compresses and retries
// instead of failing as a generic bad request.
func TestIsContextLengthErrorRecognizesDeepSeekCode(t *testing.T) {
	cases := []struct {
		name    string
		ae      apiError
		wantCtx bool
	}{
		{"deepseek numeric code 11115", apiError{Message: "Prompt is too long", Type: "invalid_request_error", Code: float64(11115)}, true},
		{"deepseek string code", apiError{Message: "Prompt is too long", Type: "invalid_request_error", Code: "11115"}, true},
		{"deepseek wrong code", apiError{Message: "other error", Type: "invalid_request_error", Code: float64(40012)}, false},
		{"structured context_length_exceeded", apiError{Message: "you reached the limit", Type: "context_length_exceeded"}, true},
		{"message fallback", apiError{Message: "Prompt is too long", Type: "invalid_request_error"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isContextLengthError(tc.ae); got != tc.wantCtx {
				t.Fatalf("isContextLengthError(%+v) = %v, want %v", tc.ae, got, tc.wantCtx)
			}
		})
	}
}
