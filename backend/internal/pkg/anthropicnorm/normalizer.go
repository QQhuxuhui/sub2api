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

func (n *Normalizer) rewriteMessageDelta(data []byte) ([]byte, error) {
	return data, nil // TEMP: 由下个任务实现
}
