package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// Gemini 原生路径「上游成功响应却没有可计费用量」的取证。
//
// 背景：handleNativeNonStreamingResponse / handleNativeStreamingResponse 在响应里找不到
// usageMetadata 时静默返回空用量，本地记 0 而上游照常扣费，事后没有任何线索。这里只观察
// 上游原始响应（OAuth 解包后、图片 usage 修正前），在缺失或全零时记一条 warn 日志，
// 不改变计费与写给客户端的任何字节。

const (
	geminiUsageEvidenceContextKey = "gemini_usage_evidence"

	geminiUsageEvidenceExitNonStream       = "native_non_stream"
	geminiUsageEvidenceExitStream          = "native_stream"
	geminiUsageEvidenceExitStreamAggregate = "native_stream_aggregate"

	geminiUsageEvidenceReasonMissing = "missing_usage_metadata"
	geminiUsageEvidenceReasonZero    = "zero_usage_metadata"

	// 所有样本合计的字节预算。
	geminiUsageEvidenceSampleBudget = 2048
	// 尾部保留的 data 载荷样本条数。
	geminiUsageEvidenceTailSize = 3
)

type geminiUsageEvidenceSample struct {
	text        string
	originalLen int
	// capturedTruncated 表示入库时脱敏后的文本已超过总预算被截断。
	capturedTruncated bool
}

type geminiUsageEvidenceCounts struct {
	prompt     int64
	candidates int64
	thoughts   int64
	cached     int64
	total      int64
}

func (c geminiUsageEvidenceCounts) allZero() bool {
	return c.prompt == 0 && c.candidates == 0 && c.thoughts == 0 && c.cached == 0 && c.total == 0
}

type geminiUsageEvidence struct {
	exit        string
	contentType string
	bytesRead   int64

	dataLines     int
	jsonPayloads  int
	parseFailures int
	blankLines    int
	commentLines  int
	doneMarkers   int
	nonDataLines  int

	firstNonData *geminiUsageEvidenceSample
	tail         []geminiUsageEvidenceSample // 环形，最多 geminiUsageEvidenceTailSize 条
	tailNext     int

	usageSeen bool
	lastUsage geminiUsageEvidenceCounts

	finishReason string
	blockReason  string
	errorPresent bool
	errorCode    string
	errorStatus  string
	modelVersion string
	responseID   string
}

// beginGeminiUsageEvidence 为本次 ForwardNative 调用创建新的取证收集器并挂到 gin context。
// 每次调用都重建，避免换号重试间串状态。
func beginGeminiUsageEvidence(c *gin.Context, exit, contentType string) *geminiUsageEvidence {
	ev := &geminiUsageEvidence{exit: exit, contentType: contentType}
	if c != nil {
		c.Set(geminiUsageEvidenceContextKey, ev)
	}
	return ev
}

// clearGeminiUsageEvidence 移除 context 中的收集器（非生成类 action 或新一轮尝试开始时）。
func clearGeminiUsageEvidence(c *gin.Context) {
	if c == nil {
		return
	}
	if _, ok := c.Get(geminiUsageEvidenceContextKey); ok {
		c.Set(geminiUsageEvidenceContextKey, (*geminiUsageEvidence)(nil))
	}
}

func geminiUsageEvidenceFromContext(c *gin.Context) *geminiUsageEvidence {
	if c == nil {
		return nil
	}
	v, ok := c.Get(geminiUsageEvidenceContextKey)
	if !ok {
		return nil
	}
	ev, _ := v.(*geminiUsageEvidence)
	return ev
}

func isGeminiUsageEvidenceAction(action string) bool {
	return action == "generateContent" || action == "streamGenerateContent"
}

func (e *geminiUsageEvidence) addBytes(n int) {
	if e == nil || n <= 0 {
		return
	}
	e.bytesRead += int64(n)
}

// observeLine 对流式/聚合出口读到的一行做分类计数。
// data 行的载荷需调用方再走 observeDataPayload（解包之后）。
func (e *geminiUsageEvidence) observeLine(line string) {
	if e == nil {
		return
	}
	e.addBytes(len(line))
	trimmed := strings.TrimRight(line, "\r\n")
	switch {
	case strings.TrimSpace(trimmed) == "":
		e.blankLines++
	case strings.HasPrefix(trimmed, "data:"):
		e.dataLines++
		if strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")) == "[DONE]" {
			e.doneMarkers++
		}
	case strings.HasPrefix(trimmed, ":"):
		e.commentLines++
	default:
		e.nonDataLines++
		if e.firstNonData == nil {
			s := e.makeSample([]byte(trimmed))
			e.firstNonData = &s
		}
	}
}

// observeParseFailure 记录一次 data 载荷解析或 OAuth 解包失败，并把原载荷作为样本保留。
func (e *geminiUsageEvidence) observeParseFailure(raw []byte) {
	if e == nil {
		return
	}
	e.parseFailures++
	e.pushTail(e.makeSample(raw))
}

