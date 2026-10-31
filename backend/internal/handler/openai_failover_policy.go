package handler

import (
	"net/http"
	"slices"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// openAIFailoverTrack accumulates what one /v1/responses request did with the
// group's failover policy. Only the request's goroutine touches it.
type openAIFailoverTrack struct {
	outcome      service.OpenAIFailoverOutcome
	slotSwitches int
	recovered    bool
}

func (t *openAIFailoverTrack) addAccount(accountID int64) {
	if !slices.Contains(t.outcome.Accounts, accountID) {
		t.outcome.Accounts = append(t.outcome.Accounts, accountID)
	}
}

// recordRuleHit keeps the first rule of the request: that is what the page
// measures a rule by.
func (t *openAIFailoverTrack) recordRuleHit(failoverErr *service.UpstreamFailoverError, accountID int64) {
	if t.outcome.RuleID == "" {
		t.outcome.RuleID = failoverErr.FailoverRuleID
		t.outcome.StatusCode = failoverErr.StatusCode
	}
	t.outcome.RuleSwitches++
	t.addAccount(accountID)
}

func (t *openAIFailoverTrack) recordSlotSwitch(accountID int64) int {
	t.slotSwitches++
	t.addAccount(accountID)
	return t.slotSwitches
}

// prepareOpenAIFailoverPolicy attaches the group's policy to the request and
// repairs the body. The encrypted-arguments filter is not part of the group
// switch: an account marked as unsupported must never see such a request.
func (h *OpenAIGatewayHandler) prepareOpenAIFailoverPolicy(
	c *gin.Context,
	apiKey *service.APIKey,
	userID int64,
	forwardBody *[]byte,
	reqLog *zap.Logger,
) (*service.OpenAIFailoverPolicy, *openAIFailoverTrack) {
	track := &openAIFailoverTrack{}
	track.outcome.UserID = userID
	track.outcome.APIKeyID = apiKey.ID
	if apiKey.GroupID != nil {
		track.outcome.GroupID = *apiKey.GroupID
	}
	ctx := c.Request.Context()
	if service.OpenAIRequestDeclaresEncryptedArgs(*forwardBody) {
		ctx = service.WithOpenAIEncryptedArgsRequired(ctx)
	}
	policy := h.failoverPolicy.PolicyForGroup(ctx, apiKey.GroupID)
	if policy != nil {
		ctx = service.WithOpenAIFailoverPolicy(ctx, policy)
		rewritten, count := service.RewriteOpenAIPlaintextEncryptedContent(*forwardBody)
		if count > 0 {
			*forwardBody = rewritten
			track.outcome.Rewrites = count
			reqLog.Info("openai.plaintext_encrypted_content_rewritten", zap.Int("items", count))
		}
	}
	c.Request = c.Request.WithContext(ctx)
	return policy, track
}

func (h *OpenAIGatewayHandler) finishOpenAIFailoverTrack(track *openAIFailoverTrack) {
	track.outcome.SlotSwitches = track.slotSwitches
	if track.outcome.RuleID == "" && track.slotSwitches == 0 && track.outcome.Rewrites == 0 {
		return
	}
	track.outcome.Recovered = track.recovered
	h.failoverPolicy.RecordOutcome(track.outcome)
}

func openAISlotBusyErrorFrom(c *gin.Context) *openAISlotBusyError {
	if value, ok := c.Get(openAISlotBusyErrorKey); ok {
		if busy, ok := value.(openAISlotBusyError); ok {
			return &busy
		}
	}
	return &openAISlotBusyError{
		status:  http.StatusTooManyRequests,
		errType: "rate_limit_error",
		code:    gatewayConcurrencyLimitCode,
		message: "Concurrency limit exceeded for account, please retry later",
	}
}

// writeOpenAIRuleFailoverExhausted returns the last upstream's real error: the
// rule moved the request, it did not change what the error means.
func (h *OpenAIGatewayHandler) writeOpenAIRuleFailoverExhausted(c *gin.Context, failoverErr *service.UpstreamFailoverError, streamStarted bool) {
	status := failoverErr.StatusCode
	message := service.SanitizeUpstreamErrorMessage(service.ExtractUpstreamErrorMessage(failoverErr.ResponseBody))
	service.SetOpsUpstreamError(c, status, message, "")
	if !streamStarted {
		service.WriteOpenAIUpstreamClientError(c, status, failoverErr.ResponseBody, message)
		return
	}
	h.handleStreamingAwareError(c, status, "invalid_request_error", message, streamStarted)
}
