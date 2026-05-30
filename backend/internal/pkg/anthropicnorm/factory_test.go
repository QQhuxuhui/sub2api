//go:build unit

package anthropicnorm

import "testing"

type fakeAcct struct{ platform, typ string }

func (f fakeAcct) NormPlatform() string { return f.platform }
func (f fakeAcct) NormType() string     { return f.typ }

func TestForRouting(t *testing.T) {
	if For(fakeAcct{"anthropic", "service_account"}, false) != nil {
		t.Fatal("未开启应返回 nil")
	}
	if For(fakeAcct{"anthropic", "oauth"}, true) != nil {
		t.Fatal("非 Vertex 应返回 nil")
	}
	if For(fakeAcct{"anthropic", "service_account"}, true) == nil {
		t.Fatal("Vertex+开启应返回 normalizer")
	}
}