// observePayload 观察一个 JSON 载荷（OAuth 已解包、尚未做图片/伪装改写）。
// 非流式出口以整个响应体调用一次。
func (e *geminiUsageEvidence) observePayload(raw []byte) {
	if e == nil {
		return
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return
	}
	if !gjson.Valid(trimmed) {
		e.parseFailures++
		e.pushTail(e.makeSample(raw))
		return
	}
	parsed := gjson.Parse(trimmed)
	if parsed.IsObject() {
		e.jsonPayloads++
		e.extractStructure(parsed)
	} else {
		// 例如 JSON 数组：不能当对象取 usage，计为解析失败但保留样本。
		e.parseFailures++
	}
	e.pushTail(e.makeSample(raw))
}

func (e *geminiUsageEvidence) extractStructure(obj gjson.Result) {
	if usage := obj.Get("usageMetadata"); usage.Exists() {
		e.usageSeen = true
		e.lastUsage = geminiUsageEvidenceCounts{
			prompt:     usage.Get("promptTokenCount").Int(),
			candidates: usage.Get("candidatesTokenCount").Int(),
			thoughts:   usage.Get("thoughtsTokenCount").Int(),
			cached:     usage.Get("cachedContentTokenCount").Int(),
			total:      usage.Get("totalTokenCount").Int(),
		}
	}
	if v := obj.Get("candidates.0.finishReason").String(); v != "" {
		e.finishReason = v
	}
	if v := obj.Get("promptFeedback.blockReason").String(); v != "" {
		e.blockReason = v
	}
	if errObj := obj.Get("error"); errObj.Exists() {
		e.errorPresent = true
		if v := errObj.Get("code").String(); v != "" {
			e.errorCode = v
		}
		if v := errObj.Get("status").String(); v != "" {
			e.errorStatus = v
		}
	}
	if v := obj.Get("modelVersion").String(); v != "" {
		e.modelVersion = v
	}
	if v := obj.Get("responseId").String(); v != "" {
		e.responseID = v
	}
}

func (e *geminiUsageEvidence) pushTail(s geminiUsageEvidenceSample) {
	if len(e.tail) < geminiUsageEvidenceTailSize {
		e.tail = append(e.tail, s)
		return
	}
	e.tail[e.tailNext] = s
	e.tailNext = (e.tailNext + 1) % geminiUsageEvidenceTailSize
}

// makeSample 生成脱敏样本：能解析为 JSON 时 text / inlineData.data / thoughtSignature
// 只留长度；否则保留原文前缀。单条样本先截到总预算，保证内存有界。
func (e *geminiUsageEvidence) makeSample(raw []byte) geminiUsageEvidenceSample {
	text := redactGeminiEvidenceSample(raw)
	cut := truncateUTF8Bytes(text, geminiUsageEvidenceSampleBudget)
	return geminiUsageEvidenceSample{
		text:              cut,
		originalLen:       len(raw),
		capturedTruncated: len(cut) < len(text),
	}
}

func redactGeminiEvidenceSample(raw []byte) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || !json.Valid([]byte(trimmed)) {
		return string(raw)
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return string(raw)
	}
	v = redactGeminiEvidenceValue(v, false)
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // 保持 <text len=N> 等占位符可读
	if err := enc.Encode(v); err != nil {
		return string(raw)
	}
	return strings.TrimRight(buf.String(), "\n")
}

func redactGeminiEvidenceValue(v any, insideInlineData bool) any {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			switch {
			case k == "text":
				if s, ok := child.(string); ok {
					t[k] = fmt.Sprintf("<text len=%d>", len(s))
					continue
				}
			case k == "thoughtSignature" || k == "thought_signature":
				if s, ok := child.(string); ok {
					t[k] = fmt.Sprintf("<signature len=%d>", len(s))
					continue
				}
			case insideInlineData && k == "data":
				if s, ok := child.(string); ok {
					t[k] = fmt.Sprintf("<data len=%d>", len(s))
					continue
				}
			}
			t[k] = redactGeminiEvidenceValue(child, k == "inlineData" || k == "inline_data")
		}
		return t
	case []any:
		for i, child := range t {
			t[i] = redactGeminiEvidenceValue(child, false)
		}
		return t
	default:
		return v
	}
}

// truncateUTF8Bytes 截到不超过 maxBytes 字节，且不切断多字节字符。
func truncateUTF8Bytes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// reason 返回取证原因；空字符串表示用量正常、无需记录。
func (e *geminiUsageEvidence) reason() string {
	if e == nil {
		return ""
	}
	if !e.usageSeen {
		return geminiUsageEvidenceReasonMissing
	}
	if e.lastUsage.allZero() {
		return geminiUsageEvidenceReasonZero
	}
	return ""
}

// orderedTail 按时间序返回尾部样本。
func (e *geminiUsageEvidence) orderedTail() []geminiUsageEvidenceSample {
	if len(e.tail) < geminiUsageEvidenceTailSize {
		return append([]geminiUsageEvidenceSample(nil), e.tail...)
	}
	out := make([]geminiUsageEvidenceSample, 0, len(e.tail))
	for i := 0; i < len(e.tail); i++ {
		out = append(out, e.tail[(e.tailNext+i)%len(e.tail)])
	}
	return out
}

