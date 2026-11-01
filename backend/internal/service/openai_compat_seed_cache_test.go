//go:build unit

package service

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func referenceSeed(body []byte) string {
	return deriveOpenAIContentSessionSeedWith(body, func(raw string) string {
		return computeCompatSeedJSON(json.RawMessage(raw))
	})
}

func seedSampleBodies() []string {
	large := strings.Repeat("x", 200<<10)
	spaced := "[" + strings.Repeat(" ", 200<<10) + `{"b":1,"a":2}]`
	return []string{
		``, `{}`, `[]`, `{`,
		`{"model":"m","tools":[{"b":1.50,"a":"<&>"}],"instructions":"i","input":"hi"}`,
		`{"model":"m","tools":[{"a":1,"a":2}],"input":[{"role":"system","content":"s"},{"role":"user","content":[{"type":"input_text","text":"t"}]}]}`,
		`{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"u"},{"role":"system","content":"late"}]}`,
		`{"model":"m","functions":[{"n":12345678901234567890123}],"input":[{"type":"input_text","text":"t"}]}`,
		`{"model":"m","tools":[1,],"input":"x"}`,
		fmt.Sprintf(`{"model":"m","tools":[{"d":"%s"}],"input":[{"role":"user","content":[{"type":"input_image","image_url":"%s"}]}]}`, large, large),
		fmt.Sprintf(`{"model":"m","tools":%s,"input":[{"role":"system","content":"%s"},{"role":"developer","content":"%s"},{"role":"user","content":"%s"}]}`, spaced, large, large, large),
		"{\"model\":\"m\",\"tools\":[\"\xff\"],\"input\":\"x\"}",
	}
}

func TestDeriveOpenAIContentSessionSeed_CacheAndMemoMatchBaseline(t *testing.T) {
	for _, sample := range seedSampleBodies() {
		body := []byte(sample)
		want := referenceSeed(body)
		for round := 0; round < 2; round++ { // 第二轮命中进程缓存
			require.Equal(t, want, deriveOpenAIContentSessionSeed(body), "body prefix: %.80q", body)
		}
		memo := &openAISessionSeedMemo{}
		for round := 0; round < 2; round++ { // 第二轮命中 memo
			require.Equal(t, want, deriveOpenAIContentSessionSeedWith(body, memo.normalize), "body prefix: %.80q", body)
		}
	}
}

func FuzzSessionSeedMatchesBaseline(f *testing.F) {
	for _, sample := range seedSampleBodies() {
		f.Add([]byte(sample))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		want := referenceSeed(body)
		require.Equal(t, want, deriveOpenAIContentSessionSeed(body))
		memo := &openAISessionSeedMemo{}
		require.Equal(t, want, deriveOpenAIContentSessionSeedWith(body, memo.normalize))
	})
}

func TestCompatSeedLRU_Limits(t *testing.T) {
	t.Run("skips oversized entries", func(t *testing.T) {
		lru := newCompatSeedLRU(4, 1024, 10)
		lru.put(json.RawMessage("123456"), "12345")
		_, ok := lru.get(json.RawMessage("123456"))
		require.False(t, ok)
		require.Zero(t, lru.bytes)
	})
	t.Run("evicts oldest by entry count", func(t *testing.T) {
		lru := newCompatSeedLRU(2, 1024, 100)
		lru.put(json.RawMessage("a"), "1")
		lru.put(json.RawMessage("b"), "2")
		_, ok := lru.get(json.RawMessage("a")) // a 变成最新
		require.True(t, ok)
		lru.put(json.RawMessage("c"), "3")
		_, ok = lru.get(json.RawMessage("b"))
		require.False(t, ok)
		_, ok = lru.get(json.RawMessage("a"))
		require.True(t, ok)
		require.Equal(t, 4, lru.bytes)
	})
	t.Run("evicts by total bytes", func(t *testing.T) {
		lru := newCompatSeedLRU(100, 10, 10)
		for i := 0; i < 5; i++ {
			lru.put(json.RawMessage(fmt.Sprintf("k%d", i)), "vvv")
		}
		require.LessOrEqual(t, lru.bytes, 10)
		require.Equal(t, 2, len(lru.items))
	})
	t.Run("does not double count a repeated key", func(t *testing.T) {
		lru := newCompatSeedLRU(4, 100, 100)
		lru.put(json.RawMessage("a"), "1")
		lru.put(json.RawMessage("a"), "1")
		require.Equal(t, 2, lru.bytes)
	})
}

