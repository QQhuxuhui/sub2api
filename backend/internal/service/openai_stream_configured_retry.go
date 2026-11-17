package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// An upstream can answer HTTP 200 and then, before any output, push a
// `response.failed` event whose error code says this backend can not serve
// the request right now (model_not_found from a relay whose pool is briefly
// empty). The stream classifiers hand such an event to the customer because
// its type is invalid_request_error.
//
// The admin keeps a list of error codes (the OpenAI tab of the failover-rules
// page) for which the gateway retries instead: pool-mode accounts on the same
// account, then on another one, without touching the account's state. When
// every attempt fails the customer gets HTTP 400 with the upstream error.
//
// Everything about that list lives in this file. The stream readers only call
// openAIStreamConfiguredRetryError before their own failover branch.

// OpenAIStreamConfiguredRetryReason marks a failover caused by the list.
const OpenAIStreamConfiguredRetryReason = GatewayFailureReason("openai_stream_configured_retry")

const (
	openAIStreamFailedRetryMaxCodes   = 20
	openAIStreamFailedRetryMaxCodeLen = 64
)

func defaultOpenAIStreamFailedRetryCodes() []string {
	return []string{"model_not_found"}
}

// openAIModerationMarkers are never retried, whatever the list says: the
// refusal is about the request content and every account answers the same.
var openAIModerationMarkers = []string{
	"content_policy",
	"content_filter",
	"policy",
	"safety",
	"high-risk cyber",
	"not allowed",
	"violat",
}

func openAIContainsModerationMarker(text string) bool {
	text = strings.ToLower(text)
	for _, marker := range openAIModerationMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// normalizeOpenAIStreamFailedRetryCode lowercases and validates one list
// entry. Codes that could never take effect are rejected so the page does not
// suggest otherwise.
func normalizeOpenAIStreamFailedRetryCode(code string) (string, error) {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" || len(code) > openAIStreamFailedRetryMaxCodeLen {
		return "", fmt.Errorf("error code must be 1 to %d characters", openAIStreamFailedRetryMaxCodeLen)
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '.' && r != '-' {
			return "", fmt.Errorf("error code %q may only contain a-z, 0-9, '_', '.' and '-'", code)
		}
	}
	if openAIContainsModerationMarker(code) {
		return "", fmt.Errorf("error code %q is a content moderation refusal and is never retried", code)
	}
	if code == "context_length_exceeded" {
		return "", fmt.Errorf("error code %q is a context window error and is never retried", code)
	}
	return code, nil
}

// normalizeOpenAIStreamFailedRetryCodes trims, lowercases and deduplicates the
// list. A nil list means "not set" and becomes the default; an empty one turns
// the feature off. In lenient mode (stored values) bad entries are dropped
// instead of failing the whole config.
func normalizeOpenAIStreamFailedRetryCodes(codes []string, lenient bool) ([]string, error) {
	if codes == nil {
		return defaultOpenAIStreamFailedRetryCodes(), nil
	}
	normalized := make([]string, 0, len(codes))
	seen := make(map[string]struct{}, len(codes))
	for _, raw := range codes {
		code, err := normalizeOpenAIStreamFailedRetryCode(raw)
		if err != nil {
			if lenient {
				continue
			}
			return nil, err
		}
		if _, dup := seen[code]; dup {
			continue
		}
		seen[code] = struct{}{}
		normalized = append(normalized, code)
	}
	if len(normalized) > openAIStreamFailedRetryMaxCodes {
		if !lenient {
			return nil, fmt.Errorf("at most %d stream retry error codes", openAIStreamFailedRetryMaxCodes)
		}
		normalized = normalized[:openAIStreamFailedRetryMaxCodes]
	}
	return normalized, nil
}

func compileOpenAIStreamFailedRetryCodes(codes []string) map[string]struct{} {
	set := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		set[code] = struct{}{}
	}
	return set
}

type openAIStreamFailedRetryCodesContextKey struct{}

