package service

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
)

type modelMappingTemplateRepoStub struct {
	values map[string]string
}

func (s *modelMappingTemplateRepoStub) Get(context.Context, string) (*Setting, error) {
	return nil, ErrSettingNotFound
}

func (s *modelMappingTemplateRepoStub) GetValue(_ context.Context, key string) (string, error) {
	value, ok := s.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (s *modelMappingTemplateRepoStub) Set(_ context.Context, key, value string) error {
	s.values[key] = value
	return nil
}

func (s *modelMappingTemplateRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

func (s *modelMappingTemplateRepoStub) SetMultiple(_ context.Context, values map[string]string) error {
	for key, value := range values {
		s.values[key] = value
	}
	return nil
}

func (s *modelMappingTemplateRepoStub) GetAll(context.Context) (map[string]string, error) {
	values := make(map[string]string, len(s.values))
	for key, value := range s.values {
		values[key] = value
	}
	return values, nil
}

func (s *modelMappingTemplateRepoStub) Delete(_ context.Context, key string) error {
	delete(s.values, key)
	return nil
}

func TestSettingServiceGetModelMappingTemplateSupportsGrok(t *testing.T) {
	repo := &modelMappingTemplateRepoStub{values: map[string]string{}}
	service := NewSettingService(repo, &config.Config{})

	mapping, err := service.GetModelMappingTemplate(context.Background(), domain.PlatformGrok)
	if err != nil {
		t.Fatalf("GetModelMappingTemplate(grok) returned error: %v", err)
	}
	if len(mapping) != 0 {
		t.Fatalf("GetModelMappingTemplate(grok) = %#v, want empty default", mapping)
	}
}

func TestSettingServiceSetModelMappingTemplateSupportsGrok(t *testing.T) {
	repo := &modelMappingTemplateRepoStub{values: map[string]string{}}
	service := NewSettingService(repo, &config.Config{})
	want := map[string]string{"grok-4": "grok-4-fast"}

	if err := service.SetModelMappingTemplate(context.Background(), domain.PlatformGrok, want); err != nil {
		t.Fatalf("SetModelMappingTemplate(grok) returned error: %v", err)
	}

	var stored map[string]string
	key := SettingKeyModelMappingTemplatePrefix + domain.PlatformGrok
	if err := json.Unmarshal([]byte(repo.values[key]), &stored); err != nil {
		t.Fatalf("stored Grok model mapping template is invalid JSON: %v", err)
	}
	if !reflect.DeepEqual(stored, want) {
		t.Fatalf("stored Grok model mapping template = %#v, want %#v", stored, want)
	}
}
