package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	mrand "math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/redis/go-redis/v9"
	"github.com/tidwall/gjson"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/monitorprobeanswer"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// sub2api's channel monitor asks every channel the same arithmetic challenge
// (channel_monitor_challenge.go), only the last two numbers vary, and marks a
// channel degraded when the answer takes six seconds or more. Downstream
// gateways whose monitors cannot be changed are hurt by a model that is slow
// to say "25". A group that opts in answers the challenge itself — the sum
// needs no model — for the users and keys in scope, as long as the group has
// an account that could serve the model right now, so a real outage still
// shows. It is independent of intent routing: it runs first and either answers
// or leaves the request untouched.
//
// The examples before the question are optional and unchecked; the question
// must be the last line.
var monitorChallengePattern = regexp.MustCompile(`^Calculate and respond with ONLY the number, nothing else\.\s*(?:Q: \d+ [+-] \d+ = \?\s*A: -?\d+\s*)*Q: (\d{1,9}) ([+-]) (\d{1,9}) = \?\s*A:$`)

// monitorChallengeSystemPrompt is the only system prompt a challenge may
// carry (the Responses monitor sends it as instructions); anything else is a
// real request.
const monitorChallengeSystemPrompt = "You are a channel health-check endpoint. Answer the arithmetic challenge exactly and briefly."

const (
	monitorAnswerMinDelay  = 300 * time.Millisecond
	monitorAnswerMaxDelay  = 800 * time.Millisecond
	monitorAnswerRecentMax = 500
	monitorAnswerStatsDays = 8
)

type monitorProtocol int

const (
	monitorProtocolResponses monitorProtocol = iota
	monitorProtocolChat
	monitorProtocolAnthropic
	monitorProtocolGemini
)

// MonitorChallenge is a recognised channel-monitor challenge.
type MonitorChallenge struct {
	Model       string
	Question    string
	Answer      string
	protocol    monitorProtocol
	promptRunes int
}

// MatchMonitorChallenge recognises a non-streaming request without tools whose
// only user message is the channel-monitor challenge and whose system prompt
// is absent or the monitor's own, on the Responses, Chat Completions,
// Anthropic Messages or Gemini generateContent endpoints, and computes the
// answer.
func MatchMonitorChallenge(path string, body []byte) (MonitorChallenge, bool) {
	path = strings.TrimRight(path, "/")
	var protocol monitorProtocol
	model := ""
	switch {
	case strings.HasSuffix(path, "/responses"):
		protocol = monitorProtocolResponses
	case strings.HasSuffix(path, "/chat/completions"):
		protocol = monitorProtocolChat
	case strings.HasSuffix(path, "/messages"):
		protocol = monitorProtocolAnthropic
	case strings.HasSuffix(path, ":generateContent"):
		protocol = monitorProtocolGemini
		_, rest, ok := strings.Cut(path, "/models/")
		if !ok {
			return MonitorChallenge{}, false
		}
		model, _, _ = strings.Cut(rest, ":")
	default:
		return MonitorChallenge{}, false
	}
	if !gjson.ValidBytes(body) {
		return MonitorChallenge{}, false
	}
	root := gjson.ParseBytes(body)
	if protocol != monitorProtocolGemini {
		model = root.Get("model").String()
	}
	model = strings.TrimSpace(model)
	if model == "" || root.Get("stream").Bool() || root.Get("tools").Exists() {
		return MonitorChallenge{}, false
	}
	prompt, ok := monitorChallengePrompt(protocol, root)
	if !ok {
		return MonitorChallenge{}, false
	}
	prompt = strings.TrimSpace(prompt)
	m := monitorChallengePattern.FindStringSubmatch(prompt)
	if m == nil {
		return MonitorChallenge{}, false
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[3])
	answer := a + b
	if m[2] == "-" {
		answer = a - b
	}
	return MonitorChallenge{
		Model: model, Question: m[1] + " " + m[2] + " " + m[3], Answer: strconv.Itoa(answer),
		protocol: protocol, promptRunes: utf8.RuneCountInString(prompt),
	}, true
}

