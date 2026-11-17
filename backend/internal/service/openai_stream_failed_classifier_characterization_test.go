//go:build unit

package service

import "testing"

// The verdicts the in-stream terminal-event classifiers gave before the
// configurable retry-code list was added. The list is a separate, additive
// check; these functions must keep answering exactly this.
func TestOpenAIStreamTerminalClassifiersCharacterization(t *testing.T) {
	cases := []struct {
		name         string
		payload      string
		failedEvent  bool
		errorEvent   bool
		failStatus   int
		semanticCode int
	}{
		{"model_not_found typed", `{"type":"response.failed","response":{"error":{"code":"model_not_found","type":"invalid_request_error","message":"The model does not exist"}}}`, false, false, 502, 400},
		{"model_not_found untyped", `{"type":"response.failed","response":{"error":{"code":"model_not_found","message":"The model does not exist"}}}`, true, false, 502, 502},
		{"overloaded", `{"type":"response.failed","response":{"error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded"}}}`, true, true, 503, 503},
		{"rate limit", `{"type":"response.failed","response":{"error":{"code":"rate_limit_exceeded","type":"invalid_request_error","message":"Rate limit reached"}}}`, true, true, 429, 429},
		{"unauthorized", `{"type":"response.failed","response":{"error":{"code":"invalid_api_key","message":"Incorrect API key provided"}}}`, true, true, 401, 401},
		{"content policy", `{"type":"response.failed","response":{"error":{"code":"content_policy_violation","message":"Your request was rejected by the safety system"}}}`, false, false, 502, 502},
		{"content filter", `{"type":"response.failed","response":{"error":{"code":"content_filter","message":"The response was filtered"}}}`, true, false, 502, 502},
		{"cyber", `{"type":"response.failed","response":{"error":{"code":"cyber_policy","message":"This request has been flagged for potentially high-risk cyber activity"}}}`, false, false, 502, 502},
		{"context window", `{"type":"response.failed","response":{"error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model"}}}`, false, false, 502, 400},
		{"enable_thinking parameter", `{"type":"response.failed","response":{"error":{"code":"invalid_parameter","type":"invalid_request_error","message":"parameter.enable_thinking must be set to false for non-streaming calls"}}}`, false, false, 502, 400},
		{"unknown", `{"type":"response.failed","response":{"error":{"code":"internal_error","message":"upstream unavailable"}}}`, true, false, 502, 502},
		{"no error object", `{"type":"response.failed","response":{}}`, true, false, 502, 502},
		{"empty payload", ``, true, false, 502, 502},
		{"bare error model_not_found", `{"type":"error","error":{"code":"model_not_found","type":"invalid_request_error","message":"The model does not exist"}}`, false, false, 502, 400},
		{"bare error overloaded", `{"type":"error","error":{"code":"server_is_overloaded","message":"overloaded"}}`, true, true, 503, 503},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(tc.payload)
			message := extractOpenAISSEErrorMessage(payload)
			if got := openAIStreamFailedEventShouldFailover(payload, message); got != tc.failedEvent {
				t.Errorf("openAIStreamFailedEventShouldFailover = %v, want %v", got, tc.failedEvent)
			}
			if got := openAIStreamErrorEventShouldFailover(payload, message); got != tc.errorEvent {
				t.Errorf("openAIStreamErrorEventShouldFailover = %v, want %v", got, tc.errorEvent)
			}
			if got := openAIStreamFailureStatus(payload, message); got != tc.failStatus {
				t.Errorf("openAIStreamFailureStatus = %d, want %d", got, tc.failStatus)
			}
			if got := openAIStreamFailedEventSemanticStatus(payload, message); got != tc.semanticCode {
				t.Errorf("openAIStreamFailedEventSemanticStatus = %d, want %d", got, tc.semanticCode)
			}
		})
	}
}
