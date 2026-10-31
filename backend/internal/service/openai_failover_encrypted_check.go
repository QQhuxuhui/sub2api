package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	openAIEncryptedArgsCheckRounds  = 3
	openAIEncryptedArgsCheckTimeout = 75 * time.Second
	openAIEncryptedArgsDefaultModel = "gpt-6.1-sol"
	openAIEncryptedArgsMaxBody      = 4 << 20
	openAIEncryptedArgsMaxDepth     = 32

	// Verdicts of a check.
	OpenAIEncryptedArgsSupported   = "supported"
	OpenAIEncryptedArgsUnsupported = "unsupported"
	OpenAIEncryptedArgsMixed       = "mixed"
	OpenAIEncryptedArgsUnknown     = "unknown"
)

// OpenAIEncryptedArgsCheckResult is what the check saw. Mixed upstreams exist
// (several backends behind one key), so it takes more than one round.
type OpenAIEncryptedArgsCheckResult struct {
	Model     string `json:"model"`
	Verdict   string `json:"verdict"`
	Encrypted int    `json:"encrypted"`
	Plaintext int    `json:"plaintext"`
	Failed    int    `json:"failed"`
	LastError string `json:"last_error,omitempty"`
}

// defaultOpenAIEncryptedArgsCheckModel picks a concrete model the account
// serves. Mapping keys can be patterns, which no upstream would accept. A
// pattern that does not cover the default says nothing about a request name,
// but its target is one the upstream takes. Empty means there is nothing to
// pick; the caller then needs a model from the admin.
func defaultOpenAIEncryptedArgsCheckModel(account *Account) string {
	mapping := account.GetModelMapping()
	if len(mapping) == 0 || account.IsOpenAIPassthroughEnabled() {
		return openAIEncryptedArgsDefaultModel
	}
	if _, matched := account.ResolveMappedModel(openAIEncryptedArgsDefaultModel); matched {
		return openAIEncryptedArgsDefaultModel
	}
	keys := make([]string, 0, len(mapping))
	for key := range mapping {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !strings.Contains(key, "*") {
			return key
		}
	}
	for _, key := range keys {
		if target := mapping[key]; !strings.Contains(target, "*") {
			return target
		}
	}
	return ""
}

// resolveOpenAIEncryptedArgsCheckModel returns the name to send: the one a real
// request for that model would reach the upstream with.
func resolveOpenAIEncryptedArgsCheckModel(account *Account, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		requested = defaultOpenAIEncryptedArgsCheckModel(account)
	}
	_, upstreamModel := resolveOpenAIForwardMappedModels(account, requested, false)
	if upstreamModel == "" || strings.Contains(requested, "*") || strings.Contains(upstreamModel, "*") {
		return "", infraerrors.BadRequest("OPENAI_ENCRYPTED_ARGS_CHECK_MODEL_REQUIRED", "a concrete model name is required for this account")
	}
	return upstreamModel, nil
}

func openAIEncryptedArgsCheckBody(model string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"model":               model,
		"instructions":        "You are a test harness. Always call the tool.",
		"stream":              true,
		"store":               false,
		"parallel_tool_calls": false,
		"tool_choice":         "required",
		"reasoning":           map[string]any{"effort": "low"},
		"include":             []string{"reasoning.encrypted_content"},
		"prompt_cache_key":    uuid.NewString(),
		"tools": []any{map[string]any{
			"type":        "function",
			"name":        "followup_task",
			"strict":      false,
			"description": "Send a follow-up task to an agent.",
			"parameters": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"target", "message"},
				"properties": map[string]any{
					"target":  map[string]any{"type": "string", "description": "Agent id"},
					"message": map[string]any{"type": "string", "encrypted": true, "description": "Message text to send to the target agent."},
				},
			},
		}},
		"input": []any{map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{map[string]any{
				"type": "input_text",
				"text": "Call followup_task with target=agent1 and message=probe hello 123.",
			}},
		}},
	})
}

// parseOpenAIEncryptedArgsCheckStream returns the tool's message argument.
func parseOpenAIEncryptedArgsCheckStream(body io.Reader) (string, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), openAIEncryptedArgsMaxBody)
	failure := ""
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		event := bytes.TrimSpace(line[len("data:"):])
		switch gjson.GetBytes(event, "type").String() {
		case "response.output_item.done":
			item := gjson.GetBytes(event, "item")
			if item.Get("type").String() == "function_call" {
				return gjson.Get(item.Get("arguments").String(), "message").String(), nil
			}
		case "response.failed", "error":
			failure = gjson.GetBytes(event, "response.error.message").String()
			if failure == "" {
				failure = gjson.GetBytes(event, "error.message").String()
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if failure == "" {
		failure = "no tool call in the response"
	}
	return "", fmt.Errorf("%s", failure)
}

func (s *OpenAIFailoverPolicyService) checkEncryptedArgsOnce(ctx context.Context, account *Account, model string) (string, error) {
	body, err := openAIEncryptedArgsCheckBody(model)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, openAIEncryptedArgsCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, buildOpenAIResponsesURL(account.GetOpenAIBaseURL()), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+account.GetOpenAIApiKey())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", "codex_cli_rs/0.166.0 (Ubuntu 22.4.0; x86_64) xterm")
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("session_id", uuid.NewString())
	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.upstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	limited := io.LimitReader(resp.Body, openAIEncryptedArgsMaxBody)
	if resp.StatusCode >= http.StatusBadRequest {
		raw, _ := io.ReadAll(limited)
		message := sanitizeUpstreamErrorMessage(extractUpstreamErrorMessage(raw))
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, message)
	}
	return parseOpenAIEncryptedArgsCheckStream(limited)
}

// CheckEncryptedArgs asks an API-key upstream to call a tool whose parameter is
// declared `"encrypted": true` and reports whether it answers with ciphertext.
func (s *OpenAIFailoverPolicyService) CheckEncryptedArgs(ctx context.Context, accountID int64, model string) (*OpenAIEncryptedArgsCheckResult, error) {
	account, err := s.accounts.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !account.IsOpenAIApiKey() {
		return nil, infraerrors.BadRequest("OPENAI_ENCRYPTED_ARGS_CHECK_UNSUPPORTED", "only OpenAI API-key accounts can be checked")
	}
	model, err = resolveOpenAIEncryptedArgsCheckModel(account, model)
	if err != nil {
		return nil, err
	}
	result := &OpenAIEncryptedArgsCheckResult{Model: model}
	for round := 0; round < openAIEncryptedArgsCheckRounds; round++ {
		message, err := s.checkEncryptedArgsOnce(ctx, account, model)
		switch {
		case err != nil:
			result.Failed++
			result.LastError = sanitizeUpstreamErrorMessage(err.Error())
		case strings.HasPrefix(message, openAIEncryptedContentPrefix):
			result.Encrypted++
		default:
			result.Plaintext++
		}
	}
	result.Verdict = openAIEncryptedArgsVerdict(result.Encrypted, result.Plaintext)
	return result, nil
}

func openAIEncryptedArgsVerdict(encrypted, plaintext int) string {
	switch {
	case encrypted > 0 && plaintext > 0:
		return OpenAIEncryptedArgsMixed
	case plaintext > 0:
		return OpenAIEncryptedArgsUnsupported
	case encrypted > 0:
		return OpenAIEncryptedArgsSupported
	default:
		return OpenAIEncryptedArgsUnknown
	}
}
