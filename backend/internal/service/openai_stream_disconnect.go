package service

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 上游流内断流：CLIProxyAPI 等 codex 号池在上游流没走到 response.completed 就断开时，
// 会在 HTTP 200 的 SSE 里发一条 code=request_timeout、type=invalid_request_error 的
// error 事件。它是上游故障，不是客户端请求有误，但原样透传会让客户与运维都当成 400。
//
// 判据刻意收窄：外层 error.code 必须是 request_timeout，且 message 含断流短语。
// 真 400（上下文超长、内容审核、参数错误等）的 code 都不是 request_timeout，天然排除。

const (
	openAIStreamDisconnectCode      = "request_timeout"
	openAIStreamDisconnectErrorType = "server_error"
)

var openAIStreamDisconnectPhrases = []string{
	"stream disconnected before completion",
	"stream closed before response.completed",
}

// IsOpenAIUpstreamStreamDisconnect reports whether an error object's outer code
// and message describe an upstream stream that closed before completing.
func IsOpenAIUpstreamStreamDisconnect(code, message string) bool {
	if strings.TrimSpace(code) != openAIStreamDisconnectCode {
		return false
	}
	lower := strings.ToLower(message)
	for _, phrase := range openAIStreamDisconnectPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// rewriteOpenAIStreamDisconnectErrorType changes error.type to server_error on
// an upstream disconnect error event relayed from an OpenAI API-key account.
// Every other field of the event is left as the upstream sent it.
func rewriteOpenAIStreamDisconnectErrorType(data []byte, eventType string, account *Account) ([]byte, bool) {
	if account == nil || !account.IsOpenAIApiKey() || eventType != "error" {
		return data, false
	}
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return data, false
	}
	errObj := gjson.GetBytes(data, "error")
	if !errObj.IsObject() {
		return data, false
	}
	if !IsOpenAIUpstreamStreamDisconnect(errObj.Get("code").String(), errObj.Get("message").String()) {
		return data, false
	}
	errType := errObj.Get("type")
	if !errType.Exists() || errType.String() == openAIStreamDisconnectErrorType {
		return data, false
	}
	rewritten, err := sjson.SetBytes(data, "error.type", openAIStreamDisconnectErrorType)
	if err != nil {
		return data, false
	}
	return rewritten, true
}

// markOpenAIRuleStreamDisconnect turns a rule-table failover error caused by an
// upstream disconnect error event into a 502 server_error, so that exhausting
// the rule's switches no longer reports the upstream fault as a client 400.
// Anything else is returned unchanged.
func markOpenAIRuleStreamDisconnect(fe *UpstreamFailoverError, account *Account, eventType string, payload []byte) *UpstreamFailoverError {
	if fe == nil || fe.FailoverRuleID == "" || account == nil || !account.IsOpenAIApiKey() || eventType != "error" {
		return fe
	}
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return fe
	}
	if !IsOpenAIUpstreamStreamDisconnect(gjson.GetBytes(payload, "error.code").String(), gjson.GetBytes(payload, "error.message").String()) {
		return fe
	}
	body := fe.ResponseBody
	if updated, err := sjson.SetBytes(body, "error.type", openAIStreamDisconnectErrorType); err == nil {
		body = updated
	} else {
		return fe
	}
	if updated, err := sjson.SetBytes(body, "error.code", openAIStreamDisconnectCode); err == nil {
		body = updated
	} else {
		return fe
	}
	fe.ResponseBody = body
	fe.StatusCode = http.StatusBadGateway
	fe.OpenAIStreamDisconnect = true
	return fe
}

// OpenAIStreamDisconnectClientErrorType and OpenAIStreamDisconnectClientCode
// are what the handler reports once a marked rule failover is exhausted.
const (
	OpenAIStreamDisconnectClientErrorType = openAIStreamDisconnectErrorType
	OpenAIStreamDisconnectClientCode      = openAIStreamDisconnectCode
)
