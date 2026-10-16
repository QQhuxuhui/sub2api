package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newOpenAIMappedAccountForBillingTest(platform string, mapping map[string]any) *Account {
	return &Account{
		ID:          30,
		Platform:    platform,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"model_mapping": mapping},
	}
}

func TestOpenAIGatewayServiceRecordUsage_AccountMappingBillsRequestedModel(t *testing.T) {
	usage := OpenAIUsage{InputTokens: 20, OutputTokens: 10}
	tokens := UsageTokens{InputTokens: 20, OutputTokens: 10}
	mapping := map[string]any{"gpt-5.4": "gpt-5.5"}

	tests := []struct {
		name               string
		account            *Account
		billingModel       string
		billingModelSource string
		wantBillingModel   string
	}{
		{
			name:               "channel_mapped without channel rename bills requested model",
			account:            newOpenAIMappedAccountForBillingTest(PlatformOpenAI, mapping),
			billingModel:       "gpt-5.5",
			billingModelSource: BillingModelSourceChannelMapped,
			wantBillingModel:   "gpt-5.4",
		},
		{
			name:             "no channel bills requested model",
			account:          newOpenAIMappedAccountForBillingTest(PlatformOpenAI, mapping),
			billingModel:     "gpt-5.5",
			wantBillingModel: "gpt-5.4",
		},
		{
			name:               "upstream source keeps account-mapped model",
			account:            newOpenAIMappedAccountForBillingTest(PlatformOpenAI, mapping),
			billingModel:       "gpt-5.5",
			billingModelSource: BillingModelSourceUpstream,
			wantBillingModel:   "gpt-5.5",
		},
		{
			name:               "billing model decided elsewhere is kept",
			account:            newOpenAIMappedAccountForBillingTest(PlatformOpenAI, mapping),
			billingModel:       "gpt-5.1",
			billingModelSource: BillingModelSourceChannelMapped,
			wantBillingModel:   "gpt-5.1",
		},
		{
			name:               "non openai platform keeps account-mapped model",
			account:            newOpenAIMappedAccountForBillingTest(PlatformGrok, mapping),
			billingModel:       "gpt-5.5",
			billingModelSource: BillingModelSourceChannelMapped,
			wantBillingModel:   "gpt-5.5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			userRepo := &openAIRecordUsageUserRepoStub{}
			subRepo := &openAIRecordUsageSubRepoStub{}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)

			expectedCost, err := svc.billingService.CalculateCost(tt.wantBillingModel, tokens, 1.1)
			require.NoError(t, err)

			err = svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{
					RequestID:     "resp_account_mapping_billing",
					Model:         "gpt-5.4",
					BillingModel:  tt.billingModel,
					UpstreamModel: tt.billingModel,
					Usage:         usage,
					Duration:      time.Second,
				},
				APIKey:  &APIKey{ID: 10},
				User:    &User{ID: 20},
				Account: tt.account,
				ChannelUsageFields: ChannelUsageFields{
					OriginalModel:      "gpt-5.4",
					ChannelMappedModel: "gpt-5.4",
					BillingModelSource: tt.billingModelSource,
				},
			})

			require.NoError(t, err)
			require.NotNil(t, usageRepo.lastLog)
			require.InDelta(t, expectedCost.ActualCost, usageRepo.lastLog.ActualCost, 1e-12)
			require.InDelta(t, expectedCost.ActualCost, userRepo.lastAmount, 1e-12)
			require.True(t, usageRepo.lastLog.ActualCost > 0, "cost must not be zero")
		})
	}
}

func TestOpenAIGatewayServiceRecordUsage_AccountMappingFallsBackWhenRequestedUnpriced(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)
	tokens := UsageTokens{InputTokens: 20, OutputTokens: 10}

	expectedCost, err := svc.billingService.CalculateCost("gpt-5.5", tokens, 1.1)
	require.NoError(t, err)

	err = svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID:     "resp_account_mapping_unpriced",
			Model:         "zz-unpriced-alias",
			BillingModel:  "gpt-5.5",
			UpstreamModel: "gpt-5.5",
			Usage:         OpenAIUsage{InputTokens: 20, OutputTokens: 10},
			Duration:      time.Second,
		},
		APIKey:  &APIKey{ID: 10},
		User:    &User{ID: 20},
		Account: newOpenAIMappedAccountForBillingTest(PlatformOpenAI, map[string]any{"zz-unpriced-alias": "gpt-5.5"}),
		ChannelUsageFields: ChannelUsageFields{
			OriginalModel:      "zz-unpriced-alias",
			ChannelMappedModel: "zz-unpriced-alias",
			BillingModelSource: BillingModelSourceChannelMapped,
		},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.InDelta(t, expectedCost.ActualCost, usageRepo.lastLog.ActualCost, 1e-12)
	require.True(t, usageRepo.lastLog.ActualCost > 0, "cost must not be zero")
}

func TestOpenAIAccountMappingBillingTestModelsHaveDistinctPrices(t *testing.T) {
	svc := newOpenAIRecordUsageServiceForTest(&openAIRecordUsageLogRepoStub{}, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	tokens := UsageTokens{InputTokens: 20, OutputTokens: 10}
	requested, err := svc.billingService.CalculateCost("gpt-5.4", tokens, 1.1)
	require.NoError(t, err)
	mapped, err := svc.billingService.CalculateCost("gpt-5.5", tokens, 1.1)
	require.NoError(t, err)
	require.NotEqual(t, requested.ActualCost, mapped.ActualCost, "测试依赖两个模型价格不同才能区分计费模型")
}
