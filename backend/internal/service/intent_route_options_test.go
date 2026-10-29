//go:build unit

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func TestIntentRoute_KeywordMaxCharsSkipsLongMessages(t *testing.T) {
	ctx := context.Background()
	f, judge, sink := newJevFixture(t, 0.10, nil)
	f.saveRouter(t, func(in *IntentRouterInput) {
		jevOn(nil)(in)
		in.KeywordMaxChars = 20
	})
	require.Equal(t, 20, f.svc.mustRouter(t, f.groupID).KeywordMaxChars)

	// 20 runes (CJK counted as one each) still matches.
	short := "画一只鹈鹕" + strings.Repeat("好", 15)
	require.Equal(t, "coding", f.svc.Decide(ctx, f.request(userTurns(short))).Intent)
	require.Len(t, sink.hits, 1)

	// One rune over: the keyword is ignored and Jev judges the turn instead.
	long := short + "好"
	requireNoPreference(t, f.svc.Decide(ctx, f.request(userTurns(long))))
	require.Equal(t, 1, judge.calls, "a long message falls through to Jev like any keyword miss")
	require.Len(t, sink.hits, 1)

	// A short follow-up does not resurrect a keyword in a long earlier message.
	requireNoPreference(t, f.svc.Decide(ctx, f.request(userTurns(long, "继续"))))
	// But it still finds one in a short earlier message.
	require.Equal(t, "coding", f.svc.Decide(ctx, f.request(userTurns(short, "继续"))).Intent)

	// The admin test follows the same limit.
	res, err := f.svc.TestClassify(ctx, f.groupID, long)
	require.NoError(t, err)
	require.NotEqual(t, "keyword", res.MatchedBy)

	// 0 lifts the limit, and takes effect on save.
	f.saveRouter(t, jevOn(nil))
	require.Equal(t, "coding", f.svc.Decide(ctx, f.request(userTurns(long))).Intent)
}

func TestIntentRoute_KeywordMaxCharsValidation(t *testing.T) {
	f := newIntentRouteFixture(t)
	save := func(n int) error {
		_, err := f.svc.SaveRouter(context.Background(), f.groupID, IntentRouterInput{Enabled: true, KeywordMaxChars: n, Rules: []domain.IntentRule{
			{Name: "coding", Keywords: []string{"x"}, Enabled: true, AccountIDs: []int64{f.accountID(t, "coding-1")}},
		}})
		return err
	}
	require.Equal(t, "INTENT_INVALID_KEYWORD_MAX_CHARS", infraerrors.Reason(save(-1)))
	require.Equal(t, "INTENT_INVALID_KEYWORD_MAX_CHARS", infraerrors.Reason(save(intentRouteMaxKeywordChars+1)))
	require.NoError(t, save(500))
	require.Equal(t, 500, f.svc.mustRouter(t, f.groupID).KeywordMaxChars)
}

func TestIntentRoute_UnavailableStatusIsConfiguredAndCarried(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	f.saveRouter(t, keywordOnly([]string{"鹈鹕"}, []string{"笑话"}))
	view := f.svc.mustRouter(t, f.groupID)
	require.Equal(t, 503, view.UnavailableStatus, "unset means 503")
	require.Equal(t, 503, f.svc.Decide(ctx, f.request(userTurns("画一只鹈鹕"))).UnavailableStatus)

	f.saveRouter(t, func(in *IntentRouterInput) {
		keywordOnly([]string{"鹈鹕"}, []string{"笑话"})(in)
		in.UnavailableStatus = 429
	})
	require.Equal(t, 429, f.svc.Decide(ctx, f.request(userTurns("画一只鹈鹕"))).UnavailableStatus, "takes effect on save")

	for _, bad := range []int{200, 400, 500, 504, -1} {
		_, err := f.svc.SaveRouter(ctx, f.groupID, IntentRouterInput{Enabled: true, UnavailableStatus: bad, Rules: []domain.IntentRule{
			{Name: "coding", Keywords: []string{"x"}, Enabled: true, AccountIDs: []int64{f.accountID(t, "coding-1")}},
		}})
		require.Equal(t, "INTENT_INVALID_UNAVAILABLE_STATUS", infraerrors.Reason(err), bad)
	}
	require.Equal(t, 503, IntentRouteUnavailableStatus(ErrIntentRouteUnavailable), "a bare error reports 503")
}
