package service

import (
	"context"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/erroridentity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

type identityAccountRepo struct {
	AccountRepository
	pages []int
}

func (r *identityAccountRepo) List(_ context.Context, p pagination.PaginationParams) ([]Account, *pagination.PaginationResult, error) {
	r.pages = append(r.pages, p.Page)
	if p.Page == 1 {
		return []Account{{Status: "disabled", Credentials: map[string]any{"model_mapping": map[string]any{"public": "hidden", "same": "same", "bad": 1}}}}, &pagination.PaginationResult{Pages: 2}, nil
	}
	return []Account{{Credentials: map[string]any{"model_mapping": map[string]string{"public2": "second"}}}}, &pagination.PaginationResult{Pages: 2}, nil
}
func TestErrorIdentityAccountLoading(t *testing.T) {
	repo := &identityAccountRepo{}
	s := &OpenAIGatewayService{accountRepo: repo}
	s.startErrorIdentity()
	defer s.CloseOpenAIWSPool()
	if len(repo.pages) != 2 {
		t.Fatal(repo.pages)
	}
	if got := s.ErrorIdentitySnapshot().Text("hidden second same", "public"); got != "public public same" {
		t.Fatal(got)
	}
	if !s.errorIdentity.Health().Ready {
		t.Fatal("not ready")
	}
}

func TestErrorIdentityCommittedAccountEvents(t *testing.T) {
	s := &SchedulerSnapshotService{}
	calls := 0
	s.setErrorIdentityRefresh(func() { calls++ })
	for _, kind := range []string{SchedulerOutboxEventAccountChanged, SchedulerOutboxEventAccountBulkChanged} {
		requireErr := s.handleOutboxEvent(context.Background(), SchedulerOutboxEvent{EventType: kind}, nil)
		if requireErr != nil {
			t.Fatal(requireErr)
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
func TestErrorIdentityTurnAndUserViews(t *testing.T) {
	cache := erroridentity.NewCache(func(context.Context) ([]string, error) { return []string{"hidden"}, nil })
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := &OpenAIGatewayService{errorIdentity: cache}
	raw := []byte(`{"type":"error","error":{"message":"hidden codex2api","code":"server_error"}}`)
	for _, model := range []string{"first", "second"} {
		scrub := s.newErrorIdentityTurn(&Account{Platform: PlatformOpenAI}, model)
		got := string(scrub(raw))
		if !strings.Contains(got, model+" upstream") || !strings.Contains(got, "server_error") {
			t.Fatal(got)
		}
	}
	if string(s.newErrorIdentityTurn(&Account{Platform: PlatformGrok}, "x")(raw)) != string(raw) {
		t.Fatal("changed grok")
	}
	e := &OpsErrorLogDetail{OpsErrorLog: OpsErrorLog{Platform: PlatformOpenAI, RequestedModel: "public", Message: "hidden codex2api"}, ErrorBody: string(raw)}
	out := ToUserErrorRequestDetail(e, cache.Snapshot())
	if out.Message != "public upstream" || strings.Contains(out.ErrorBody, "hidden") {
		t.Fatal(out)
	}
	if e.Message != "hidden codex2api" || e.ErrorBody != string(raw) {
		t.Fatal("mutated audit")
	}
	e.StatusCode = 400
	e.ErrorBody = `{"detail":"hidden codex2api","code":"hidden"}`
	out = ToUserErrorRequestDetail(e, cache.Snapshot())
	if out.ErrorBody != `{"code":"hidden","detail":"public upstream"}` {
		t.Fatalf("detail error was not scrubbed: %s", out.ErrorBody)
	}
	if e.ErrorBody != `{"detail":"hidden codex2api","code":"hidden"}` {
		t.Fatal("mutated detail audit")
	}
}
