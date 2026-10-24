package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Wei-Shaw/sub2api/internal/pkg/erroridentity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

// startErrorIdentity is invoked once during provider wiring, before serving.
func (s *OpenAIGatewayService) startErrorIdentity() {
	if s == nil || s.accountRepo == nil || s.errorIdentity != nil {
		return
	}
	s.errorIdentity = erroridentity.NewCache(func(ctx context.Context) ([]string, error) {
		var models []string
		for page := 1; ; page++ {
			accounts, paging, err := s.accountRepo.List(ctx, pagination.PaginationParams{Page: page, PageSize: 1000, SortBy: "id", SortOrder: pagination.SortOrderAsc})
			if err != nil {
				return nil, err
			}
			for _, a := range accounts {
				switch mapping := a.Credentials["model_mapping"].(type) {
				case map[string]any:
					for k, v := range mapping {
						if name, ok := v.(string); ok && k != name {
							models = append(models, name)
						}
					}
				case map[string]string:
					for k, v := range mapping {
						if k != v {
							models = append(models, v)
						}
					}
				}
			}
			if paging == nil {
				return nil, fmt.Errorf("account pagination missing on page %d", page)
			}
			if page >= paging.Pages || len(accounts) == 0 {
				break
			}
		}
		return models, nil
	})
	if s.schedulerSnapshot != nil {
		s.schedulerSnapshot.setErrorIdentityRefresh(s.errorIdentity.Notify)
	}
	s.errorIdentity.Start(context.Background(), func(err error) { slog.Error("openai error identity snapshot refresh failed", "error", err) })
}

func (s *OpenAIGatewayService) ErrorIdentitySnapshot() *erroridentity.Scrubber {
	if s == nil {
		return nil
	}
	return s.errorIdentity.Snapshot()
}
func (s *OpsService) ErrorIdentitySnapshot() *erroridentity.Scrubber {
	if s == nil {
		return nil
	}
	return s.openAIGatewayService.ErrorIdentitySnapshot()
}
func (s *OpsService) ErrorIdentityHealth() erroridentity.Health {
	if s == nil || s.openAIGatewayService == nil {
		return erroridentity.Health{}
	}
	return s.openAIGatewayService.errorIdentity.Health()
}

// Each WS turn captures its model and immutable snapshot before any upstream I/O.
func (s *OpenAIGatewayService) newErrorIdentityTurn(account *Account, model string) func([]byte) []byte {
	snapshot := s.ErrorIdentitySnapshot()
	return func(payload []byte) []byte {
		if account == nil || account.Platform != PlatformOpenAI {
			return payload
		}
		return snapshot.JSON(payload, model, false)
	}
}