type geminiUsageEvidenceSampleOut struct {
	Sample      string `json:"sample"`
	OriginalLen int    `json:"original_len"`
	Truncated   bool   `json:"truncated,omitempty"`
}

// budgetedSamples 让首个非 data 行与尾部样本共用一个字节预算。
func (e *geminiUsageEvidence) budgetedSamples() (first *geminiUsageEvidenceSampleOut, tail []geminiUsageEvidenceSampleOut, truncated bool) {
	remaining := geminiUsageEvidenceSampleBudget
	take := func(s geminiUsageEvidenceSample) geminiUsageEvidenceSampleOut {
		text := truncateUTF8Bytes(s.text, remaining)
		remaining -= len(text)
		out := geminiUsageEvidenceSampleOut{Sample: text, OriginalLen: s.originalLen}
		if len(text) < len(s.text) || s.capturedTruncated {
			out.Truncated = true
			truncated = true
		}
		return out
	}
	if e.firstNonData != nil {
		o := take(*e.firstNonData)
		first = &o
	}
	for _, s := range e.orderedTail() {
		tail = append(tail, take(s))
	}
	return first, tail, truncated
}

// geminiUsageEvidenceMeta 是 ForwardNative 侧补充的关联字段。
type geminiUsageEvidenceMeta struct {
	account           *Account
	model             string
	upstreamModel     string
	action            string
	upstreamAction    string
	clientStream      bool
	upstreamStream    bool
	upstreamRequestID string
	resp              *http.Response
	duration          time.Duration
	billedUsage       *ClaudeUsage
}

func (e *geminiUsageEvidence) emit(c *gin.Context, meta geminiUsageEvidenceMeta) {
	reason := e.reason()
	if reason == "" {
		return
	}
	ctx := c.Request.Context()
	first, tail, truncated := e.budgetedSamples()

	fields := []zap.Field{
		zap.String("component", "service.gemini_usage_evidence"),
		zap.String("reason", reason),
		zap.String("exit", e.exit),
		zap.String("model", meta.model),
		zap.String("upstream_model", meta.upstreamModel),
		zap.String("action", meta.action),
		zap.String("upstream_action", meta.upstreamAction),
		zap.Bool("client_stream", meta.clientStream),
		zap.Bool("upstream_stream", meta.upstreamStream),
		zap.String("content_type", e.contentType),
		zap.Int64("bytes_read", e.bytesRead),
		zap.Int("data_lines", e.dataLines),
		zap.Int("json_payloads", e.jsonPayloads),
		zap.Int("parse_failures", e.parseFailures),
		zap.Int("blank_lines", e.blankLines),
		zap.Int("comment_lines", e.commentLines),
		zap.Int("done_markers", e.doneMarkers),
		zap.Int("non_data_lines", e.nonDataLines),
		zap.Bool("usage_seen", e.usageSeen),
		zap.Int64("duration_ms", meta.duration.Milliseconds()),
		zap.Bool("samples_truncated", truncated),
		zap.Any("tail_samples", tail),
	}
	if meta.account != nil {
		fields = append(fields,
			zap.Int64("account_id", meta.account.ID),
			zap.String("account_type", meta.account.Type),
			zap.String("platform", meta.account.Platform),
		)
	}
	if e.usageSeen {
		fields = append(fields,
			zap.Int64("usage_prompt", e.lastUsage.prompt),
			zap.Int64("usage_candidates", e.lastUsage.candidates),
			zap.Int64("usage_thoughts", e.lastUsage.thoughts),
			zap.Int64("usage_cached", e.lastUsage.cached),
			zap.Int64("usage_total", e.lastUsage.total),
		)
	}
	if first != nil {
		fields = append(fields, zap.Any("first_non_data_sample", first))
	}
	optional := map[string]string{
		"finish_reason":       e.finishReason,
		"block_reason":        e.blockReason,
		"error_code":          e.errorCode,
		"error_status":        e.errorStatus,
		"model_version":       e.modelVersion,
		"response_id":         e.responseID,
		"upstream_request_id": meta.upstreamRequestID,
	}
	for _, k := range []string{"finish_reason", "block_reason", "error_code", "error_status", "model_version", "response_id", "upstream_request_id"} {
		if v := optional[k]; v != "" {
			fields = append(fields, zap.String(k, v))
		}
	}
	if e.errorPresent {
		fields = append(fields, zap.Bool("error_present", true))
	}
	if meta.resp != nil {
		fields = append(fields, zap.Int("status_code", meta.resp.StatusCode))
		if meta.resp.Request != nil && meta.resp.Request.URL != nil && meta.resp.Request.URL.Host != "" {
			fields = append(fields, zap.String("upstream_host", meta.resp.Request.URL.Host))
		}
	}
	if meta.billedUsage != nil {
		u := meta.billedUsage
		fields = append(fields, zap.Bool("billed_usage_zero",
			u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadInputTokens == 0 &&
				u.CacheCreationInputTokens == 0 && u.ImageOutputTokens == 0))
	}

	logger.FromContext(ctx).Warn("gemini.usage_evidence", fields...)
}
