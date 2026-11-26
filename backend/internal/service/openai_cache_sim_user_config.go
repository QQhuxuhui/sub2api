package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// OpenAI cache simulation by downstream user: a global configuration and
// per-user overrides, both stored as settings. A user's override wins over the
// global configuration, which wins over the accounts' own simulation; with the
// global configuration off and no override, nothing changes.
const (
	SettingKeyOpenAICacheSimGlobal = "openai_cache_sim_global"
	SettingKeyOpenAICacheSimUsers  = "openai_cache_sim_users"
)

const (
	openAICacheSimUserCacheTTL      = 10 * time.Second
	openAICacheSimUserErrorCacheTTL = 2 * time.Second
	openAICacheSimUserDBTimeout     = 2 * time.Second
)

// Sources of a user's effective cache simulation.
const (
	OpenAICacheSimSourceUser    = "user"
	OpenAICacheSimSourceGlobal  = "global"
	OpenAICacheSimSourceAccount = "account"
)

// openAICacheSimUserSettings is one consistent view of both settings keys.
type openAICacheSimUserSettings struct {
	global    OpenAICacheSimConfig
	overrides map[int64]OpenAICacheSimConfig
}

type openAICacheSimUserCacheEntry struct {
	settings  *openAICacheSimUserSettings
	expiresAt int64
}

var (
	openAICacheSimUserCache   atomic.Pointer[openAICacheSimUserCacheEntry]
	openAICacheSimUserGen     atomic.Uint64
	openAICacheSimUserSF      singleflight.Group
	openAICacheSimUserPublish sync.Mutex
	// openAICacheSimUserSaveMu serialises read-modify-write saves of either key.
	openAICacheSimUserSaveMu sync.Mutex
)

func resetOpenAICacheSimUserCacheForTest() {
	openAICacheSimUserCache.Store(nil)
	openAICacheSimUserGen.Add(1)
	openAICacheSimUserSF = singleflight.Group{}
}

func emptyOpenAICacheSimUserSettings() *openAICacheSimUserSettings {
	return &openAICacheSimUserSettings{global: defaultOpenAICacheSimConfig(), overrides: map[int64]OpenAICacheSimConfig{}}
}

// parseOpenAICacheSimGlobal: empty → off with defaults; a broken value → off.
func parseOpenAICacheSimGlobal(raw string) (OpenAICacheSimConfig, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultOpenAICacheSimConfig(), nil
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return defaultOpenAICacheSimConfig(), err
	}
	cfg, err := parseOpenAICacheSimConfig(obj)
	if err != nil {
		return defaultOpenAICacheSimConfig(), err
	}
	return cfg, nil
}

// parseOpenAICacheSimUsers skips entries that do not parse, keeping the rest.
func parseOpenAICacheSimUsers(raw string) (map[int64]OpenAICacheSimConfig, error) {
	out := map[int64]OpenAICacheSimConfig{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out, nil
	}
	var obj map[string]map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return out, err
	}
	var firstErr error
	for key, value := range obj {
		id, err := strconv.ParseInt(strings.TrimSpace(key), 10, 64)
		if err != nil || id <= 0 {
			if firstErr == nil {
				firstErr = fmt.Errorf("invalid user id %q", key)
			}
			continue
		}
		cfg, err := parseOpenAICacheSimConfig(value)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("user %d: %w", id, err)
			}
			continue
		}
		out[id] = cfg
	}
	return out, firstErr
}

func marshalOpenAICacheSimUsers(overrides map[int64]OpenAICacheSimConfig) (string, error) {
	ids := make([]int64, 0, len(overrides))
	for id := range overrides {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	obj := make(map[string]map[string]any, len(ids))
	for _, id := range ids {
		obj[strconv.FormatInt(id, 10)] = overrides[id].ToExtra()
	}
	data, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func loadOpenAICacheSimUserSettings(ctx context.Context, repo SettingRepository) (*openAICacheSimUserSettings, error) {
	out := emptyOpenAICacheSimUserSettings()
	if repo == nil {
		return out, nil
	}
	globalRaw, err := repo.GetValue(ctx, SettingKeyOpenAICacheSimGlobal)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return nil, err
	}
	usersRaw, err := repo.GetValue(ctx, SettingKeyOpenAICacheSimUsers)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return nil, err
	}
	global, perr := parseOpenAICacheSimGlobal(globalRaw)
	if perr != nil {
		slog.Warn("openai_cache_sim_global_parse_failed", "error", perr)
	}
	overrides, perr := parseOpenAICacheSimUsers(usersRaw)
	if perr != nil {
		slog.Warn("openai_cache_sim_users_parse_failed", "error", perr)
	}
	out.global, out.overrides = global, overrides
	return out, nil
}

