package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

var modelMappingTemplatePlatforms = map[string]struct{}{
	domain.PlatformAnthropic:   {},
	domain.PlatformOpenAI:      {},
	domain.PlatformGemini:      {},
	domain.PlatformAntigravity: {},
	domain.PlatformGrok:        {},
}

// DefaultModelMappingTemplate returns a fresh default template for a platform.
func DefaultModelMappingTemplate(platform string) map[string]string {
	result := make(map[string]string)
	if platform == domain.PlatformAntigravity {
		for source, target := range domain.DefaultAntigravityModelMapping {
			result[source] = target
		}
	}
	return result
}

// GetModelMappingTemplate returns the write-time template used when creating accounts.
// Missing, empty, or malformed values fall back to the platform default.
func (s *SettingService) GetModelMappingTemplate(ctx context.Context, platform string) (map[string]string, error) {
	if _, ok := modelMappingTemplatePlatforms[platform]; !ok {
		return nil, fmt.Errorf("unsupported platform: %s", platform)
	}

	value, err := s.settingRepo.GetValue(ctx, SettingKeyModelMappingTemplatePrefix+platform)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return DefaultModelMappingTemplate(platform), nil
		}
		return nil, fmt.Errorf("get model mapping template: %w", err)
	}
	if value == "" {
		return DefaultModelMappingTemplate(platform), nil
	}

	var mapping map[string]string
	if err := json.Unmarshal([]byte(value), &mapping); err != nil {
		return DefaultModelMappingTemplate(platform), nil
	}
	if mapping == nil {
		mapping = map[string]string{}
	}
	return mapping, nil
}

// SetModelMappingTemplate replaces the write-time template for a platform.
func (s *SettingService) SetModelMappingTemplate(ctx context.Context, platform string, mapping map[string]string) error {
	if _, ok := modelMappingTemplatePlatforms[platform]; !ok {
		return fmt.Errorf("unsupported platform: %s", platform)
	}
	if mapping == nil {
		mapping = map[string]string{}
	}

	data, err := json.Marshal(mapping)
	if err != nil {
		return fmt.Errorf("marshal model mapping template: %w", err)
	}
	return s.settingRepo.Set(ctx, SettingKeyModelMappingTemplatePrefix+platform, string(data))
}
