package handler

import (
	"go.uber.org/zap"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// openAICallIDShortener applies service.ShortenOpenAIOverlongCallIDs to each
// forward attempt of one request, remembering the last result so a failover
// that forwards the same body again does not redo the work.
type openAICallIDShortener struct {
	src    []byte
	out    []byte
	cached bool
	logged bool
}

// apply returns the body to forward to account. Codex-protocol accounts that
// are not in passthrough mode get body unchanged: their Codex transform already
// normalizes call_id prefixes (call_x and fc_x pair up) before compacting, and
// shortening first would break that pairing.
func (s *openAICallIDShortener) apply(body []byte, account *service.Account, reqLog *zap.Logger) []byte {
	if account == nil || (account.UsesOpenAICodexProtocol() && !account.IsOpenAIPassthroughEnabled()) {
		return body
	}
	if s.cached && sameByteSlice(s.src, body) {
		return s.out
	}
	out, changed := service.ShortenOpenAIOverlongCallIDs(body)
	s.src, s.out, s.cached = body, out, true
	if changed > 0 && !s.logged && reqLog != nil {
		s.logged = true
		reqLog.Info("openai.overlong_call_id_shortened",
			zap.Int("items", changed),
			zap.Int64("account_id", account.ID),
		)
	}
	return out
}

func sameByteSlice(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	return len(a) == 0 || &a[0] == &b[0]
}
