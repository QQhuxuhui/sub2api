package anthropicnorm

import (
	"crypto/rand"
	"encoding/json"

	"github.com/tidwall/gjson"
)

const (
	serviceTierStandard = "standard"
	inferenceGeoDefault = "not_available"
	idAlphabet          = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	idBodyLen           = 22 // "msg_01" + 22 = 24 字符尾部
)

// Normalizer 按响应实例化，持有一次性生成的 message id。
type Normalizer struct {
	id string
}

func newNormalizer() *Normalizer { return &Normalizer{} }

// messageID 懒生成并复用：msg_01 + 22 位 base62。
func (n *Normalizer) messageID() string {
	if n.id == "" {
		buf := make([]byte, idBodyLen)
		if _, err := rand.Read(buf); err == nil {
			for i := range buf {
				buf[i] = idAlphabet[int(buf[i])%len(idAlphabet)]
			}
		}
		n.id = "msg_01" + string(buf)
	}
	return n.id
}

// rawOrNull 返回上游原始 JSON 片段；不存在则返回字面量 null。
func rawOrNull(r gjson.Result) json.RawMessage {
	if r.Exists() {
		return json.RawMessage(r.Raw)
	}
	return nullRaw
}

// RewriteStreamEvent 重写单个 SSE 事件 data。仅接管 message_start / message_delta，
// 其余类型原样返回。injectPing 为 true 时调用方应在该事件块后注入一个 ping。
// 任何解析/序列化失败均回退为原样透传。
func (n *Normalizer) RewriteStreamEvent(eventType string, data []byte) (out []byte, injectPing bool) {
	if !json.Valid(data) {
		return data, false
	}
	switch eventType {
	case "message_start":
		b, err := n.rewriteMessageStart(data)
		if err != nil {
			return data, false
		}
		return b, true
	case "message_delta":
		b, err := n.rewriteMessageDelta(data)
		if err != nil {
			return data, false
		}
		return b, false
	default:
		return data, false
	}
}

func (n *Normalizer) rewriteMessageStart(data []byte) ([]byte, error) {
	g := gjson.ParseBytes(data)
	m := g.Get("message")
	u := m.Get("usage")
	ev := messageStartEvent{
		Type: "message_start",
		Message: messageStartBody{
			Model:        m.Get("model").String(),
			ID:           n.messageID(),
			Type:         "message",
			Role:         "assistant",
			Content:      rawOrNull(m.Get("content")),
			StopReason:   rawOrNull(m.Get("stop_reason")),
			StopSequence: rawOrNull(m.Get("stop_sequence")),
			StopDetails:  rawOrNull(m.Get("stop_details")),
			Usage: startUsage{
				InputTokens:              u.Get("input_tokens").Int(),
				CacheCreationInputTokens: u.Get("cache_creation_input_tokens").Int(),
				CacheReadInputTokens:     u.Get("cache_read_input_tokens").Int(),
				CacheCreation: cacheCreation{
					Ephemeral5m: u.Get("cache_creation.ephemeral_5m_input_tokens").Int(),
					Ephemeral1h: u.Get("cache_creation.ephemeral_1h_input_tokens").Int(),
				},
				OutputTokens: u.Get("output_tokens").Int(),
				ServiceTier:  serviceTierStandard,
				InferenceGeo: inferenceGeoDefault,
			},
		},
	}
	return json.Marshal(ev)
}

// RewriteNonStreamingBody 把非流式 Anthropic 响应体重建为原生形状。失败回退原样。
func (n *Normalizer) RewriteNonStreamingBody(body []byte) []byte {
	g := gjson.ParseBytes(body)
	if g.Get("type").String() != "message" {
		return body
	}
	u := g.Get("usage")
	ccIn := u.Get("cache_creation_input_tokens").Int()
	crIn := u.Get("cache_read_input_tokens").Int()
	inTok := u.Get("input_tokens").Int()
	outTok := u.Get("output_tokens").Int()
	cc := cacheCreation{
		Ephemeral5m: u.Get("cache_creation.ephemeral_5m_input_tokens").Int(),
		Ephemeral1h: u.Get("cache_creation.ephemeral_1h_input_tokens").Int(),
	}
	msg := nonStreamMessage{
		Model:        g.Get("model").String(),
		ID:           n.messageID(),
		Type:         "message",
		Role:         "assistant",
		Content:      rawOrNull(g.Get("content")),
		StopReason:   rawOrNull(g.Get("stop_reason")),
		StopSequence: rawOrNull(g.Get("stop_sequence")),
		StopDetails:  rawOrNull(g.Get("stop_details")),
		Usage: fullUsage{
			InputTokens:              inTok,
			CacheCreationInputTokens: ccIn,
			CacheReadInputTokens:     crIn,
			CacheCreation:            cc,
			Iterations: []iteration{{
				InputTokens:              inTok,
				OutputTokens:             outTok,
				CacheReadInputTokens:     crIn,
				CacheCreationInputTokens: ccIn,
				CacheCreation:            cc,
				Type:                     "message",
			}},
			OutputTokens:        outTok,
			OutputTokensDetails: outputTokensDetails{ThinkingTokens: u.Get("output_tokens_details.thinking_tokens").Int()},
			ServiceTier:         serviceTierStandard,
			InferenceGeo:        inferenceGeoDefault,
		},
		ContextManagement: emptyContextManagement,
	}
	out, err := json.Marshal(msg)
	if err != nil {
		return body
	}
	return out
}

func (n *Normalizer) rewriteMessageDelta(data []byte) ([]byte, error) {
	g := gjson.ParseBytes(data)
	d := g.Get("delta")
	u := g.Get("usage")
	in := u.Get("input_tokens").Int()
	outTok := u.Get("output_tokens").Int()
	ccIn := u.Get("cache_creation_input_tokens").Int()
	crIn := u.Get("cache_read_input_tokens").Int()
	cc := cacheCreation{
		Ephemeral5m: u.Get("cache_creation.ephemeral_5m_input_tokens").Int(),
		Ephemeral1h: u.Get("cache_creation.ephemeral_1h_input_tokens").Int(),
	}
	ev := messageDeltaEvent{
		Type: "message_delta",
		Delta: deltaBody{
			StopReason:   rawOrNull(d.Get("stop_reason")),
			StopSequence: rawOrNull(d.Get("stop_sequence")),
			StopDetails:  rawOrNull(d.Get("stop_details")),
		},
		Usage: deltaUsage{
			InputTokens:              in,
			CacheCreationInputTokens: ccIn,
			CacheReadInputTokens:     crIn,
			OutputTokens:             outTok,
			OutputTokensDetails:      outputTokensDetails{ThinkingTokens: u.Get("output_tokens_details.thinking_tokens").Int()},
			Iterations: []iteration{{
				InputTokens:              in,
				OutputTokens:             outTok,
				CacheReadInputTokens:     crIn,
				CacheCreationInputTokens: ccIn,
				CacheCreation:            cc,
				Type:                     "message",
			}},
		},
		ContextManagement: emptyContextManagement,
	}
	return json.Marshal(ev)
}
