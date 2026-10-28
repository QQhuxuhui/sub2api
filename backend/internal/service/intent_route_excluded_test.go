//go:build unit

package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func withModel(body, model string) []byte {
	return []byte(`{"model":"` + model + `",` + body[1:])
}

func TestIntentRoute_ExcludedModelsSkipRoutingEntirely(t *testing.T) {
	ctx := context.Background()
	f, judge, sink := newJevFixture(t, 0.99, nil)
	f.saveRouter(t, func(in *IntentRouterInput) {
		jevOn(nil)(in)
		in.ExcludedModels = []string{" codex-auto-review ", "Gemini-*", "CODEX-AUTO-REVIEW", ""}
	})
	view := f.svc.mustRouter(t, f.groupID)
	require.Equal(t, []string{"codex-auto-review", "Gemini-*"}, view.ExcludedModels, "trimmed, deduplicated case-insensitively")

	captureDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(captureDir, "api_keys"), []byte("11"), 0o644))
	f.svc.capture = newIntentRouteCapture(captureDir, 0)
	f.svc.capture.reloadKeys()

	keywordTurn := userTurns("画一只鹈鹕")
	for _, model := range []string{"codex-auto-review", "Codex-Auto-Review"} {
		in := f.request(keywordTurn)
		in.Body = withModel(keywordTurn, model)
		d := f.svc.Decide(ctx, in)
		requireNoPreference(t, d)
	}
	// Gemini carries the model in the path; the wildcard is a prefix match.
	gemini := f.request(userTurns("Reply OK"))
	gemini.Endpoint = "/v1beta/models/gemini-3.1-pro:streamGenerateContent"
	requireNoPreference(t, f.svc.Decide(ctx, gemini))
	require.Zero(t, judge.calls, "excluded models never reach Jev")
	require.Empty(t, sink.hits)
	files, _ := filepath.Glob(filepath.Join(captureDir, "misses-*.jsonl"))
	require.Empty(t, files, "nor the miss capture")

	// Other models are routed as before.
	in := f.request(keywordTurn)
	in.Body = withModel(keywordTurn, "gpt-6.1-sol")
	require.Equal(t, "coding", f.svc.Decide(ctx, in).Intent)
	require.False(t, (&intentRouterConfig{ExcludedModels: []string{"gemini-*"}}).excludesModel("gpt-gemini-x"), "prefix, not substring")
}

func TestIntentRoute_ExcludedModelsValidation(t *testing.T) {
	f := newIntentRouteFixture(t)
	save := func(models []string) error {
		_, err := f.svc.SaveRouter(context.Background(), f.groupID, IntentRouterInput{Enabled: true, ExcludedModels: models, Rules: []domain.IntentRule{
			{Name: "coding", Keywords: []string{"x"}, Enabled: true, AccountIDs: []int64{f.accountID(t, "coding-1")}},
		}})
		return err
	}
	require.Equal(t, "INTENT_EXCLUDED_MODEL_ALL", infraerrors.Reason(save([]string{"*"})))
	require.NoError(t, save(nil))
	require.Equal(t, []string{}, f.svc.mustRouter(t, f.groupID).ExcludedModels)
}
