//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func (s *stubProbeAvailability) HasServableOpenAIAccountNow(ctx context.Context, groupID int64, platform, model string, capability OpenAIEndpointCapability) bool {
	s.capabilities = append(s.capabilities, capability)
	return s.HasServableAccountNow(ctx, groupID, platform, model)
}

func preludeCovers(s *IntentRouterService, ctx context.Context, groupID, userID, keyID int64) bool {
	_, _, ok := s.OpenAIStreamPreludeDelay(ctx, groupID, userID, keyID)
	return ok
}

func TestOpenAIStreamPrelude_Delay(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	alice, aliceKeys := jevUserWithKeys(t, f, "alice@example.com", 1)

	view, err := f.svc.SaveOpenAIStreamPrelude(ctx, f.groupID, domain.OpenAIStreamPreludeConfig{Enabled: true})
	require.NoError(t, err)
	require.Equal(t, 800, view.Config.DelayMinMs, "default range")
	require.Equal(t, 1800, view.Config.DelayMaxMs)
	lo, hi, ok := f.svc.OpenAIStreamPreludeDelay(ctx, f.groupID, alice, aliceKeys[0])
	require.True(t, ok)
	require.Equal(t, 800*time.Millisecond, lo)
	require.Equal(t, 1800*time.Millisecond, hi)

	_, err = f.svc.SaveOpenAIStreamPrelude(ctx, f.groupID, domain.OpenAIStreamPreludeConfig{Enabled: true, DelayMinMs: 500, DelayMaxMs: 900})
	require.NoError(t, err)
	lo, hi, _ = f.svc.OpenAIStreamPreludeDelay(ctx, f.groupID, alice, aliceKeys[0])
	require.Equal(t, 500*time.Millisecond, lo)
	require.Equal(t, 900*time.Millisecond, hi)

	for _, bad := range [][2]int{{-1, 100}, {900, 500}, {0, 10001}} {
		_, err = f.svc.SaveOpenAIStreamPrelude(ctx, f.groupID, domain.OpenAIStreamPreludeConfig{Enabled: true, DelayMinMs: bad[0], DelayMaxMs: bad[1]})
		require.Equal(t, "STREAM_PRELUDE_INVALID_DELAY", infraerrors.Reason(err), "%v", bad)
	}
}

func TestOpenAIStreamPrelude_ScopeAndAvailability(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	avail := &stubProbeAvailability{ok: true}
	f.svc.SetMonitorProbeAvailability(avail)
	alice, aliceKeys := jevUserWithKeys(t, f, "alice@example.com", 2)
	bob, bobKeys := jevUserWithKeys(t, f, "bob@example.com", 1)

	require.False(t, preludeCovers(f.svc, ctx, f.groupID, alice, aliceKeys[0]), "off until saved")

	_, err := f.svc.SaveOpenAIStreamPrelude(ctx, f.groupID, domain.OpenAIStreamPreludeConfig{Enabled: true})
	require.NoError(t, err)
	require.True(t, preludeCovers(f.svc, ctx, f.groupID, bob, bobKeys[0]), "everyone, at once")
	require.False(t, preludeCovers(f.svc, ctx, f.groupID+1, bob, bobKeys[0]), "other groups untouched")

	require.True(t, f.svc.OpenAIStreamPreludeServableNow(ctx, f.groupID, PlatformOpenAI, "gpt-6.1-sol", OpenAIEndpointCapabilityChatCompletions))
	require.Equal(t, []OpenAIEndpointCapability{OpenAIEndpointCapabilityChatCompletions}, avail.capabilities, "the request's capability is checked")
	avail.ok = false
	require.False(t, f.svc.OpenAIStreamPreludeServableNow(ctx, f.groupID, PlatformOpenAI, "gpt-6.1-sol", ""))

	// Listed: Alice narrowed to her first key; Bob not listed.
	view, err := f.svc.SaveOpenAIStreamPrelude(ctx, f.groupID, domain.OpenAIStreamPreludeConfig{
		Enabled: true, Scope: domain.IntentJevScopeListed, UserIDs: []int64{alice}, APIKeys: []domain.IntentJevAPIKey{{ID: aliceKeys[0]}},
	})
	require.NoError(t, err)
	require.Equal(t, alice, view.Config.APIKeys[0].UserID, "owner filled in")
	require.True(t, preludeCovers(f.svc, ctx, f.groupID, alice, aliceKeys[0]))
	require.False(t, preludeCovers(f.svc, ctx, f.groupID, alice, aliceKeys[1]))
	require.False(t, preludeCovers(f.svc, ctx, f.groupID, bob, bobKeys[0]))

	_, err = f.svc.SaveOpenAIStreamPrelude(ctx, f.groupID, domain.OpenAIStreamPreludeConfig{Enabled: false, Scope: domain.IntentJevScopeAll})
	require.NoError(t, err)
	require.False(t, preludeCovers(f.svc, ctx, f.groupID, alice, aliceKeys[0]), "off again")

	got, err := f.svc.GetOpenAIStreamPrelude(ctx, f.groupID)
	require.NoError(t, err)
	require.False(t, got.Config.Enabled)
}

func TestOpenAIStreamPrelude_Validation(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	save := func(cfg domain.OpenAIStreamPreludeConfig) error {
		_, err := f.svc.SaveOpenAIStreamPrelude(ctx, f.groupID, cfg)
		return err
	}
	require.Equal(t, "STREAM_PRELUDE_SCOPE_EMPTY", infraerrors.Reason(save(domain.OpenAIStreamPreludeConfig{Enabled: true, Scope: domain.IntentJevScopeListed})))
	require.Equal(t, "STREAM_PRELUDE_INVALID_SCOPE", infraerrors.Reason(save(domain.OpenAIStreamPreludeConfig{Scope: "some"})))
	_, err := f.svc.SaveOpenAIStreamPrelude(ctx, 99999, domain.OpenAIStreamPreludeConfig{})
	require.Equal(t, "GROUP_NOT_FOUND", infraerrors.Reason(err))

	got, err := f.svc.GetOpenAIStreamPrelude(ctx, f.groupID)
	require.NoError(t, err)
	require.Equal(t, domain.IntentJevScopeAll, got.Config.Scope, "defaults when never saved")
	require.NotNil(t, got.Config.UserIDs)
}
