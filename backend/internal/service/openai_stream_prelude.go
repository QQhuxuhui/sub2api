package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/openaistreamprelude"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// A downstream gateway built on sub2api times the first token of an OpenAI
// stream from the first data event that is not a preamble
// (openAIStreamDataStartsSemanticTTFT), and some downstreams reject channels
// whose first token is slow. A group that opts in sends a protocol-neutral
// event — an empty assistant delta on Chat Completions, a keepalive event on
// Responses — for streaming requests in scope that are known to be servable, at
// a random moment of the configured range after arrival (an instant first token
// would look fake) unless real output or an error came first. The real
// upstream output follows unchanged. The writing side lives in
// handler/openai_stream_prelude.go; this file holds the setting, the scope and
// the records.

const (
	openAIStreamPreludeRecentMax = 500
	openAIStreamPreludeStatsDays = 8

	// An instant first token looks fake; a quick model takes about a second.
	openAIStreamPreludeDefaultMinMs = 800
	openAIStreamPreludeDefaultMaxMs = 1800
	openAIStreamPreludeMaxDelayMs   = 10000
)

// openAIStreamPreludeConfig is the hot-path copy of a group's setting.
type openAIStreamPreludeConfig struct {
	scope    intentJevConfig // only AllUsers / UserIDs / UserKeys are used
	min, max time.Duration
}

// OpenAIStreamPreludeDelay reports whether the group sends an early first
// event for this user's key, and the range (from the request's arrival) of the
// random moment it goes out.
func (s *IntentRouterService) OpenAIStreamPreludeDelay(ctx context.Context, groupID, userID, apiKeyID int64) (minDelay, maxDelay time.Duration, ok bool) {
	cfg := s.openAIStreamPreludeConfig(ctx, groupID)
	if cfg == nil || !cfg.scope.covers(userID, apiKeyID) {
		return 0, 0, false
	}
	return cfg.min, cfg.max, true
}

// openAIStreamPreludeAvailability is the part of the availability check the
// early event needs: real OpenAI account selection, endpoint capability
// included.
type openAIStreamPreludeAvailability interface {
	HasServableOpenAIAccountNow(ctx context.Context, groupID int64, groupPlatform, model string, capability OpenAIEndpointCapability) bool
}

// OpenAIStreamPreludeServableNow reports whether real account selection —
// with the capability the request's own selection requires — would find an
// account for the model right now, so the early event is not followed by an
// error the request could have returned with its own status.
func (s *IntentRouterService) OpenAIStreamPreludeServableNow(ctx context.Context, groupID int64, platform, model string, capability OpenAIEndpointCapability) bool {
	if s == nil {
		return false
	}
	check, ok := s.probeAvailability.(openAIStreamPreludeAvailability)
	return ok && check.HasServableOpenAIAccountNow(ctx, groupID, platform, model, capability)
}

func (s *IntentRouterService) openAIStreamPreludeConfig(ctx context.Context, groupID int64) *openAIStreamPreludeConfig {
	if s == nil {
		return nil
	}
	s.config(ctx, groupID) // loads or refreshes every snapshot
	snapshot := s.preludeConfigs.Load()
	if snapshot == nil {
		return nil
	}
	return (*snapshot)[groupID]
}

// reloadOpenAIStreamPreludeConfigs is part of reloadConfigs (under configMu).
func (s *IntentRouterService) reloadOpenAIStreamPreludeConfigs(ctx context.Context) {
	rows, err := s.entClient.OpenAIStreamPrelude.Query().All(ctx)
	if err != nil {
		slog.Warn("intent_route.stream_prelude_config_load_failed", "error", err)
		if s.preludeConfigs.Load() == nil {
			empty := map[int64]*openAIStreamPreludeConfig{}
			s.preludeConfigs.Store(&empty)
		}
		return
	}
	next := make(map[int64]*openAIStreamPreludeConfig, len(rows))
	for _, row := range rows {
		if !row.Config.Enabled {
			continue
		}
		cfg := openAIStreamPreludeDefaults(row.Config)
		next[row.GroupID] = &openAIStreamPreludeConfig{
			scope: newIntentJevConfig("", domain.IntentJevConfig{Scope: cfg.Scope, UserIDs: cfg.UserIDs, APIKeys: cfg.APIKeys}),
			min:   time.Duration(cfg.DelayMinMs) * time.Millisecond,
			max:   time.Duration(cfg.DelayMaxMs) * time.Millisecond,
		}
	}
	s.preludeConfigs.Store(&next)
}

// ---- records ----

// Outcomes of a request that got an early event.
const (
	OpenAIStreamPreludeOutcomeOK         = "ok"          // upstream output followed
	OpenAIStreamPreludeOutcomeFailed     = "failed"      // ended with an in-stream error
	OpenAIStreamPreludeOutcomeClientGone = "client_gone" // nothing followed
)

