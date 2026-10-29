//go:build unit

package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const monitorChallengeText = "Calculate and respond with ONLY the number, nothing else.\n\nQ: 3 + 5 = ?\nA: 8\n\nQ: 12 - 7 = ?\nA: 5\n\nQ: 37 + 64 = ?\nA:"

func TestMatchMonitorChallenge(t *testing.T) {
	text := intentJSONString(monitorChallengeText)
	system := intentJSONString(monitorChallengeSystemPrompt)
	cases := []struct {
		name, path, body, model, answer string
		protocol                        monitorProtocol
	}{
		{"responses, as the sub2api monitor sends it", "/v1/responses",
			`{"model":"gpt-6.1-sol","instructions":` + system + `,"input":` + text + `,"max_output_tokens":50,"stream":false}`, "gpt-6.1-sol", "101", monitorProtocolResponses},
		{"responses, message array", "/responses",
			`{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":` + text + `}]}]}`, "m", "101", monitorProtocolResponses},
		{"chat completions", "/v1/chat/completions",
			`{"model":"m","messages":[{"role":"user","content":` + text + `}],"max_tokens":50,"stream":false}`, "m", "101", monitorProtocolChat},
		{"chat with the monitor's system message", "/v1/chat/completions",
			`{"model":"m","messages":[{"role":"system","content":` + system + `},{"role":"user","content":` + text + `}]}`, "m", "101", monitorProtocolChat},
		{"anthropic messages", "/v1/messages",
			`{"model":"claude-x","max_tokens":50,"messages":[{"role":"user","content":` + text + `}]}`, "claude-x", "101", monitorProtocolAnthropic},
		{"anthropic with the system prompt as blocks", "/v1/messages",
			`{"model":"claude-x","system":[{"type":"text","text":` + system + `}],"messages":[{"role":"user","content":[{"type":"text","text":` + text + `}]}]}`, "claude-x", "101", monitorProtocolAnthropic},
		{"gemini, model from the path", "/v1beta/models/gemini-3-pro:generateContent",
			`{"contents":[{"role":"user","parts":[{"text":` + text + `}]}],"generationConfig":{"maxOutputTokens":50}}`, "gemini-3-pro", "101", monitorProtocolGemini},
		{"one example, subtraction", "/v1/chat/completions",
			`{"model":"m","messages":[{"role":"user","content":"Calculate and respond with ONLY the number, nothing else.\n\nQ: 3 + 5 = ?\nA: 8\n\nQ: 64 - 37 = ?\nA:"}]}`, "m", "27", monitorProtocolChat},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := MatchMonitorChallenge(c.path, []byte(c.body))
			require.True(t, ok)
			require.Equal(t, c.answer, got.Answer)
			require.Equal(t, c.model, got.Model)
			require.Equal(t, c.protocol, got.protocol)
		})
	}

	other := intentJSONString("You are a helpful assistant.")
	for name, c := range map[string][2]string{
		"streaming":                 {"/v1/chat/completions", `{"model":"m","stream":true,"messages":[{"role":"user","content":` + text + `}]}`},
		"with tools":                {"/v1/chat/completions", `{"model":"m","tools":[],"messages":[{"role":"user","content":` + text + `}]}`},
		"more turns":                {"/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"ok"},{"role":"user","content":` + text + `}]}`},
		"other chat system message": {"/v1/chat/completions", `{"model":"m","messages":[{"role":"system","content":` + other + `},{"role":"user","content":` + text + `}]}`},
		"other instructions":        {"/v1/responses", `{"model":"m","instructions":` + other + `,"input":` + text + `}`},
		"other anthropic system":    {"/v1/messages", `{"model":"m","system":` + other + `,"messages":[{"role":"user","content":` + text + `}]}`},
		"other gemini system":       {"/v1beta/models/g:generateContent", `{"systemInstruction":{"parts":[{"text":` + other + `}]},"contents":[{"role":"user","parts":[{"text":` + text + `}]}]}`},
		"gemini streaming":          {"/v1beta/models/g:streamGenerateContent", `{"contents":[{"role":"user","parts":[{"text":` + text + `}]}]}`},
		"with an image":             {"/v1/responses", `{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":` + text + `},{"type":"input_image","image_url":"https://x/y.png"}]}]}`},
		"other question":            {"/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"Q: 3 + 5 = ?\nA:"}]}`},
		"text after question":       {"/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":` + intentJSONString(monitorChallengeText+" and explain") + `}]}`},
		"no model":                  {"/v1/responses", `{"input":` + text + `}`},
		"other endpoint":            {"/v1/embeddings", `{"model":"m","input":` + text + `}`},
		"not json":                  {"/v1/responses", `{"model":`},
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			_, ok := MatchMonitorChallenge(c[0], []byte(c[1]))
			require.False(t, ok)
		})
	}
}

// Each protocol answers in the shape its monitor reads the answer from.
func TestMonitorChallengeBody(t *testing.T) {
	now := time.Unix(1700000000, 0)
	for _, c := range []struct {
		path, body, textPath, modelPath string
	}{
		{"/v1/responses", `{"model":"gpt-x","input":` + intentJSONString(monitorChallengeText) + `}`, "output.0.content.0.text", "model"},
		{"/v1/chat/completions", `{"model":"gpt-x","messages":[{"role":"user","content":` + intentJSONString(monitorChallengeText) + `}]}`, "choices.0.message.content", "model"},
		{"/v1/messages", `{"model":"gpt-x","messages":[{"role":"user","content":` + intentJSONString(monitorChallengeText) + `}]}`, "content.0.text", "model"},
		{"/v1beta/models/gpt-x:generateContent", `{"contents":[{"parts":[{"text":` + intentJSONString(monitorChallengeText) + `}]}]}`, "candidates.0.content.parts.0.text", "modelVersion"},
	} {
		challenge, ok := MatchMonitorChallenge(c.path, []byte(c.body))
		require.True(t, ok, c.path)
		body := challenge.Body(now)
		require.True(t, gjson.ValidBytes(body))
		require.Equal(t, "101", gjson.GetBytes(body, c.textPath).String(), c.path)
		require.Equal(t, "gpt-x", gjson.GetBytes(body, c.modelPath).String(), c.path)
	}
}

type stubProbeAvailability struct {
	ok           bool
	calls        []string
	capabilities []OpenAIEndpointCapability // HasServableOpenAIAccountNow calls
}

func (s *stubProbeAvailability) HasServableAccountNow(_ context.Context, _ int64, platform, model string) bool {
	s.calls = append(s.calls, platform+"/"+model)
	return s.ok
}

type memoryMonitorAnswerLog struct {
	mu     sync.Mutex
	days   map[string]int64
	recent []MonitorProbeAnswerRecord
}

func (m *memoryMonitorAnswerLog) Add(_ context.Context, rec MonitorProbeAnswerRecord, day string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.days == nil {
		m.days = map[string]int64{}
	}
	m.days[day]++
	m.recent = append([]MonitorProbeAnswerRecord{rec}, m.recent...)
	return nil
}

func (m *memoryMonitorAnswerLog) Day(_ context.Context, _ int64, day string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.days[day], nil
}

func (m *memoryMonitorAnswerLog) Recent(_ context.Context, _ int64, limit int) ([]MonitorProbeAnswerRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.recent) > limit {
		return m.recent[:limit], nil
	}
	return m.recent, nil
}

func monitorChallengeInput(f *intentRouteFixture, userID, keyID int64) IntentRouteInput {
	return IntentRouteInput{
		GroupID: f.groupID, UserID: userID, APIKeyID: keyID, Endpoint: "/v1/responses",
		Body: []byte(`{"model":"gpt-6.1-sol","input":` + intentJSONString(monitorChallengeText) + `}`),
	}
}

// The setting is the group's own: it needs no intent router, and answers only
// in scope and only when the group could serve the model right now.
func TestMonitorProbeAnswer_ScopeAndAvailability(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	avail := &stubProbeAvailability{ok: true}
	f.svc.SetMonitorProbeAvailability(avail)
	alice, aliceKeys := jevUserWithKeys(t, f, "alice@example.com", 2)
	bob, bobKeys := jevUserWithKeys(t, f, "bob@example.com", 1)

	_, ok := f.svc.AnswerMonitorProbe(ctx, monitorChallengeInput(f, alice, aliceKeys[0]), PlatformOpenAI)
	require.False(t, ok, "off until saved")
	require.False(t, f.svc.AnswersMonitorProbes(ctx, f.groupID))

	_, err := f.svc.SaveMonitorProbeAnswer(ctx, f.groupID, domain.MonitorProbeAnswerConfig{Enabled: true})
	require.NoError(t, err)
	require.True(t, f.svc.AnswersMonitorProbes(ctx, f.groupID), "takes effect on save")
	challenge, ok := f.svc.AnswerMonitorProbe(ctx, monitorChallengeInput(f, bob, bobKeys[0]), PlatformOpenAI)
	require.True(t, ok)
	require.Equal(t, "101", challenge.Answer)
	require.Equal(t, []string{"openai/gpt-6.1-sol"}, avail.calls)

	avail.ok = false
	_, ok = f.svc.AnswerMonitorProbe(ctx, monitorChallengeInput(f, bob, bobKeys[0]), PlatformOpenAI)
	require.False(t, ok, "nothing could serve the model: the real error must show")
	avail.ok = true

	in := monitorChallengeInput(f, bob, bobKeys[0])
	in.Body = []byte(`{"model":"gpt-6.1-sol","input":"hi"}`)
	_, ok = f.svc.AnswerMonitorProbe(ctx, in, PlatformOpenAI)
	require.False(t, ok, "only the challenge")

	// Listed: Alice narrowed to her first key; Bob not listed.
	view, err := f.svc.SaveMonitorProbeAnswer(ctx, f.groupID, domain.MonitorProbeAnswerConfig{
		Enabled: true, Scope: domain.IntentJevScopeListed, UserIDs: []int64{alice}, APIKeys: []domain.IntentJevAPIKey{{ID: aliceKeys[0]}},
	})
	require.NoError(t, err)
	require.Equal(t, []domain.IntentJevAPIKey{{ID: aliceKeys[0], UserID: alice}}, view.Config.APIKeys)
	require.Equal(t, "alice@example.com", view.Scope.Users[0].Email)
	for _, c := range []struct {
		user, key int64
		want      bool
	}{{alice, aliceKeys[0], true}, {alice, aliceKeys[1], false}, {bob, bobKeys[0], false}} {
		_, ok := f.svc.AnswerMonitorProbe(ctx, monitorChallengeInput(f, c.user, c.key), PlatformOpenAI)
		require.Equal(t, c.want, ok, c)
	}

	_, err = f.svc.SaveMonitorProbeAnswer(ctx, f.groupID, domain.MonitorProbeAnswerConfig{Enabled: false, Scope: domain.IntentJevScopeListed, UserIDs: []int64{alice}})
	require.NoError(t, err)
	_, ok = f.svc.AnswerMonitorProbe(ctx, monitorChallengeInput(f, alice, aliceKeys[0]), PlatformOpenAI)
	require.False(t, ok, "switched off")
	got, err := f.svc.GetMonitorProbeAnswer(ctx, f.groupID)
	require.NoError(t, err)
	require.False(t, got.Config.Enabled)
	require.Equal(t, []int64{alice}, got.Config.UserIDs, "a switched-off scope is kept")
}

func TestMonitorProbeAnswer_Validation(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	alice, _ := jevUserWithKeys(t, f, "alice@example.com", 1)
	_, bobKeys := jevUserWithKeys(t, f, "bob@example.com", 1)
	save := func(cfg domain.MonitorProbeAnswerConfig) error {
		_, err := f.svc.SaveMonitorProbeAnswer(ctx, f.groupID, cfg)
		return err
	}
	require.Equal(t, "MONITOR_ANSWER_SCOPE_EMPTY", infraerrors.Reason(save(domain.MonitorProbeAnswerConfig{Enabled: true, Scope: domain.IntentJevScopeListed})))
	require.Equal(t, "MONITOR_ANSWER_INVALID_SCOPE", infraerrors.Reason(save(domain.MonitorProbeAnswerConfig{Scope: "some"})))
	require.Equal(t, "INTENT_JEV_API_KEY_USER_NOT_LISTED", infraerrors.Reason(save(domain.MonitorProbeAnswerConfig{
		Enabled: true, Scope: domain.IntentJevScopeListed, UserIDs: []int64{alice}, APIKeys: []domain.IntentJevAPIKey{{ID: bobKeys[0]}},
	})))
	_, err := f.svc.SaveMonitorProbeAnswer(ctx, 99999, domain.MonitorProbeAnswerConfig{})
	require.Equal(t, "GROUP_NOT_FOUND", infraerrors.Reason(err))
}

func TestMonitorProbeAnswer_RecordsAndStatus(t *testing.T) {
	ctx := context.Background()
	f := newIntentRouteFixture(t)
	log := &memoryMonitorAnswerLog{}
	f.svc.probeLog = log
	in := monitorChallengeInput(f, 7, 8)
	challenge, ok := MatchMonitorChallenge(in.Endpoint, in.Body)
	require.True(t, ok)
	f.svc.RecordMonitorAnswer(in, challenge)
	f.svc.RecordMonitorAnswer(in, challenge)
	require.Eventually(t, func() bool {
		n, _ := log.Day(ctx, f.groupID, f.svc.jevDay(f.svc.now()))
		return n == 2
	}, time.Second, 10*time.Millisecond)

	status, err := f.svc.MonitorProbeAnswerStatus(ctx, f.groupID)
	require.NoError(t, err)
	require.Len(t, status.Days, monitorAnswerStatsDays)
	require.Equal(t, int64(2), status.Days[0].Answers, "today first")
	require.Len(t, status.Recent, 2)
	require.Equal(t, MonitorProbeAnswerRecord{
		Time: f.svc.now(), GroupID: f.groupID, UserID: 7, APIKeyID: 8, Endpoint: "/v1/responses",
		Model: "gpt-6.1-sol", Question: "37 + 64", Answer: "101",
	}, status.Recent[0])
	require.Equal(t, []string{"monitor_answer", "monitor_answer"}, f.store.kinds())
}

func TestMonitorAnswerDelayIsWithinBounds(t *testing.T) {
	s := &IntentRouterService{}
	for i := 0; i < 200; i++ {
		d := s.MonitorAnswerDelay()
		require.GreaterOrEqual(t, d, monitorAnswerMinDelay)
		require.Less(t, d, monitorAnswerMaxDelay)
	}
}
