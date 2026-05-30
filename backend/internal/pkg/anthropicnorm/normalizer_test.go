//go:build unit

package anthropicnorm

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"

	"github.com/tidwall/gjson"
)

var msgIDRe = regexp.MustCompile(`^msg_[0-9A-Za-z]{24}$`)

func TestNewMessageIDFormat(t *testing.T) {
	n := newNormalizer()
	id := n.messageID()
	if !msgIDRe.MatchString(id) {
		t.Fatalf("id %q 不符合 ^msg_[0-9A-Za-z]{24}$", id)
	}
	if id2 := n.messageID(); id2 != id {
		t.Fatalf("同一 normalizer 应复用同一 id，得到 %q vs %q", id, id2)
	}
}

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func TestRewriteMessageStart(t *testing.T) {
	in := loadFixture(t, "b_message_start.json")
	n := newNormalizer()
	out, injectPing := n.RewriteStreamEvent("message_start", in)
	if !injectPing {
		t.Fatal("message_start 后应注入 ping")
	}
	g := gjson.ParseBytes(out)
	var keys []string
	g.Get("message").ForEach(func(k, _ gjson.Result) bool { keys = append(keys, k.String()); return true })
	want := []string{"model", "id", "type", "role", "content", "stop_reason", "stop_sequence", "stop_details", "usage"}
	if len(keys) != len(want) {
		t.Fatalf("message 字段数 %d != %d: %v", len(keys), len(want), keys)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("字段序[%d]=%q want %q (%v)", i, keys[i], want[i], keys)
		}
	}
	if id := g.Get("message.id").String(); !msgIDRe.MatchString(id) {
		t.Fatalf("id 未改写为 msg_: %q", id)
	}
	u := g.Get("message.usage")
	if u.Get("service_tier").String() != "standard" {
		t.Fatalf("service_tier=%q", u.Get("service_tier").String())
	}
	if u.Get("inference_geo").String() != "not_available" {
		t.Fatalf("inference_geo=%q", u.Get("inference_geo").String())
	}
	for _, k := range []string{"cache_creation_input_tokens", "cache_read_input_tokens", "cache_creation"} {
		if !u.Get(k).Exists() {
			t.Fatalf("usage 缺 %s", k)
		}
	}
	if u.Get("input_tokens").Int() != gjson.ParseBytes(in).Get("message.usage.input_tokens").Int() {
		t.Fatal("input_tokens 未保留上游值")
	}
	_ = json.Valid
}