func publishOpenAICacheSimUserCache(gen uint64, settings *openAICacheSimUserSettings, ttl time.Duration) {
	openAICacheSimUserPublish.Lock()
	defer openAICacheSimUserPublish.Unlock()
	if openAICacheSimUserGen.Load() != gen {
		return
	}
	openAICacheSimUserCache.Store(&openAICacheSimUserCacheEntry{settings: settings, expiresAt: time.Now().Add(ttl).UnixNano()})
}

func storeOpenAICacheSimUserCacheAfterSave(settings *openAICacheSimUserSettings) {
	openAICacheSimUserPublish.Lock()
	defer openAICacheSimUserPublish.Unlock()
	openAICacheSimUserGen.Add(1)
	openAICacheSimUserCache.Store(&openAICacheSimUserCacheEntry{settings: settings, expiresAt: time.Now().Add(openAICacheSimUserCacheTTL).UnixNano()})
}

// cachedOpenAICacheSimUserSettings is the hot-path read (10s process cache).
// A failed load keeps the last known value; without one, everything is off.
func cachedOpenAICacheSimUserSettings(ctx context.Context, repo SettingRepository) *openAICacheSimUserSettings {
	cached := openAICacheSimUserCache.Load()
	if cached != nil && time.Now().UnixNano() < cached.expiresAt {
		return cached.settings
	}
	if repo == nil {
		if cached != nil {
			return cached.settings
		}
		return emptyOpenAICacheSimUserSettings()
	}
	result, _, _ := openAICacheSimUserSF.Do("settings", func() (any, error) {
		gen := openAICacheSimUserGen.Load()
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAICacheSimUserDBTimeout)
		defer cancel()
		settings, err := loadOpenAICacheSimUserSettings(dbCtx, repo)
		if err != nil {
			slog.Warn("openai_cache_sim_user_settings_load_failed", "error", err)
			settings = emptyOpenAICacheSimUserSettings()
			if previous := openAICacheSimUserCache.Load(); previous != nil {
				settings = previous.settings
			}
			publishOpenAICacheSimUserCache(gen, settings, openAICacheSimUserErrorCacheTTL)
			return settings, nil
		}
		publishOpenAICacheSimUserCache(gen, settings, openAICacheSimUserCacheTTL)
		if latest := openAICacheSimUserCache.Load(); latest != nil && openAICacheSimUserGen.Load() != gen {
			return latest.settings, nil
		}
		return settings, nil
	})
	settings, _ := result.(*openAICacheSimUserSettings)
	if settings == nil {
		return emptyOpenAICacheSimUserSettings()
	}
	return settings
}

// OpenAICacheSimUserMode is a user's effective cache simulation.
type OpenAICacheSimUserMode struct {
	Config OpenAICacheSimConfig
	Source string
	// Active: the by-user simulation applies. Source=user with Active=false
	// is an explicit opt-out: nothing is changed for that user.
	Active bool
}

// Off reports an explicit opt-out.
func (m OpenAICacheSimUserMode) Off() bool {
	return m.Source == OpenAICacheSimSourceUser && !m.Active
}

func (s *openAICacheSimUserSettings) resolve(userID int64) OpenAICacheSimUserMode {
	if s == nil || userID <= 0 {
		return OpenAICacheSimUserMode{Config: defaultOpenAICacheSimConfig(), Source: OpenAICacheSimSourceAccount}
	}
	if cfg, ok := s.overrides[userID]; ok {
		return OpenAICacheSimUserMode{Config: cfg, Source: OpenAICacheSimSourceUser, Active: cfg.Enabled}
	}
	if s.global.Enabled {
		return OpenAICacheSimUserMode{Config: s.global, Source: OpenAICacheSimSourceGlobal, Active: true}
	}
	return OpenAICacheSimUserMode{Config: s.global, Source: OpenAICacheSimSourceAccount}
}

func resolveOpenAICacheSimUserMode(ctx context.Context, repo SettingRepository, userID int64) OpenAICacheSimUserMode {
	return cachedOpenAICacheSimUserSettings(ctx, repo).resolve(userID)
}

// ---- admin read/write (direct DB) ----

func (s *SettingService) openAICacheSimUserSettingsDirect(ctx context.Context) (*openAICacheSimUserSettings, error) {
	if s == nil || s.settingRepo == nil {
		return emptyOpenAICacheSimUserSettings(), nil
	}
	return loadOpenAICacheSimUserSettings(ctx, s.settingRepo)
}