// monitorChallengePrompt is the text of the request's only user message, when
// the system prompt (if any) is the monitor's own.
func monitorChallengePrompt(protocol monitorProtocol, root gjson.Result) (string, bool) {
	switch protocol {
	case monitorProtocolResponses:
		if !monitorSystemPromptOK(root.Get("instructions").String()) {
			return "", false
		}
		input := root.Get("input")
		if input.Type == gjson.String {
			return input.String(), true
		}
		items := input.Array()
		if len(items) != 1 {
			return "", false
		}
		return monitorUserText(items[0], "content")
	case monitorProtocolChat:
		messages := root.Get("messages").Array()
		if len(messages) == 2 {
			if role := messages[0].Get("role").String(); role != "system" && role != "developer" {
				return "", false
			}
			if !monitorSystemPromptOK(monitorPartsText(messages[0].Get("content"), "text")) {
				return "", false
			}
			messages = messages[1:]
		}
		if len(messages) != 1 {
			return "", false
		}
		return monitorUserText(messages[0], "content")
	case monitorProtocolAnthropic:
		if !monitorSystemPromptOK(monitorPartsText(root.Get("system"), "text")) {
			return "", false
		}
		messages := root.Get("messages").Array()
		if len(messages) != 1 {
			return "", false
		}
		return monitorUserText(messages[0], "content")
	case monitorProtocolGemini:
		system := root.Get("systemInstruction")
		if !system.Exists() {
			system = root.Get("system_instruction")
		}
		if !monitorSystemPromptOK(monitorPartsText(system.Get("parts"), "text")) {
			return "", false
		}
		contents := root.Get("contents").Array()
		if len(contents) != 1 {
			return "", false
		}
		return monitorUserText(contents[0], "parts")
	}
	return "", false
}

func monitorSystemPromptOK(system string) bool {
	system = strings.TrimSpace(system)
	return system == "" || system == monitorChallengeSystemPrompt
}

// monitorUserText is a user message's text — a plain string or its text parts
// — or false for any other role or for non-text parts (images, tool results).
func monitorUserText(msg gjson.Result, field string) (string, bool) {
	if role := msg.Get("role").String(); role != "" && role != "user" {
		return "", false
	}
	if t := msg.Get("type").String(); t != "" && t != "message" {
		return "", false
	}
	content := msg.Get(field)
	if content.Type == gjson.String {
		return content.String(), true
	}
	if !content.IsArray() {
		return "", false
	}
	var b strings.Builder
	for _, part := range content.Array() {
		if t := part.Get("type").String(); t != "" && t != "text" && t != "input_text" {
			return "", false
		}
		if !part.Get("text").Exists() {
			return "", false
		}
		b.WriteString(part.Get("text").String())
	}
	return b.String(), true
}

// monitorPartsText joins a system prompt given as a string or as text parts.
func monitorPartsText(v gjson.Result, field string) string {
	if v.Type == gjson.String {
		return v.String()
	}
	var b strings.Builder
	for _, part := range v.Array() {
		b.WriteString(part.Get(field).String())
	}
	return b.String()
}

// Body is the response the endpoint's API gives, in the shape the monitor
// reads its answer from.
func (c MonitorChallenge) Body(now time.Time) []byte {
	inputTokens := c.promptRunes/4 + 20
	outputTokens := 2
	var out any
	switch c.protocol {
	case monitorProtocolResponses:
		out = map[string]any{
			"id": "resp_" + monitorRandomID(), "object": "response", "created_at": now.Unix(),
			"status": "completed", "model": c.Model,
			"output": []any{map[string]any{
				"id": "msg_" + monitorRandomID(), "type": "message", "status": "completed", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": c.Answer, "annotations": []any{}}},
			}},
			"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": outputTokens, "total_tokens": inputTokens + outputTokens},
		}
	case monitorProtocolChat:
		out = map[string]any{
			"id": "chatcmpl-" + monitorRandomID(), "object": "chat.completion", "created": now.Unix(), "model": c.Model,
			"choices": []any{map[string]any{
				"index": 0, "message": map[string]any{"role": "assistant", "content": c.Answer}, "finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": inputTokens, "completion_tokens": outputTokens, "total_tokens": inputTokens + outputTokens},
		}
	case monitorProtocolAnthropic:
		out = map[string]any{
			"id": "msg_" + monitorRandomID(), "type": "message", "role": "assistant", "model": c.Model,
			"content":     []any{map[string]any{"type": "text", "text": c.Answer}},
			"stop_reason": "end_turn", "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": outputTokens},
		}
	default:
		out = map[string]any{
			"candidates": []any{map[string]any{
				"content":      map[string]any{"role": "model", "parts": []any{map[string]any{"text": c.Answer}}},
				"finishReason": "STOP", "index": 0,
			}},
			"usageMetadata": map[string]any{"promptTokenCount": inputTokens, "candidatesTokenCount": outputTokens, "totalTokenCount": inputTokens + outputTokens},
			"modelVersion":  c.Model,
			"responseId":    monitorRandomID(),
		}
	}
	body, _ := json.Marshal(out)
	return body
}

func monitorRandomID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ---- runtime ----

