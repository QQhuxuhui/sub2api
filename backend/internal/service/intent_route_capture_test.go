//go:build unit

package service

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// waitCaptured returns the misses written so far once count of them exist.
func waitCaptured(t *testing.T, dir string, count int) []IntentRouteMiss {
	t.Helper()
	var misses []IntentRouteMiss
	require.Eventually(t, func() bool {
		misses = nil
		files, _ := filepath.Glob(filepath.Join(dir, "misses-*.jsonl"))
		for _, name := range files {
			f, err := os.Open(name)
			if err != nil {
				return false
			}
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			for sc.Scan() {
				var m IntentRouteMiss
				require.NoError(t, json.Unmarshal(sc.Bytes(), &m))
				misses = append(misses, m)
			}
			_ = f.Close()
		}
		return len(misses) >= count
	}, 5*time.Second, 50*time.Millisecond)
	return misses
}

func TestIntentRouteCapture_KeepsOnlyMissesOfWatchedKeys(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "api_keys"), []byte("11, 99\n"), 0o644))
	f := newIntentRouteFixture(t)
	f.saveRouter(t, keywordOnly([]string{"鹈鹕"}, []string{"笑话"}))
	f.svc.capture = newIntentRouteCapture(dir, 0)
	f.svc.capture.reloadKeys()

	hidden := "画一只鸟 <user_instructions>pelican 鹈鹕 svg</user_instructions>"
	miss := f.request(userTurns("第一句话", hidden))
	miss.ClientRequestID = "abc"
	requireNoPreference(t, f.svc.Decide(ctx, miss))

	hit := f.svc.Decide(ctx, f.request(userTurns("画一只鹈鹕")))
	require.Equal(t, "coding", hit.Intent)

	other := IntentRouteInput{GroupID: f.groupID, APIKeyID: 12, Body: []byte(userTurns("不在名单里的令牌"))}
	requireNoPreference(t, f.svc.Decide(ctx, other))

	misses := waitCaptured(t, dir, 1)
	time.Sleep(2500 * time.Millisecond) // one more flush: nothing else may arrive
	misses = waitCaptured(t, dir, 1)
	require.Len(t, misses, 1, "hits and unwatched keys are not captured")
	got := misses[0]
	require.Equal(t, int64(11), got.APIKeyID)
	require.Equal(t, "client:abc", got.RequestID)
	require.Equal(t, []string{"画一只鸟", "第一句话"}, got.Checked, "the texts the rules saw, newest first")
	require.Equal(t, hidden, got.RawLastUser, "the raw turn keeps what stripping removed")
	require.Equal(t, "第一句话", got.FirstUser)
	require.Equal(t, 3, got.Turns)
}

func TestIntentRouteCapture_OffWithoutKeyFile(t *testing.T) {
	c := newIntentRouteCapture(t.TempDir(), 0)
	c.reloadKeys()
	require.False(t, c.watching(11))
	var nilCapture *intentRouteCapture
	require.False(t, nilCapture.watching(11))
}

func TestIntentRouteCapture_StopsAtSizeLimit(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "api_keys"), []byte("11"), 0o644))
	c := newIntentRouteCapture(dir, 600)
	c.reloadKeys()
	in := IntentRouteInput{GroupID: 1, APIKeyID: 11}
	for i := 0; i < 20; i++ {
		c.add(in, intentRequestView{body: []byte(userTurns("一段普通的话"))}, []string{"一段普通的话"})
	}
	misses := waitCaptured(t, dir, 1)
	time.Sleep(2500 * time.Millisecond)
	misses = waitCaptured(t, dir, 1)
	require.Less(t, len(misses), 20)
	files, _ := filepath.Glob(filepath.Join(dir, "misses-*.jsonl"))
	var size int64
	for _, name := range files {
		info, err := os.Stat(name)
		require.NoError(t, err)
		size += info.Size()
	}
	require.LessOrEqual(t, size, int64(600))
}
