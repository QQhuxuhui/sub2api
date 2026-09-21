package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

const (
	// IntentRouteInternalHeader marks the classification request itself. The
	// classifier usually calls this very server (loopback), so without the
	// marker a misconfigured router could classify its own classifier calls.
	IntentRouteInternalHeader = "X-Sub2api-Intent-Classifier"

	intentClassifierMaxAnswerBytes = 64 << 10
	// Generous on purpose: some cheap models spend tokens on hidden reasoning
	// before the one-word answer.
	intentClassifierMaxTokens = 256
)

// intentClassifier asks a model which rule a piece of user text belongs to.
type intentClassifier interface {
	// Classify returns the raw model answer.
	Classify(ctx context.Context, cfg *intentRouterConfig, text string) (string, error)
}

type httpIntentClassifier struct {
	client *http.Client
	// selfBaseURL is used when a router leaves classifier_base_url empty.
	selfBaseURL string
}

func buildIntentClassifierPrompt(rules []domain.IntentRule) string {
	var b strings.Builder
	_, _ = b.WriteString("You are a request classifier. Read the user's message and decide which ONE of the labels below describes it best.\n\nLabels:\n")
	for _, rule := range rules {
		_, _ = fmt.Fprintf(&b, "- %s: %s\n", strings.TrimSpace(rule.Name), strings.TrimSpace(rule.Description))
	}
	_, _ = fmt.Fprintf(&b, "\nAnswer with the label only, exactly as written above. If none of the labels fits, answer %s. Do not answer the user's message, do not explain.", intentNoneLabel)
	return b.String()
}

func (c *httpIntentClassifier) Classify(ctx context.Context, cfg *intentRouterConfig, text string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.ClassifierBaseURL), "/")
	if base == "" {
		base = c.selfBaseURL
	}
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("classifier base url is not an http(s) url")
	}
	system := buildIntentClassifierPrompt(cfg.activeRules())
	// The message is data to label, not an instruction to follow.
	user := "Message to classify:\n<<<\n" + text + "\n>>>"

	var (
		endpoint string
		payload  any
		answerAt string
	)
	switch cfg.ClassifierProtocol {
	case domain.IntentClassifierProtocolGemini:
		endpoint = base + "/v1beta/models/" + url.PathEscape(cfg.ClassifierModel) + ":generateContent"
		payload = map[string]any{
			"systemInstruction": map[string]any{"parts": []map[string]any{{"text": system}}},
			"contents":          []map[string]any{{"role": "user", "parts": []map[string]any{{"text": user}}}},
			"generationConfig":  map[string]any{"temperature": 0, "maxOutputTokens": intentClassifierMaxTokens},
		}
		answerAt = "candidates.0.content.parts.#.text"
	default:
		endpoint = base + "/v1/chat/completions"
		payload = map[string]any{
			"model":       cfg.ClassifierModel,
			"stream":      false,
			"temperature": 0,
			"max_tokens":  intentClassifierMaxTokens,
			"messages": []map[string]any{
				{"role": "system", "content": system},
				{"role": "user", "content": user},
			},
		}
		answerAt = "choices.0.message.content"
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(IntentRouteInternalHeader, "1")
	if cfg.ClassifierProtocol == domain.IntentClassifierProtocolGemini {
		req.Header.Set("x-goog-api-key", cfg.ClassifierAPIKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+cfg.ClassifierAPIKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("classifier request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, intentClassifierMaxAnswerBytes))
	if err != nil {
		return "", fmt.Errorf("classifier read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("classifier http %d: %s", resp.StatusCode, truncateRunes(strings.TrimSpace(string(body)), 200))
	}
	answer := gjson.GetBytes(body, answerAt)
	if answer.IsArray() {
		parts := make([]string, 0, len(answer.Array()))
		for _, part := range answer.Array() {
			parts = append(parts, part.String())
		}
		return strings.TrimSpace(strings.Join(parts, "")), nil
	}
	return strings.TrimSpace(answer.String()), nil
}