// MonitorProbeAvailability reports whether real account selection would find
// an account for the model right now, concurrency aside. groupPlatform is the
// group's own; the request context carries a forced or composite platform.
type MonitorProbeAvailability interface {
	HasServableAccountNow(ctx context.Context, groupID int64, groupPlatform, model string) bool
}

// SetMonitorProbeAvailability installs the availability check; without it no
// challenge is answered.
func (s *IntentRouterService) SetMonitorProbeAvailability(check MonitorProbeAvailability) {
	if s != nil {
		s.probeAvailability = check
	}
}

// monitorProbeAnswerConfig is the hot-path copy of a group's setting.
type monitorProbeAnswerConfig struct {
	scope intentJevConfig // only AllUsers / UserIDs / UserKeys are used
}

// AnswersMonitorProbes reports whether the group answers channel-monitor
// challenges for some requests, so the body is worth reading.
func (s *IntentRouterService) AnswersMonitorProbes(ctx context.Context, groupID int64) bool {
	return s.monitorProbeConfig(ctx, groupID) != nil
}

// AnswerMonitorProbe returns the response to send for a channel-monitor
// challenge, or false when the request is not one, is out of scope, or the
// group could not serve the model right now. platform is the group's.
func (s *IntentRouterService) AnswerMonitorProbe(ctx context.Context, in IntentRouteInput, platform string) (MonitorChallenge, bool) {
	cfg := s.monitorProbeConfig(ctx, in.GroupID)
	if cfg == nil || !cfg.scope.covers(in.UserID, in.APIKeyID) {
		return MonitorChallenge{}, false
	}
	challenge, ok := MatchMonitorChallenge(in.Endpoint, in.Body)
	if !ok {
		return MonitorChallenge{}, false
	}
	if s.probeAvailability == nil || !s.probeAvailability.HasServableAccountNow(ctx, in.GroupID, platform, challenge.Model) {
		return MonitorChallenge{}, false
	}
	return challenge, true
}

// MonitorAnswerDelay is how long to wait before answering, so the reply takes
// about as long as a quick model would.
func (s *IntentRouterService) MonitorAnswerDelay() time.Duration {
	if s != nil && s.probeDelay != nil {
		return s.probeDelay()
	}
	return monitorAnswerMinDelay + time.Duration(mrand.Int64N(int64(monitorAnswerMaxDelay-monitorAnswerMinDelay)))
}

// RecordMonitorAnswer counts an answered challenge and keeps it among the
// recent ones, off the request path.
func (s *IntentRouterService) RecordMonitorAnswer(in IntentRouteInput, challenge MonitorChallenge) {
	if s == nil {
		return
	}
	s.record(IntentRouteEvent{GroupID: in.GroupID, Kind: "monitor_answer", Detail: challenge.Model + " " + challenge.Question + " = " + challenge.Answer})
	if s.probeLog == nil {
		return
	}
	rec := MonitorProbeAnswerRecord{
		Time: s.now(), GroupID: in.GroupID, UserID: in.UserID, APIKeyID: in.APIKeyID,
		Endpoint: in.Endpoint, Model: challenge.Model, Question: challenge.Question, Answer: challenge.Answer,
	}
	day := s.jevDay(rec.Time)
	s.detached(func(ctx context.Context) error { return s.probeLog.Add(ctx, rec, day) })
}

func (s *IntentRouterService) monitorProbeConfig(ctx context.Context, groupID int64) *monitorProbeAnswerConfig {
	if s == nil {
		return nil
	}
	s.config(ctx, groupID) // loads or refreshes both snapshots
	snapshot := s.probeConfigs.Load()
	if snapshot == nil {
		return nil
	}
	return (*snapshot)[groupID]
}

// reloadMonitorProbeConfigs is part of reloadConfigs (under configMu).
func (s *IntentRouterService) reloadMonitorProbeConfigs(ctx context.Context) {
	rows, err := s.entClient.MonitorProbeAnswer.Query().All(ctx)
	if err != nil {
		slog.Warn("intent_route.monitor_answer_config_load_failed", "error", err)
		if s.probeConfigs.Load() == nil {
			empty := map[int64]*monitorProbeAnswerConfig{}
			s.probeConfigs.Store(&empty)
		}
		return
	}
	next := make(map[int64]*monitorProbeAnswerConfig, len(rows))
	for _, row := range rows {
		if !row.Config.Enabled {
			continue
		}
		next[row.GroupID] = &monitorProbeAnswerConfig{scope: newIntentJevConfig("", domain.IntentJevConfig{
			Scope: row.Config.Scope, UserIDs: row.Config.UserIDs, APIKeys: row.Config.APIKeys,
		})}
	}
	s.probeConfigs.Store(&next)
}

