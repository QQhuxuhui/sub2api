package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const openAIFailoverTrackedAccountsMax = 12

// openAIFailoverRuleTrack carries one OpenAI HTTP request's rule-table state:
// the group's policy, the switch budget it allows and what the statistics
// page records. The first rule hit is kept; later failures never overwrite it.
type openAIFailoverRuleTrack struct {
	service  *service.OpenAIFailoverPolicyService
	policy   *service.OpenAIFailoverPolicy
	switches int
	outcome  service.OpenAIFailoverOutcome
}

// beginOpenAIFailoverRules attaches the group's policy to the request. A group
// that did not opt in gets a nil policy, which leaves every path as before.
func (h *OpenAIGatewayHandler) beginOpenAIFailoverRules(c *gin.Context, apiKey *service.APIKey, userID int64) *openAIFailoverRuleTrack {
	track := &openAIFailoverRuleTrack{service: h.openAIFailoverPolicyService}
	if apiKey == nil {
		return track
	}
	track.outcome.UserID = userID
	track.outcome.APIKeyID = apiKey.ID
	if apiKey.GroupID != nil {
		track.outcome.GroupID = *apiKey.GroupID
	}
	if h.openAIFailoverPolicyService != nil {
		track.policy = h.openAIFailoverPolicyService.PolicyForGroup(c.Request.Context(), apiKey.GroupID)
	}
	c.Request = c.Request.WithContext(service.WithOpenAIFailoverPolicy(c.Request.Context(), track.policy))
	return track
}

// hit records a rule switch request and reports whether the rule budget is
// already spent, in which case the error goes to the customer.
func (t *openAIFailoverRuleTrack) hit(failoverErr *service.UpstreamFailoverError, accountID int64) (exhausted bool) {
	if t == nil || failoverErr == nil || failoverErr.FailoverRuleID == "" {
		return false
	}
	if t.outcome.RuleID == "" {
		t.outcome.RuleID = failoverErr.FailoverRuleID
		t.outcome.StatusCode = failoverErr.StatusCode
	}
	if len(t.outcome.Accounts) < openAIFailoverTrackedAccountsMax {
		t.outcome.Accounts = append(t.outcome.Accounts, accountID)
	}
	return t.switches >= t.policy.MaxRuleSwitches()
}

// switched counts an account switch a rule caused.
func (t *openAIFailoverRuleTrack) switched(failoverErr *service.UpstreamFailoverError) {
	if t != nil && failoverErr != nil && failoverErr.FailoverRuleID != "" {
		t.switches++
		t.outcome.Switches = t.switches
	}
}

// finish records the request when a rule was hit. Recovered means a later
// account answered successfully.
func (t *openAIFailoverRuleTrack) finish(c *gin.Context) {
	if t == nil || t.outcome.RuleID == "" || t.service == nil {
		return
	}
	if c != nil && c.Request != nil && c.Request.Context().Err() != nil {
		t.outcome.Canceled = true
	} else if c != nil && t.switches > 0 && c.Writer.Written() && c.Writer.Status() < http.StatusBadRequest {
		t.outcome.Recovered = true
	}
	t.service.RecordOutcome(t.outcome)
}

// writeOpenAIRuleFailoverExhausted shows the customer the error a rule switch
// ended on the way the path that received it would have without the rule
// table. Account handling is skipped while replaying.
func (h *OpenAIGatewayHandler) writeOpenAIRuleFailoverExhausted(c *gin.Context, failoverErr *service.UpstreamFailoverError, streamStarted bool, anthropicFormat bool) {
	if !streamStarted && !c.Writer.Written() && failoverErr.RuleReplay != nil {
		failoverErr.RuleReplay(c)
		return
	}
	message := service.SanitizeUpstreamErrorMessage(service.ExtractUpstreamErrorMessage(failoverErr.ResponseBody))
	if message == "" {
		message = http.StatusText(failoverErr.StatusCode)
	}
	service.SetOpsUpstreamError(c, failoverErr.StatusCode, message, "")
	if anthropicFormat {
		h.anthropicStreamingAwareError(c, failoverErr.StatusCode, "invalid_request_error", message, streamStarted)
		return
	}
	h.handleStreamingAwareError(c, failoverErr.StatusCode, "invalid_request_error", message, streamStarted)
}
