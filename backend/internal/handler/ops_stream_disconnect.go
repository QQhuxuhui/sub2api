package handler

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// normalizeOpsUpstreamStreamDisconnect files an upstream stream disconnect
// (code request_timeout + disconnect message) as a 502 upstream error. Without
// it the event's type invalid_request_error makes ops record a client 400.
// It covers in-stream failures and the 502 JSON written when rule switches run
// out; an explicit 5xx status on the event is kept.
func normalizeOpsUpstreamStreamDisconnect(parsed parsedOpsError, wireStatus int) parsedOpsError {
	if !service.IsOpenAIUpstreamStreamDisconnect(parsed.Code, parsed.Message) {
		return parsed
	}
	if !parsed.StreamFailure && wireStatus < http.StatusInternalServerError {
		return parsed
	}
	parsed.ErrorType = "upstream_error"
	if parsed.StreamFailure && (parsed.StatusCode < http.StatusInternalServerError || parsed.StatusCode > 599) {
		parsed.StatusCode = http.StatusBadGateway
	}
	return parsed
}
