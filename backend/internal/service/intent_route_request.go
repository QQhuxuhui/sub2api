package service

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
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
	// cache 为 nil 时每次都重新解析。按值传递的 view 共享同一个 cache。
	cache *intentViewCache
}

// intentViewCache 让同一请求里的关键字匹配、会话键、探针判定和未命中采集共用一次解析。
// 只缓存 body 的纯函数结果，加锁是因为分类可能在独立 goroutine 上继续读取。
type intentViewCache struct {
	mu          sync.Mutex
	turnsLoaded bool
	turns       []gjson.Result
	turnTexts   map[int]string
}

func newIntentRequestView(body []byte) intentRequestView {
	return intentRequestView{body: body, cache: &intentViewCache{}}
}

// lastUserText returns the text of the most recent user turn that has any
// (tool results and images are skipped), cut to maxChars runes (0: uncut).
// Text that coding clients inject into user turns (see intentInjectedBlocks)
// is not what the user asked, so it is removed first; a turn holding nothing
// else counts as having no text.
func (v intentRequestView) lastUserText(maxChars int) string {
	if texts := v.recentUserTexts(1); len(texts) > 0 {
		return truncateRunes(texts[0], maxChars)
	}
	return ""
}

// recentUserTexts returns the texts of up to n most recent user turns that
// have any, newest first, read the same way as lastUserText. Assistant turns
// are never included.
func (v intentRequestView) recentUserTexts(n int) []string {
	var texts []string
	turns := v.turns()
	for i := len(turns) - 1; i >= 0 && len(texts) < n; i-- {
		if !isIntentUserRole(turns[i].Get("role").String()) {
			continue
		}
		if text := stripIntentInjectedText(v.turnText(turns, i)); text != "" {
			texts = append(texts, text)
		}
	}
	if len(texts) > 0 {
		return texts
	}
	// Responses API / legacy completions with a bare string prompt.
	for _, path := range []string{"input", "prompt"} {
		if r := gjson.GetBytes(v.body, path); r.Type == gjson.String {
			if text := stripIntentInjectedText(r.String()); text != "" {
				return []string{text}
			}
		}
	}
	return nil
}

// intentInjectedBlocks match tagged blocks that clients such as Claude Code
// and Codex put into user turns on their own: reminders, IDE context, local
// command output, environment and instruction dumps. They are long and full
// of ordinary words, so leaving them in would make keywords match (and the
// classifier judge) text the user never wrote.
var intentInjectedBlocks = func() []*regexp.Regexp {
	tags := []string{
		"system-reminder", "local-command-stdout", "local-command-stderr",
		"ide_opened_file", "ide_selection", "ide_diagnostics",
		"environment_context", "user_instructions",
	}
	patterns := make([]*regexp.Regexp, 0, len(tags))
	for _, tag := range tags {
		patterns = append(patterns, regexp.MustCompile(`(?s)<`+tag+`>.*?</`+tag+`>`))
	}
	return patterns
}()

func stripIntentInjectedText(text string) string {
	if strings.IndexByte(text, '<') < 0 {
		return strings.TrimSpace(text)
	}
	for _, pattern := range intentInjectedBlocks {
		text = pattern.ReplaceAllString(text, "")
	}
	return strings.TrimSpace(text)
}

// firstUserText anchors content-derived session keys: it stays the same for
// every later turn of a conversation.
func (v intentRequestView) firstUserText() string {
	turns := v.turns()
	for i, turn := range turns {
		if !isIntentUserRole(turn.Get("role").String()) {
			continue
		}
		if text := stripIntentInjectedText(v.turnText(turns, i)); text != "" {
			return text
		}
	}
	if r := gjson.GetBytes(v.body, "input"); r.Type == gjson.String {
		return stripIntentInjectedText(r.String())
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
	if v.cache == nil {
		return intentParseTurns(v.body)
	}
	v.cache.mu.Lock()
	defer v.cache.mu.Unlock()
	if !v.cache.turnsLoaded {
		v.cache.turns = intentParseTurns(v.body)
		v.cache.turnsLoaded = true
	}
	return v.cache.turns
}

func intentParseTurns(body []byte) []gjson.Result {
	for _, path := range []string{"messages", "contents", "input"} {
		// 找不到的路径要把顶层字段全走一遍，Responses 请求的 input 会被多走两次。
		if !jsonBodyMayHaveKey(body, path) {
			continue
		}
		if r := gjson.GetBytes(body, path); r.IsArray() {
			return r.Array()
		}
	}
	return nil
}

// turnText 是 intentTurnText 的按轮缓存，turns 必须来自 v.turns()。
func (v intentRequestView) turnText(turns []gjson.Result, index int) string {
	if v.cache == nil {
		return intentTurnText(turns[index])
	}
	v.cache.mu.Lock()
	defer v.cache.mu.Unlock()
	if text, ok := v.cache.turnTexts[index]; ok {
		return text
	}
	text := intentTurnText(turns[index])
	if v.cache.turnTexts == nil {
		v.cache.turnTexts = make(map[int]string)
	}
	v.cache.turnTexts[index] = text
	return text
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
			if block.Type == gjson.String {
				text = block.String()
			} else {
				// 先看 type：图片块带着 MB 级数据，后面几次字段查找都要扫过它，
				// 而非文本块无论如何都取不到文本，可以直接跳过。
				if !isIntentTextBlockType(block.Get("type").String()) {
					continue
				}
				if block.Get("functionCall").Exists() || block.Get("functionResponse").Exists() {
					continue
				}
				text = block.Get("text").String()
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

func isIntentTextBlockType(blockType string) bool {
	switch blockType {
	case "", "text", "input_text", "output_text":
		return true
	}
	return false
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
// Only identifiers that really name ONE conversation are trusted on their own:
// a session header, a conversation id, or the session part of Claude Code's
// metadata.user_id. A bare user id is not a conversation — the same person
// starts a coding chat and a small-talk chat — and prompt_cache_key is set per
// conversation by some clients but per application by others; those two are
// therefore never used alone. Without a trustworthy identifier the
// conversation is recognized by its system prompt and opening user message,
// which later turns repeat verbatim. Two conversations that open identically
// share a decision, which for routing is the desired outcome anyway.
func intentSessionKey(apiKeyID int64, header http.Header, view intentRequestView) string {
	seed := ""
	for _, name := range intentSessionHeaders {
		if value := strings.TrimSpace(header.Get(name)); value != "" {
			seed = "h:" + value
			break
		}
	}
	if seed == "" {
		for _, path := range []string{"conversation.id", "conversation"} {
			if r := gjson.GetBytes(view.body, path); r.Type == gjson.String && strings.TrimSpace(r.String()) != "" {
				seed = "b:conversation:" + strings.TrimSpace(r.String())
				break
			}
		}
	}
	if seed == "" {
		if parsed := ParseMetadataUserID(gjson.GetBytes(view.body, "metadata.user_id").String()); parsed != nil && parsed.SessionID != "" {
			seed = "b:session:" + parsed.SessionID
		}
	}
	if seed == "" {
		first := view.firstUserText()
		if first == "" {
			return ""
		}
		seed = "c:" + stripIntentInjectedText(view.systemText()) + "\x00" + first
		// A cache key narrows the match when present, but never widens it.
		if r := gjson.GetBytes(view.body, "prompt_cache_key"); r.Type == gjson.String {
			seed += "\x00" + strings.TrimSpace(r.String())
		}
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
