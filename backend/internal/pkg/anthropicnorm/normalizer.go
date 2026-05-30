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
