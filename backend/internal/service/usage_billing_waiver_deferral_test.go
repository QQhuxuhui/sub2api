//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// usageBillingWaiverRepo answers like the real repository, which reports a
// deferred deduction only for a positive amount.
type usageBillingWaiverRepo struct {
	UsageBillingRepository
	cmd *UsageBillingCommand
}

func (r *usageBillingWaiverRepo) Apply(_ context.Context, cmd *UsageBillingCommand) (*UsageBillingApplyResult, error) {
	r.cmd = cmd
	return &UsageBillingApplyResult{Applied: true, BalanceDeferred: cmd.DeferBalance && cmd.BalanceCost > 0}, nil
}

func TestOpenAIRecordUsage_EmptyImageWaiverWritesNoPending(t *testing.T) {
	observed := 0
	input := &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID:            "waiver-openai",
			Model:                "gpt-image-1",
			ImageCount:           1,
			ImageOutputsObserved: &observed,
			ImageSize:            ImageBillingSize1K,
		},
		APIKey:  &APIKey{ID: 10},
		User:    &User{ID: 20},
		Account: &Account{ID: 30},
	}

	t.Run("a matching rule leaves nothing to defer", func(t *testing.T) {
		repo := &usageBillingWaiverRepo{}
		deferral := &deferralStub{deferred: true}
		svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(&openAIRecordUsageLogRepoStub{inserted: true}, repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
		svc.emptyResponseBillingResolver = newUserEmptyResponseBillingResolver(&stubEmptyResponseBillingRepo{rules: []EmptyResponseBillingRule{{ID: 77, Enabled: true}}}, "test")
		svc.SetUserBalanceDeferral(deferral)

		require.NoError(t, svc.RecordUsage(context.Background(), input))

		require.NotNil(t, repo.cmd)
		require.Zero(t, repo.cmd.BalanceCost)
		require.False(t, repo.cmd.DeferBalance)
		require.Empty(t, deferral.dirty)
	})

	// The same request without a rule proves this deferral setup does defer.
	t.Run("without a rule the charge is deferred", func(t *testing.T) {
		repo := &usageBillingWaiverRepo{}
		deferral := &deferralStub{deferred: true}
		svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(&openAIRecordUsageLogRepoStub{inserted: true}, repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
		svc.SetUserBalanceDeferral(deferral)

		require.NoError(t, svc.RecordUsage(context.Background(), input))

		require.NotNil(t, repo.cmd)
		require.Positive(t, repo.cmd.BalanceCost)
		require.True(t, repo.cmd.DeferBalance)
		require.Equal(t, []int64{20}, deferral.dirty)
	})
}

func TestGatewayRecordUsage_EmptyImageWaiverWritesNoPending(t *testing.T) {
	imagePrice := 0.19
	groupID := int64(901)
	observed := 0
	input := func() *RecordUsageInput {
		return &RecordUsageInput{
			Result: &ForwardResult{
				RequestID:            "waiver-gateway",
				Model:                "gemini-image",
				ImageCount:           1,
				ImageOutputsObserved: &observed,
				ImageSize:            ImageBillingSize2K,
				Duration:             time.Second,
			},
			APIKey: &APIKey{
				ID:      801,
				GroupID: i64p(groupID),
				Group:   &Group{ID: groupID, RateMultiplier: 1.0, ImagePrice2K: &imagePrice},
			},
			User:    &User{ID: 601},
			Account: &Account{ID: 701},
		}
	}

	t.Run("a matching rule leaves nothing to defer", func(t *testing.T) {
		repo := &usageBillingWaiverRepo{}
		deferral := &deferralStub{deferred: true}
		svc := newGatewayRecordUsageServiceWithBillingRepoForTest(&openAIRecordUsageLogRepoStub{inserted: true}, repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
		svc.emptyResponseBillingResolver = newUserEmptyResponseBillingResolver(&stubEmptyResponseBillingRepo{rules: []EmptyResponseBillingRule{{ID: 78, Enabled: true}}}, "test")
		svc.SetUserBalanceDeferral(deferral)

		require.NoError(t, svc.RecordUsage(context.Background(), input()))

		require.NotNil(t, repo.cmd)
		require.Zero(t, repo.cmd.BalanceCost)
		require.False(t, repo.cmd.DeferBalance)
		require.Empty(t, deferral.dirty)
	})

	t.Run("without a rule the charge is deferred", func(t *testing.T) {
		repo := &usageBillingWaiverRepo{}
		deferral := &deferralStub{deferred: true}
		svc := newGatewayRecordUsageServiceWithBillingRepoForTest(&openAIRecordUsageLogRepoStub{inserted: true}, repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
		svc.SetUserBalanceDeferral(deferral)

		require.NoError(t, svc.RecordUsage(context.Background(), input()))

		require.NotNil(t, repo.cmd)
		require.Positive(t, repo.cmd.BalanceCost)
		require.True(t, repo.cmd.DeferBalance)
		require.Equal(t, []int64{601}, deferral.dirty)
	})
}