func TestCompatSeedLRU_ConcurrentUse(t *testing.T) {
	lru := newCompatSeedLRU(8, 1024, 64)
	done := make(chan struct{})
	for g := 0; g < 8; g++ {
		go func(seed int64) {
			defer func() { done <- struct{}{} }()
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < 2000; i++ {
				key := json.RawMessage(fmt.Sprintf("%d", rng.Intn(32)))
				if value, ok := lru.get(key); ok {
					require.Equal(t, "v"+string(key), value)
				} else {
					lru.put(key, "v"+string(key))
				}
			}
		}(int64(g))
	}
	for g := 0; g < 8; g++ {
		<-done
	}
}

func TestOpenAISessionSeedMemo_Budget(t *testing.T) {
	small := `{"a":1}`
	memo := &openAISessionSeedMemo{}
	memo.normalize(small)
	require.Empty(t, memo.entries, "放得进进程缓存的片段不进 memo")

	spaced := "[" + strings.Repeat(" ", compatSeedCacheMaxEntryBytes) + "1]"
	require.Equal(t, "[1]", memo.normalize(spaced))
	require.Len(t, memo.entries, 1)
	require.Equal(t, 3, memo.valueBytes, "预算只算 value，key 是请求体视图")

	for i := 0; i < openAISessionSeedMemoMaxEntries+4; i++ {
		memo.normalize(fmt.Sprintf(`["%d",%s1]`, i, strings.Repeat(" ", compatSeedCacheMaxEntryBytes)))
	}
	require.Len(t, memo.entries, openAISessionSeedMemoMaxEntries)
}

func TestOpenAISessionSeedMemo_ValueBudgetSkipsLargeValues(t *testing.T) {
	memo := &openAISessionSeedMemo{valueBytes: openAISessionSeedMemoMaxValues - 1}
	raw := `"` + strings.Repeat("x", compatSeedCacheMaxEntryBytes) + `"`
	require.Equal(t, raw, memo.normalize(raw))
	require.Empty(t, memo.entries)
}

func TestOpenAISessionSeedMemo_HitDoesNotRenormalizeOrAllocateKey(t *testing.T) {
	raw := `"` + strings.Repeat("x", 1<<20) + `"`
	memo := &openAISessionSeedMemo{}
	want := memo.normalize(raw)
	allocs := testing.AllocsPerRun(10, func() {
		require.Equal(t, want, memo.normalize(raw))
	})
	require.Less(t, allocs, 3.0)
}

func TestOpenAISessionSeedMemo_Lifecycle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	body := []byte(fmt.Sprintf(`{"model":"m","input":"hi","tools":["%s"]}`, strings.Repeat("x", 100<<10)))
	svc := &OpenAIGatewayService{}

	require.Nil(t, takeOpenAISessionSeedMemo(c), "未开启时没有 memo")
	withoutMemo := svc.GenerateSessionHash(c, body)

	EnableOpenAISessionSeedMemo(c)
	withMemo := svc.GenerateSessionHash(c, body)
	require.Equal(t, withoutMemo, withMemo)
	memo := takeOpenAISessionSeedMemo(c)
	require.NotNil(t, memo)
	require.Len(t, memo.entries, 1)

	require.Nil(t, openAISessionSeedMemoFromContext(c), "取走后 context 不再持有")
	require.Nil(t, takeOpenAISessionSeedMemo(c), "换号重入拿不到 memo")
	require.Equal(t, withoutMemo, svc.GenerateSessionHash(c, body))
	require.Len(t, memo.entries, 1, "取走后不再填充")
	require.Equal(t, withoutMemo, svc.generateSessionHashWithMemo(c, body, memo))
}
