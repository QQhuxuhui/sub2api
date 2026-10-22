package service

import (
	"fmt"
	"maps"
	"math"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// openAICacheSimulationKey 是账号 Extra 里「缓存模拟」配置对象的键：
//
//	{"enabled": true, "lo": 82, "hi": 88, "half_life_minutes": 60, "cap": 95}
//
// 只对 OpenAI 平台账号生效，默认关。
const openAICacheSimulationKey = "openai_cache_simulation"

// OpenAICacheSimulationExtraKey 对外暴露的 Extra 键名（管理端快捷修改用）。
const OpenAICacheSimulationExtraKey = openAICacheSimulationKey

const (
	openAICacheSimDefaultLo       = 82.0
	openAICacheSimDefaultHi       = 88.0
	openAICacheSimDefaultHalfLife = 60
	openAICacheSimDefaultCap      = 95.0
	// openAICacheSimMinPromptTokens：OpenAI 提示缓存的最小前缀，低于它的请求真实世界不可能有缓存。
	openAICacheSimMinPromptTokens = 1024
	// openAICacheSimGranularity：真实 cached_tokens 永远是 128 的倍数。
	openAICacheSimGranularity = 128
)

// OpenAICacheSimConfig 是解析后的账号级缓存模拟配置。
type OpenAICacheSimConfig struct {
	Enabled bool
	// Lo/Hi 是目标区间（百分比）。每笔在 [Lo, Hi] 内随机取目标。
	Lo float64
	Hi float64
	// HalfLifeMinutes 是滚动窗口（衰减计数器）的半衰期。
	HalfLifeMinutes int
	// Cap 是单笔上限：模拟后 cached 不超过 prompt 的 Cap%。
	Cap float64
}

func defaultOpenAICacheSimConfig() OpenAICacheSimConfig {
	return OpenAICacheSimConfig{
		Lo:              openAICacheSimDefaultLo,
		Hi:              openAICacheSimDefaultHi,
		HalfLifeMinutes: openAICacheSimDefaultHalfLife,
		Cap:             openAICacheSimDefaultCap,
	}
}

// HalfLife 返回半衰期时长。
func (c OpenAICacheSimConfig) HalfLife() time.Duration {
	if c.HalfLifeMinutes <= 0 {
		return time.Duration(openAICacheSimDefaultHalfLife) * time.Minute
	}
	return time.Duration(c.HalfLifeMinutes) * time.Minute
}

// ToExtra 序列化成 Extra 里的对象形态。
func (c OpenAICacheSimConfig) ToExtra() map[string]any {
	return map[string]any{
		"enabled":           c.Enabled,
		"lo":                c.Lo,
		"hi":                c.Hi,
		"half_life_minutes": c.HalfLifeMinutes,
		"cap":               c.Cap,
	}
}

// OpenAICacheSimulation 返回该账号的缓存模拟配置；非 OpenAI 平台或未配置时为关闭态的默认值。
func (a *Account) OpenAICacheSimulation() OpenAICacheSimConfig {
	cfg := defaultOpenAICacheSimConfig()
	if a == nil || !a.IsOpenAI() || a.Extra == nil {
		return cfg
	}
	raw, ok := a.Extra[openAICacheSimulationKey].(map[string]any)
	if !ok {
		return cfg
	}
	parsed, err := parseOpenAICacheSimConfig(raw)
	if err != nil {
		return cfg
	}
	return parsed
}

func extraNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	default:
		return 0, false
	}
}

func parseOpenAICacheSimConfig(raw map[string]any) (OpenAICacheSimConfig, error) {
	cfg := defaultOpenAICacheSimConfig()
	if raw == nil {
		return cfg, nil
	}
	if v, exists := raw["enabled"]; exists {
		b, ok := v.(bool)
		if !ok {
			return cfg, fmt.Errorf("enabled must be a boolean")
		}
		cfg.Enabled = b
	}
	if v, exists := raw["lo"]; exists {
		n, ok := extraNumber(v)
		if !ok || math.IsNaN(n) || n < 0 || n > 100 {
			return cfg, fmt.Errorf("lo must be a number between 0 and 100")
		}
		cfg.Lo = n
	}
	if v, exists := raw["hi"]; exists {
		n, ok := extraNumber(v)
		if !ok || math.IsNaN(n) || n < 0 || n > 100 {
			return cfg, fmt.Errorf("hi must be a number between 0 and 100")
		}
		cfg.Hi = n
	}
	if cfg.Lo > cfg.Hi {
		return cfg, fmt.Errorf("lo must not exceed hi")
	}
	if v, exists := raw["half_life_minutes"]; exists {
		n, ok := extraNumber(v)
		if !ok || math.IsNaN(n) || n < 1 || n > 1440 || n != math.Trunc(n) {
			return cfg, fmt.Errorf("half_life_minutes must be an integer between 1 and 1440")
		}
		cfg.HalfLifeMinutes = int(n)
	}
	if v, exists := raw["cap"]; exists {
		n, ok := extraNumber(v)
		if !ok || math.IsNaN(n) || n < 50 || n > 100 {
			return cfg, fmt.Errorf("cap must be a number between 50 and 100")
		}
		cfg.Cap = n
	}
	return cfg, nil
}

// ValidateOpenAICacheSimulationExtra 校验 Extra 里的缓存模拟配置（存在时）。
func ValidateOpenAICacheSimulationExtra(platform string, extra map[string]any) error {
	if platform != PlatformOpenAI {
		return nil
	}
	raw, exists := extra[openAICacheSimulationKey]
	if !exists || raw == nil {
		return nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return infraerrors.BadRequest(
			"OPENAI_CACHE_SIMULATION_INVALID",
			"openai_cache_simulation must be an object",
		)
	}
	if _, err := parseOpenAICacheSimConfig(obj); err != nil {
		return infraerrors.BadRequest(
			"OPENAI_CACHE_SIMULATION_INVALID",
			"openai_cache_simulation: "+err.Error(),
		)
	}
	return nil
}

// cloneOpenAICacheSimulationExtra 复制父账号的缓存模拟配置对象（影子账号继承用）。
func cloneOpenAICacheSimulationExtra(parent *Account) map[string]any {
	if parent == nil || parent.Extra == nil {
		return nil
	}
	raw, ok := parent.Extra[openAICacheSimulationKey].(map[string]any)
	if !ok {
		return nil
	}
	return maps.Clone(raw)
}