// OpenAIStreamPreludeRecord is one request that got an early event. Times are
// from the request's arrival.
type OpenAIStreamPreludeRecord struct {
	Time     time.Time `json:"time"`
	GroupID  int64     `json:"group_id"`
	UserID   int64     `json:"user_id"`
	APIKeyID int64     `json:"api_key_id"`
	Endpoint string    `json:"endpoint"`
	Model    string    `json:"model"`
	// PreludeMs is when the early event went out; OutputMs when the first
	// real byte of the response did (nil when none did).
	PreludeMs  int64  `json:"prelude_ms"`
	OutputMs   *int64 `json:"output_ms,omitempty"`
	Heartbeats int    `json:"heartbeats"`
	Outcome    string `json:"outcome"`
}

// RecordOpenAIStreamPrelude counts a request that got an early event and keeps
// it among the recent ones, off the request path.
func (s *IntentRouterService) RecordOpenAIStreamPrelude(rec OpenAIStreamPreludeRecord) {
	if s == nil || s.preludeLog == nil {
		return
	}
	day := s.jevDay(rec.Time)
	s.detached(func(ctx context.Context) error { return s.preludeLog.Add(ctx, rec, day) })
}

// openAIStreamPreludeLog keeps daily counts and the latest requests of a group.
type openAIStreamPreludeLog interface {
	Add(ctx context.Context, rec OpenAIStreamPreludeRecord, day string) error
	Day(ctx context.Context, groupID int64, day string) (OpenAIStreamPreludeDay, error)
	Recent(ctx context.Context, groupID int64, limit int) ([]OpenAIStreamPreludeRecord, error)
}

type redisOpenAIStreamPreludeLog struct{ rdb *redis.Client }

func openAIStreamPreludeDayKey(groupID int64, day string) string {
	return fmt.Sprintf("intent_route:stream_prelude:day:%d:%s", groupID, day)
}

func openAIStreamPreludeRecentKey(groupID int64) string {
	return fmt.Sprintf("intent_route:stream_prelude:recent:%d", groupID)
}

func (r *redisOpenAIStreamPreludeLog) Add(ctx context.Context, rec OpenAIStreamPreludeRecord, day string) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	dayKey, recentKey := openAIStreamPreludeDayKey(rec.GroupID, day), openAIStreamPreludeRecentKey(rec.GroupID)
	pipe := r.rdb.Pipeline()
	pipe.HIncrBy(ctx, dayKey, "sent", 1)
	if rec.Outcome == OpenAIStreamPreludeOutcomeFailed {
		pipe.HIncrBy(ctx, dayKey, "failed", 1)
	}
	pipe.Expire(ctx, dayKey, openAIStreamPreludeStatsDays*24*time.Hour)
	pipe.LPush(ctx, recentKey, raw)
	pipe.LTrim(ctx, recentKey, 0, openAIStreamPreludeRecentMax-1)
	_, err = pipe.Exec(ctx)
	return err
}

func (r *redisOpenAIStreamPreludeLog) Day(ctx context.Context, groupID int64, day string) (OpenAIStreamPreludeDay, error) {
	out := OpenAIStreamPreludeDay{Date: day}
	values, err := r.rdb.HGetAll(ctx, openAIStreamPreludeDayKey(groupID, day)).Result()
	if err != nil {
		return out, err
	}
	fmt.Sscan(values["sent"], &out.Sent)
	fmt.Sscan(values["failed"], &out.Failed)
	return out, nil
}

func (r *redisOpenAIStreamPreludeLog) Recent(ctx context.Context, groupID int64, limit int) ([]OpenAIStreamPreludeRecord, error) {
	raw, err := r.rdb.LRange(ctx, openAIStreamPreludeRecentKey(groupID), 0, int64(limit-1)).Result()
	if err != nil {
		return nil, err
	}
	out := make([]OpenAIStreamPreludeRecord, 0, len(raw))
	for _, item := range raw {
		var rec OpenAIStreamPreludeRecord
		if json.Unmarshal([]byte(item), &rec) == nil {
			out = append(out, rec)
		}
	}
	return out, nil
}

// ---- admin ----

// OpenAIStreamPreludeView is a group's setting with the names of the users and
// keys in its scope.
type OpenAIStreamPreludeView struct {
	GroupID int64                            `json:"group_id"`
	Config  domain.OpenAIStreamPreludeConfig `json:"config"`
	Scope   IntentJevScopeLabels             `json:"scope_labels"`
}

// OpenAIStreamPreludeStatus is the daily counts (today first) and the latest
// requests.
type OpenAIStreamPreludeStatus struct {
	Days   []OpenAIStreamPreludeDay    `json:"days"`
	Recent []OpenAIStreamPreludeRecord `json:"recent"`
}

type OpenAIStreamPreludeDay struct {
	Date   string `json:"date"`
	Sent   int64  `json:"sent"`
	Failed int64  `json:"failed"`
}

