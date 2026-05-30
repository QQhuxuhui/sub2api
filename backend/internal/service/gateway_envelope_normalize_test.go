//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicnorm"
	"github.com/tidwall/gjson"
)

type acctVertex struct{}

func (acctVertex) NormPlatform() string { return "anthropic" }
func (acctVertex) NormType() string     { return "service_account" }

// 启用时：Vertex message_start 经规范化后 id 为 msg_、含 service_tier、注入 ping。
func TestEnvelopeNormalizeStreamingShape(t *testing.T) {
	n := anthropicnorm.For(acctVertex{}, true)
	if n == nil {
		t.Fatal("Vertex+启用应得到 normalizer")
	}
	in := []byte(`{"type":"message_start","message":{"content":[],"id":"req_vrtx_x","model":"claude-opus-4-6","role":"assistant","stop_reason":null,"stop_sequence":null,"type":"message","usage":{"input_tokens":10,"output_tokens":1}}}`)
	out, ping := n.RewriteStreamEvent("message_start", in)
	if !ping {
		t.Fatal("应注入 ping")
	}
	g := gjson.ParseBytes(out)
	if !strings.HasPrefix(g.Get("message.id").String(), "msg_") {
		t.Fatal("id 未规范化")
	}
	if g.Get("message.usage.service_tier").String() != "standard" {
		t.Fatal("service_tier 缺失")
	}
}

// 未启用：For 返回 nil（零接触）。
func TestEnvelopeNormalizeDisabledNil(t *testing.T) {
	if anthropicnorm.For(acctVertex{}, false) != nil {
		t.Fatal("未启用应为 nil")
	}
}