// ---- records ----

// MonitorProbeAnswerRecord is one answered challenge.
type MonitorProbeAnswerRecord struct {
	Time     time.Time `json:"time"`
	GroupID  int64     `json:"group_id"`
	UserID   int64     `json:"user_id"`
	APIKeyID int64     `json:"api_key_id"`
	Endpoint string    `json:"endpoint"`
	Model    string    `json:"model"`
	Question string    `json:"question"`
	Answer   string    `json:"answer"`
}

// monitorProbeAnswerLog keeps daily counts and the latest answers of a group.
type monitorProbeAnswerLog interface {
	Add(ctx context.Context, rec MonitorProbeAnswerRecord, day string) error
	Day(ctx context.Context, groupID int64, day string) (int64, error)
	Recent(ctx context.Context, groupID int64, limit int) ([]MonitorProbeAnswerRecord, error)
}

type redisMonitorProbeAnswerLog struct{ rdb *redis.Client }

func monitorAnswerDayKey(groupID int64, day string) string {
	return fmt.Sprintf("intent_route:monitor_answer:day:%d:%s", groupID, day)
}

func monitorAnswerRecentKey(groupID int64) string {
	return fmt.Sprintf("intent_route:monitor_answer:recent:%d", groupID)
}

func (r *redisMonitorProbeAnswerLog) Add(ctx context.Context, rec MonitorProbeAnswerRecord, day string) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	dayKey, recentKey := monitorAnswerDayKey(rec.GroupID, day), monitorAnswerRecentKey(rec.GroupID)
	pipe := r.rdb.Pipeline()
	pipe.Incr(ctx, dayKey)
	pipe.Expire(ctx, dayKey, monitorAnswerStatsDays*24*time.Hour)
	pipe.LPush(ctx, recentKey, raw)
	pipe.LTrim(ctx, recentKey, 0, monitorAnswerRecentMax-1)
	_, err = pipe.Exec(ctx)
	return err
}

func (r *redisMonitorProbeAnswerLog) Day(ctx context.Context, groupID int64, day string) (int64, error) {
	n, err := r.rdb.Get(ctx, monitorAnswerDayKey(groupID, day)).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	return n, err
}

func (r *redisMonitorProbeAnswerLog) Recent(ctx context.Context, groupID int64, limit int) ([]MonitorProbeAnswerRecord, error) {
	raw, err := r.rdb.LRange(ctx, monitorAnswerRecentKey(groupID), 0, int64(limit-1)).Result()
	if err != nil {
		return nil, err
	}
	out := make([]MonitorProbeAnswerRecord, 0, len(raw))
	for _, item := range raw {
		var rec MonitorProbeAnswerRecord
		if json.Unmarshal([]byte(item), &rec) == nil {
			out = append(out, rec)
		}
	}
	return out, nil
}

// ---- admin ----

// MonitorProbeAnswerView is a group's setting with the names of the users and
// keys in its scope.
type MonitorProbeAnswerView struct {
	GroupID int64                           `json:"group_id"`
	Config  domain.MonitorProbeAnswerConfig `json:"config"`
	Scope   IntentJevScopeLabels            `json:"scope_labels"`
}

// MonitorProbeAnswerStatus is the daily counts (today first) and the latest
// answers.
type MonitorProbeAnswerStatus struct {
	Days   []MonitorProbeAnswerDay    `json:"days"`
	Recent []MonitorProbeAnswerRecord `json:"recent"`
}

type MonitorProbeAnswerDay struct {
	Date    string `json:"date"`
	Answers int64  `json:"answers"`
}

func monitorProbeAnswerDefaults(cfg domain.MonitorProbeAnswerConfig) domain.MonitorProbeAnswerConfig {
	if cfg.Scope == "" {
		cfg.Scope = domain.IntentJevScopeAll
	}
	if cfg.UserIDs == nil {
		cfg.UserIDs = []int64{}
	}
	if cfg.APIKeys == nil {
		cfg.APIKeys = []domain.IntentJevAPIKey{}
	}
	return cfg
}

// GetMonitorProbeAnswer returns the group's setting (off when never saved).
func (s *IntentRouterService) GetMonitorProbeAnswer(ctx context.Context, groupID int64) (*MonitorProbeAnswerView, error) {
	row, err := s.entClient.MonitorProbeAnswer.Query().Where(monitorprobeanswer.GroupIDEQ(groupID)).Only(ctx)
	var cfg domain.MonitorProbeAnswerConfig
	switch {
	case dbent.IsNotFound(err):
	case err != nil:
		return nil, fmt.Errorf("load monitor answer setting: %w", err)
	default:
		cfg = row.Config
	}
	return s.monitorProbeAnswerView(ctx, groupID, monitorProbeAnswerDefaults(cfg))
}

