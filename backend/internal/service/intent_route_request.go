package service

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"github.com/tidwall/gjson"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// intentRequestView is the protocol-agnostic reading of a chat-style request
// body that intent routing needs: what the user last asked, and what makes
// two requests part of the same conversation.
//
// Claude Messages, OpenAI Chat Completions, OpenAI Responses and Gemini
// generateContent all carry "a system prompt" and "a list of turns"; only the
// field names differ, so one tolerant reader covers them without depending on
// any protocol-specific parser.
type intentRequestView struct {
	body []byte
}

// lastUserText returns the text of the most recent user turn that has any
// (tool results and images are skipped), cut to maxChars runes.
func (v intentRequestView) lastUserText(maxChars int) string {
	turns := v.turns()
	for i := len(turns) - 1; i >= 0; i-- {
		if !isIntentUserRole(turns[i].Get("role").String()) {
			continue
		}
		if text := intentTurnText(turns[i]); text != "" {
			return truncateRunes(text, maxChars)
		}
	}
	// Responses API / legacy completions with a bare string prompt.
	for _, path := range []string{"input", "prompt"} {
		if r := gjson.GetBytes(v.body, path); r.Type == gjson.String {
			if text := strings.TrimSpace(r.String()); text != "" {
				return truncateRunes(text, maxChars)
			}
		}
	}
	return ""
}

// firstUserText anchors content-derived session keys: it stays the same for
// every later turn of a conversation.
func (v intentRequestView) firstUserText() string {
	for _, turn := range v.turns() {
		if !isIntentUserRole(turn.Get("role").String()) {
			continue
		}
		if text := intentTurnText(turn); text != "" {
			return text
		}
	}
	if r := gjson.GetBytes(v.body, "input"); r.Type == gjson.String {
		return strings.TrimSpace(r.String())
	}
	return ""
}

func (v intentRequestView) systemText() string {
	for _, path := range []string{"system", "instructions", "systemInstruction", "system_instruction"} {
		if r := gjson.GetBytes(v.body, path); r.Exists() {
			if text := intentContentText(r); text != "" {
				return text
			}
		}
	}
	// Chat Completions keeps the system prompt as a turn.
	for _, turn := range v.turns() {
		if role := turn.Get("role").String(); role == "system" || role == "developer" {
			if text := intentTurnText(turn); text != "" {
				return text
			}
		}
	}
	return ""
}

func (v intentRequestView) turns() []gjson.Result {
	for _, path := range []string{"messages", "contents", "input"} {
		if r := gjson.GetBytes(v.body, path); r.IsArray() {
			return r.Array()
		}
	}
	return nil
}

func isIntentUserRole(role string) bool {
	// Gemini allows a missing role on single-turn requests.
	return role == "user" || role == ""
}

func intentTurnText(turn gjson.Result) string {
	for _, key := range []string{"content", "parts"} {
		if r := turn.Get(key); r.Exists() {
			if text := intentContentText(r); text != "" {
				return text
			}
		}
	}
	return ""
}

// intentContentText flattens a string, a block list or a {parts:[...]} object
// to its text, ignoring non-text blocks (images, tool calls, tool results).
func intentContentText(r gjson.Result) string {
	switch {
	case r.Type == gjson.String:
		return strings.TrimSpace(r.String())
	case r.IsObject():
		if parts := r.Get("parts"); parts.Exists() {
			return intentContentText(parts)
		}
		return strings.TrimSpace(r.Get("text").String())
	case r.IsArray():
		var b strings.Builder
		for _, block := range r.Array() {
			var text string
			switch {
			case block.Type == gjson.String:
				text = block.String()
			case block.Get("functionCall").Exists() || block.Get("functionResponse").Exists():
				continue
			default:
				switch block.Get("type").String() {
				case "", "text", "input_text", "output_text":
					text = block.Get("text").String()
				}
			}
			if text = strings.TrimSpace(text); text != "" {
				if b.Len() > 0 {
					_ = b.WriteByte('\n')
				}
				_, _ = b.WriteString(text)
			}
		}
		return b.String()
	}
	return ""
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

// intentSessionHeaders are client-supplied conversation identifiers, the same
// ones sticky-session hashing honors.
var intentSessionHeaders = []string{"session_id", "session-id", "conversation_id", "x-session-id", "x-conversation-id", "x-session-affinity"}

// intentSessionKey identifies the conversation a request belongs to. It is
// scoped by API key, so two users can never share a routing decision.
//
// An explicit identifier wins; otherwise the conversation is recognized by its
// system prompt and opening user message, which later turns repeat verbatim.
// Two conversations that open identically therefore share a decision — for
// routing purposes that is the desired outcome anyway.
func intentSessionKey(apiKeyID int64, header http.Header, view intentRequestView) string {
	seed := ""
	for _, name := range intentSessionHeaders {
		if value := strings.TrimSpace(header.Get(name)); value != "" {
			seed = "h:" + value
			break
		}
	}
	if seed == "" {
		for _, path := range []string{"metadata.user_id", "prompt_cache_key", "conversation.id", "conversation"} {
			if r := gjson.GetBytes(view.body, path); r.Type == gjson.String && strings.TrimSpace(r.String()) != "" {
				seed = "b:" + path + ":" + strings.TrimSpace(r.String())
				break
			}
		}
	}
	if seed == "" {
		first := view.firstUserText()
		if first == "" {
			return ""
		}
		seed = "c:" + view.systemText() + "\x00" + first
	}
	sum := sha256.Sum256([]byte(strconv.FormatInt(apiKeyID, 10) + "\x00" + seed))
	return hex.EncodeToString(sum[:16])
}

// intentNoneLabel is what the classifier answers when no rule applies.
const intentNoneLabel = "NONE"

// matchIntentLabel maps a model's free-form answer onto a rule name. Models
// add quotes, punctuation or a short sentence around the label; an answer is
// accepted when it names exactly one rule, and rejected when ambiguous.
func matchIntentLabel(answer string, rules []domain.IntentRule) (string, bool) {
	cleaned := strings.TrimFunc(strings.TrimSpace(answer), func(r rune) bool {
		return unicode.IsPunct(r) || unicode.IsSpace(r) || unicode.IsSymbol(r)
	})
	if cleaned == "" {
		return "", false
	}
	for _, rule := range rules {
		if strings.EqualFold(cleaned, strings.TrimSpace(rule.Name)) {
			return rule.Name, true
		}
	}
	if strings.EqualFold(cleaned, intentNoneLabel) {
		return "", true
	}
	lower := strings.ToLower(cleaned)
	matched := ""
	for _, rule := range rules {
		name := strings.ToLower(strings.TrimSpace(rule.Name))
		if name != "" && strings.Contains(lower, name) {
			if matched != "" {
				return "", false
			}
			matched = rule.Name
		}
	}
	if matched != "" {
		return matched, true
	}
	if strings.Contains(lower, strings.ToLower(intentNoneLabel)) {
		return "", true
	}
	return "", false
}