// WithOpenAIStreamFailedRetryCodes carries the list to the forward path. The
// set is shared between requests and must not be modified.
func WithOpenAIStreamFailedRetryCodes(ctx context.Context, codes map[string]struct{}) context.Context {
	return context.WithValue(ctx, openAIStreamFailedRetryCodesContextKey{}, codes)
}

// openAIStreamFailedRetryCodesFromContext returns nil for a request that did
// not go through an entry point that mounts the list.
func openAIStreamFailedRetryCodesFromContext(ctx context.Context) map[string]struct{} {
	if ctx == nil {
		return nil
	}
	codes, _ := ctx.Value(openAIStreamFailedRetryCodesContextKey{}).(map[string]struct{})
	return codes
}

// openAIStreamConfiguredRetryEligible mirrors the precondition under which
// shouldFailoverOpenAIUpstreamResponse fails over an HTTP 400 model_not_found.
// TestOpenAIStreamConfiguredRetryEligibleMatchesHTTPPath keeps the two equal.
func (s *OpenAIGatewayService) openAIStreamConfiguredRetryEligible(account *Account) bool {
	return s != nil && s.accountRepo != nil && account != nil && account.IsOpenAICompatible()
}

// openAIStreamTerminalErrorField reads a field of the terminal event's error,
// trimmed. Only the structured fields count: no embedded JSON, no message
// parsing.
func openAIStreamTerminalErrorField(payload []byte, field string) string {
	value := strings.TrimSpace(gjson.GetBytes(payload, "response.error."+field).String())
	if value == "" {
		value = strings.TrimSpace(gjson.GetBytes(payload, "error."+field).String())
	}
	return value
}

// openAIStreamConfiguredRetryError returns the failover error for a
// pre-output `response.failed` whose error code is on the admin's list, nil
// otherwise. A nil result leaves the event to the caller's existing logic.
// The caller is responsible for only asking before client output started.
func (s *OpenAIGatewayService) openAIStreamConfiguredRetryError(
	c *gin.Context,
	account *Account,
	passthrough bool,
	eventType string,
	payload []byte,
	message string,
	upstreamRequestID string,
	responseHeaders http.Header,
) *UpstreamFailoverError {
	if strings.TrimSpace(eventType) != "response.failed" || c == nil || c.Request == nil {
		return nil
	}
	codes := openAIStreamFailedRetryCodesFromContext(c.Request.Context())
	if len(codes) == 0 {
		return nil
	}
	code := openAIStreamTerminalErrorField(payload, "code")
	if _, listed := codes[strings.ToLower(code)]; !listed {
		return nil
	}
	if !s.openAIStreamConfiguredRetryEligible(account) {
		return nil
	}
	if hit, _, _ := detectOpenAICyberPolicy(payload); hit {
		return nil
	}
	if isOpenAIContextWindowError(message, payload) {
		return nil
	}
	errType := openAIStreamTerminalErrorField(payload, "type")
	if openAIContainsModerationMarker(code + " " + errType + " " + message) {
		return nil
	}

	message = s.recordOpenAIStreamUpstreamError(c, account, passthrough, upstreamRequestID, "failover", payload, message)
	if errType == "" {
		errType = openAIUpstreamClientErrorFallbackType
	}
	body, _ := json.Marshal(gin.H{
		"error": gin.H{
			"code":    code,
			"type":    errType,
			"message": message,
		},
	})
	var headers http.Header
	if responseHeaders != nil {
		headers = responseHeaders.Clone()
	}
	return &UpstreamFailoverError{
		StatusCode:             http.StatusBadRequest,
		ResponseBody:           body,
		ResponseHeaders:        headers,
		Reason:                 OpenAIStreamConfiguredRetryReason,
		Scope:                  GatewayFailureScopeRequest,
		NextAccountAction:      NextAccountRetry,
		RetryableOnSameAccount: account.IsPoolMode(),
	}
}
