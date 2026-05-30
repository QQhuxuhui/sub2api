package anthropicnorm

import "encoding/json"

// nullRaw 是字面量 null，用于结构字段缺省。
var nullRaw = json.RawMessage("null")

// emptyContextManagement 是非流式/delta 的 context_management 默认值。
var emptyContextManagement = json.RawMessage(`{"applied_edits":[]}`)

type cacheCreation struct {
	Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
	Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
}

// ---- message_start ----

type messageStartEvent struct {
	Type    string           `json:"type"`
	Message messageStartBody `json:"message"`
}

type messageStartBody struct {
	Model        string          `json:"model"`
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	Role         string          `json:"role"`
	Content      json.RawMessage `json:"content"`
	StopReason   json.RawMessage `json:"stop_reason"`
	StopSequence json.RawMessage `json:"stop_sequence"`
	StopDetails  json.RawMessage `json:"stop_details"`
	Usage        startUsage      `json:"usage"`
}

type startUsage struct {
	InputTokens              int64         `json:"input_tokens"`
	CacheCreationInputTokens int64         `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64         `json:"cache_read_input_tokens"`
	CacheCreation            cacheCreation `json:"cache_creation"`
	OutputTokens             int64         `json:"output_tokens"`
	ServiceTier              string        `json:"service_tier"`
	InferenceGeo             string        `json:"inference_geo"`
}

// ---- message_delta ----

type messageDeltaEvent struct {
	Type              string          `json:"type"`
	Delta             deltaBody       `json:"delta"`
	Usage             deltaUsage      `json:"usage"`
	ContextManagement json.RawMessage `json:"context_management"`
}

type deltaBody struct {
	StopReason   json.RawMessage `json:"stop_reason"`
	StopSequence json.RawMessage `json:"stop_sequence"`
	StopDetails  json.RawMessage `json:"stop_details"`
}

type deltaUsage struct {
	InputTokens              int64               `json:"input_tokens"`
	CacheCreationInputTokens int64               `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64               `json:"cache_read_input_tokens"`
	OutputTokens             int64               `json:"output_tokens"`
	OutputTokensDetails      outputTokensDetails `json:"output_tokens_details"`
	Iterations               []iteration         `json:"iterations"`
}

type outputTokensDetails struct {
	ThinkingTokens int64 `json:"thinking_tokens"`
}

type iteration struct {
	InputTokens              int64         `json:"input_tokens"`
	OutputTokens             int64         `json:"output_tokens"`
	CacheReadInputTokens     int64         `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64         `json:"cache_creation_input_tokens"`
	CacheCreation            cacheCreation `json:"cache_creation"`
	Type                     string        `json:"type"`
}

// ---- 非流式（全集） ----

type nonStreamMessage struct {
	Model             string          `json:"model"`
	ID                string          `json:"id"`
	Type              string          `json:"type"`
	Role              string          `json:"role"`
	Content           json.RawMessage `json:"content"`
	StopReason        json.RawMessage `json:"stop_reason"`
	StopSequence      json.RawMessage `json:"stop_sequence"`
	StopDetails       json.RawMessage `json:"stop_details"`
	Usage             fullUsage       `json:"usage"`
	ContextManagement json.RawMessage `json:"context_management"`
}

type fullUsage struct {
	InputTokens              int64               `json:"input_tokens"`
	CacheCreationInputTokens int64               `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64               `json:"cache_read_input_tokens"`
	CacheCreation            cacheCreation       `json:"cache_creation"`
	Iterations               []iteration         `json:"iterations"`
	OutputTokens             int64               `json:"output_tokens"`
	OutputTokensDetails      outputTokensDetails `json:"output_tokens_details"`
	ServiceTier              string              `json:"service_tier"`
	InferenceGeo             string              `json:"inference_geo"`
}