// GetOpenAICacheSimGlobal returns the stored global configuration.
func (s *SettingService) GetOpenAICacheSimGlobal(ctx context.Context) (OpenAICacheSimConfig, error) {
	settings, err := s.openAICacheSimUserSettingsDirect(ctx)
	if err != nil {
		return defaultOpenAICacheSimConfig(), fmt.Errorf("get openai cache sim global: %w", err)
	}
	return settings.global, nil
}

// GetOpenAICacheSimOverrides returns every stored per-user override.
func (s *SettingService) GetOpenAICacheSimOverrides(ctx context.Context) (map[int64]OpenAICacheSimConfig, error) {
	settings, err := s.openAICacheSimUserSettingsDirect(ctx)
	if err != nil {
		return nil, fmt.Errorf("get openai cache sim overrides: %w", err)
	}
	return settings.overrides, nil
}

// OpenAICacheSimUserModes resolves users against the cached settings (pages).
func (s *SettingService) OpenAICacheSimUserModes(ctx context.Context, userIDs []int64) map[int64]OpenAICacheSimUserMode {
	var repo SettingRepository
	if s != nil {
		repo = s.settingRepo
	}
	settings := cachedOpenAICacheSimUserSettings(ctx, repo)
	out := make(map[int64]OpenAICacheSimUserMode, len(userIDs))
	for _, id := range userIDs {
		out[id] = settings.resolve(id)
	}
	return out
}

// SetOpenAICacheSimGlobal saves the global configuration.
func (s *SettingService) SetOpenAICacheSimGlobal(ctx context.Context, cfg OpenAICacheSimConfig) (OpenAICacheSimConfig, error) {
	if s == nil || s.settingRepo == nil {
		return cfg, fmt.Errorf("setting repository unavailable")
	}
	if _, err := parseOpenAICacheSimConfig(cfg.ToExtra()); err != nil {
		return cfg, err
	}
	data, err := json.Marshal(cfg.ToExtra())
	if err != nil {
		return cfg, err
	}
	openAICacheSimUserSaveMu.Lock()
	defer openAICacheSimUserSaveMu.Unlock()
	settings, err := s.openAICacheSimUserSettingsDirect(ctx)
	if err != nil {
		return cfg, err
	}
	if err := s.settingRepo.Set(ctx, SettingKeyOpenAICacheSimGlobal, string(data)); err != nil {
		return cfg, err
	}
	settings.global = cfg
	storeOpenAICacheSimUserCacheAfterSave(settings)
	return cfg, nil
}

// SetOpenAICacheSimUser saves a user's override; nil removes it (inherit).
func (s *SettingService) SetOpenAICacheSimUser(ctx context.Context, userID int64, cfg *OpenAICacheSimConfig) error {
	if s == nil || s.settingRepo == nil {
		return fmt.Errorf("setting repository unavailable")
	}
	if userID <= 0 {
		return fmt.Errorf("invalid user id")
	}
	if cfg != nil {
		if _, err := parseOpenAICacheSimConfig(cfg.ToExtra()); err != nil {
			return err
		}
	}
	openAICacheSimUserSaveMu.Lock()
	defer openAICacheSimUserSaveMu.Unlock()
	settings, err := s.openAICacheSimUserSettingsDirect(ctx)
	if err != nil {
		return err
	}
	overrides := make(map[int64]OpenAICacheSimConfig, len(settings.overrides)+1)
	for id, c := range settings.overrides {
		overrides[id] = c
	}
	if cfg == nil {
		delete(overrides, userID)
	} else {
		overrides[userID] = *cfg
	}
	data, err := marshalOpenAICacheSimUsers(overrides)
	if err != nil {
		return err
	}
	if err := s.settingRepo.Set(ctx, SettingKeyOpenAICacheSimUsers, data); err != nil {
		return err
	}
	settings.overrides = overrides
	storeOpenAICacheSimUserCacheAfterSave(settings)
	return nil
}

// ---- fixed-point percentages ----

// openAICacheSimBasisPoints converts a percentage (0~100, decimals allowed)
// to basis points so that rate comparisons and 128-token rounding agree.
func openAICacheSimBasisPoints(pct float64) int64 {
	if pct <= 0 {
		return 0
	}
	if pct >= 100 {
		return 10000
	}
	return int64(pct*100 + 0.5)
}

// ParseOpenAICacheSimConfigMap validates a configuration in its JSON form
// (the same rules as an account's openai_cache_simulation).
func ParseOpenAICacheSimConfigMap(raw map[string]any) (OpenAICacheSimConfig, error) {
	return parseOpenAICacheSimConfig(raw)
}