func openAIStreamPreludeDefaults(cfg domain.OpenAIStreamPreludeConfig) domain.OpenAIStreamPreludeConfig {
	if cfg.Scope == "" {
		cfg.Scope = domain.IntentJevScopeAll
	}
	if cfg.UserIDs == nil {
		cfg.UserIDs = []int64{}
	}
	if cfg.APIKeys == nil {
		cfg.APIKeys = []domain.IntentJevAPIKey{}
	}
	if cfg.DelayMinMs == 0 && cfg.DelayMaxMs == 0 {
		cfg.DelayMinMs, cfg.DelayMaxMs = openAIStreamPreludeDefaultMinMs, openAIStreamPreludeDefaultMaxMs
	}
	return cfg
}

// GetOpenAIStreamPrelude returns the group's setting (off when never saved).
func (s *IntentRouterService) GetOpenAIStreamPrelude(ctx context.Context, groupID int64) (*OpenAIStreamPreludeView, error) {
	row, err := s.entClient.OpenAIStreamPrelude.Query().Where(openaistreamprelude.GroupIDEQ(groupID)).Only(ctx)
	var cfg domain.OpenAIStreamPreludeConfig
	switch {
	case dbent.IsNotFound(err):
	case err != nil:
		return nil, fmt.Errorf("load stream prelude setting: %w", err)
	default:
		cfg = row.Config
	}
	return s.openAIStreamPreludeView(ctx, groupID, openAIStreamPreludeDefaults(cfg))
}

// SaveOpenAIStreamPrelude stores the group's setting; it takes effect at once.
func (s *IntentRouterService) SaveOpenAIStreamPrelude(ctx context.Context, groupID int64, in domain.OpenAIStreamPreludeConfig) (*OpenAIStreamPreludeView, error) {
	if _, err := s.entClient.Group.Get(ctx, groupID); err != nil {
		if dbent.IsNotFound(err) {
			return nil, infraerrors.NotFound("GROUP_NOT_FOUND", "group not found")
		}
		return nil, err
	}
	in = openAIStreamPreludeDefaults(in)
	if in.Scope != domain.IntentJevScopeAll && in.Scope != domain.IntentJevScopeListed {
		return nil, infraerrors.BadRequest("STREAM_PRELUDE_INVALID_SCOPE", "scope must be all or listed")
	}
	if in.DelayMinMs < 0 || in.DelayMaxMs > openAIStreamPreludeMaxDelayMs || in.DelayMinMs > in.DelayMaxMs {
		return nil, infraerrors.BadRequest("STREAM_PRELUDE_INVALID_DELAY", fmt.Sprintf("delay must satisfy 0 <= min <= max <= %d ms", openAIStreamPreludeMaxDelayMs))
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
		return nil, infraerrors.BadRequest("STREAM_PRELUDE_SCOPE_EMPTY", "choose at least one user, or apply to everyone")
	}
	err = s.entClient.OpenAIStreamPrelude.Create().SetGroupID(groupID).SetConfig(in).
		OnConflictColumns(openaistreamprelude.FieldGroupID).
		// The new row's values: setting the JSON column inside Update is not
		// marshalled by ent's upsert.
		UpdateNewValues().
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("save stream prelude setting: %w", err)
	}
	s.InvalidateConfig(ctx)
	return s.openAIStreamPreludeView(ctx, groupID, in)
}

func (s *IntentRouterService) openAIStreamPreludeView(ctx context.Context, groupID int64, cfg domain.OpenAIStreamPreludeConfig) (*OpenAIStreamPreludeView, error) {
	// The labels are looked up the way Jev's are.
	labels := &IntentRouterView{Jev: domain.IntentJevConfig{UserIDs: cfg.UserIDs, APIKeys: cfg.APIKeys}}
	if err := s.withJevScopeLabels(ctx, labels); err != nil {
		return nil, err
	}
	return &OpenAIStreamPreludeView{GroupID: groupID, Config: cfg, Scope: labels.JevScope}, nil
}

// OpenAIStreamPreludeStatus returns the group's daily counts and latest requests.
func (s *IntentRouterService) OpenAIStreamPreludeStatus(ctx context.Context, groupID int64) (*OpenAIStreamPreludeStatus, error) {
	status := &OpenAIStreamPreludeStatus{Days: []OpenAIStreamPreludeDay{}, Recent: []OpenAIStreamPreludeRecord{}}
	if s.preludeLog == nil {
		return status, nil
	}
	today := s.now()
	for i := 0; i < openAIStreamPreludeStatsDays; i++ {
		day, err := s.preludeLog.Day(ctx, groupID, s.jevDay(today.AddDate(0, 0, -i)))
		if err != nil {
			return nil, fmt.Errorf("read stream prelude stats: %w", err)
		}
		status.Days = append(status.Days, day)
	}
	recent, err := s.preludeLog.Recent(ctx, groupID, openAIStreamPreludeRecentMax)
	if err != nil {
		return nil, fmt.Errorf("read stream prelude records: %w", err)
	}
	status.Recent = recent
	return status, nil
}