// SaveMonitorProbeAnswer stores the group's setting; it takes effect at once.
func (s *IntentRouterService) SaveMonitorProbeAnswer(ctx context.Context, groupID int64, in domain.MonitorProbeAnswerConfig) (*MonitorProbeAnswerView, error) {
	if _, err := s.entClient.Group.Get(ctx, groupID); err != nil {
		if dbent.IsNotFound(err) {
			return nil, infraerrors.NotFound("GROUP_NOT_FOUND", "group not found")
		}
		return nil, err
	}
	in = monitorProbeAnswerDefaults(in)
	if in.Scope != domain.IntentJevScopeAll && in.Scope != domain.IntentJevScopeListed {
		return nil, infraerrors.BadRequest("MONITOR_ANSWER_INVALID_SCOPE", "scope must be all or listed")
	}
	var err error
	if in.UserIDs, err = normalizeIntentJevIDs(in.UserIDs); err != nil {
		return nil, err
	}
	seen := make(map[int64]struct{}, len(in.APIKeys))
	keys := make([]domain.IntentJevAPIKey, 0, len(in.APIKeys))
	for _, key := range in.APIKeys {
		if _, dup := seen[key.ID]; dup || key.ID <= 0 {
			continue
		}
		seen[key.ID] = struct{}{}
		keys = append(keys, domain.IntentJevAPIKey{ID: key.ID})
	}
	if len(keys) > intentJevMaxScopeIDs {
		return nil, infraerrors.BadRequest("INTENT_JEV_SCOPE_TOO_LARGE", fmt.Sprintf("at most %d API keys", intentJevMaxScopeIDs))
	}
	// Owners are filled in, and every key must belong to a chosen user —
	// the same rules as Jev's scope.
	scope := domain.IntentJevConfig{UserIDs: in.UserIDs, APIKeys: keys}
	if err := s.resolveIntentJevKeys(ctx, &scope); err != nil {
		return nil, err
	}
	in.APIKeys = scope.APIKeys
	if in.Enabled && in.Scope == domain.IntentJevScopeListed && len(in.UserIDs) == 0 {
		return nil, infraerrors.BadRequest("MONITOR_ANSWER_SCOPE_EMPTY", "choose at least one user, or answer for everyone")
	}
	err = s.entClient.MonitorProbeAnswer.Create().SetGroupID(groupID).SetConfig(in).
		OnConflictColumns(monitorprobeanswer.FieldGroupID).
		// The new row's values: setting the JSON column inside Update is not
		// marshalled by ent's upsert.
		UpdateNewValues().
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("save monitor answer setting: %w", err)
	}
	s.InvalidateConfig(ctx)
	return s.monitorProbeAnswerView(ctx, groupID, in)
}

func (s *IntentRouterService) monitorProbeAnswerView(ctx context.Context, groupID int64, cfg domain.MonitorProbeAnswerConfig) (*MonitorProbeAnswerView, error) {
	// The labels are looked up the way Jev's are.
	labels := &IntentRouterView{Jev: domain.IntentJevConfig{UserIDs: cfg.UserIDs, APIKeys: cfg.APIKeys}}
	if err := s.withJevScopeLabels(ctx, labels); err != nil {
		return nil, err
	}
	return &MonitorProbeAnswerView{GroupID: groupID, Config: cfg, Scope: labels.JevScope}, nil
}

// MonitorProbeAnswerStatus returns the group's daily counts and latest answers.
func (s *IntentRouterService) MonitorProbeAnswerStatus(ctx context.Context, groupID int64) (*MonitorProbeAnswerStatus, error) {
	status := &MonitorProbeAnswerStatus{Days: []MonitorProbeAnswerDay{}, Recent: []MonitorProbeAnswerRecord{}}
	if s.probeLog == nil {
		return status, nil
	}
	today := s.now()
	for i := 0; i < monitorAnswerStatsDays; i++ {
		day := s.jevDay(today.AddDate(0, 0, -i))
		n, err := s.probeLog.Day(ctx, groupID, day)
		if err != nil {
			return nil, fmt.Errorf("read monitor answer stats: %w", err)
		}
		status.Days = append(status.Days, MonitorProbeAnswerDay{Date: day, Answers: n})
	}
	recent, err := s.probeLog.Recent(ctx, groupID, monitorAnswerRecentMax)
	if err != nil {
		return nil, fmt.Errorf("read monitor answers: %w", err)
	}
	status.Recent = recent
	return status, nil
}
