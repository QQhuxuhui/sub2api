//go:build unit

package anthropicnorm

import (
	"regexp"
	"testing"
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
