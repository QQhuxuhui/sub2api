package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const anthropicFailoverTrackedAccountsMax = 12

// anthropicFailoverTracker collects what one /v1/messages request did with
// rule switches and pre-content stream switches, for the statistics page. The
// first cause is kept: later failures never overwrite it.
type anthropicFailoverTracker struct {
	service *service.AnthropicFailoverPolicyService
	outcome service.AnthropicFailoverOutcome
}

func newAnthropicFailoverTracker(svc *service.AnthropicFailoverPolicyService, userID, apiKeyID int64) *anthropicFailoverTracker {
	return &anthropicFailoverTracker{
		service: svc,
		outcome: service.AnthropicFailoverOutcome{UserID: userID, APIKeyID: apiKeyID},
	}
}

func (t *anthropicFailoverTracker) note(failoverErr *service.UpstreamFailoverError, accountID int64, groupID *int64) {
	if t == nil || failoverErr == nil {
		return
	}
	if t.outcome.Reason == "" {
		reason := failoverErr.FailoverRuleID
		if reason == "" && failoverErr.SafeToFailoverAfterWrite {
			reason = service.AnthropicFailoverReasonStream
		}
		if reason == "" {
			return
		}
		t.outcome.Reason = reason
		t.outcome.StatusCode = failoverErr.StatusCode
		if groupID != nil {
			t.outcome.GroupID = *groupID
		}
	}
	if len(t.outcome.Accounts) < anthropicFailoverTrackedAccountsMax {
		t.outcome.Accounts = append(t.outcome.Accounts, accountID)
	}
}

func (t *anthropicFailoverTracker) switched() {
	if t != nil && t.outcome.Reason != "" {
		t.outcome.Switches++
	}
}

func (t *anthropicFailoverTracker) succeeded(result *service.ForwardResult) {
	if t != nil && t.outcome.Reason != "" && result != nil && !result.ClientDisconnect {
		t.outcome.Recovered = true
	}
}

func (t *anthropicFailoverTracker) finish(c *gin.Context) {
	if t == nil || t.outcome.Reason == "" {
		return
	}
	if c != nil && c.Request != nil && c.Request.Context().Err() != nil {
		t.outcome.Canceled = true
		t.outcome.Recovered = false
	}
	t.service.RecordOutcome(t.outcome)
}
