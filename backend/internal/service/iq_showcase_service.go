package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	iqShowcaseMaxConcurrent      = 4
	iqShowcaseLease              = 7 * time.Minute
	iqShowcaseExecTimeout        = 5*time.Minute + 30*time.Second
	iqShowcaseHTTPTimeout        = 5 * time.Minute
	iqShowcaseFinishTimeout      = 30 * time.Second
	iqShowcaseFailureRetention   = 30 * 24 * time.Hour
	iqShowcaseDueBatch           = 20
	iqShowcaseDefaultInterval    = 60
	iqShowcaseMaxInterval        = 1440
	iqShowcaseDefaultMaxCards    = 12
	iqShowcaseMaxMaxCards        = 100
	iqShowcaseMaxPromptBytes     = 32 << 10
	iqShowcaseMaxOutputTokensCap = 200000
	iqShowcaseMaxSVGBatch        = 24
)

var iqShowcaseReasoningEfforts = map[string]bool{
	"": true, "none": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true,
}

// IQShowcaseService runs the user-facing IQ showcase: admin-configured
// prompts sent through the real gateway, whose SVG answers are shown as cards.
type IQShowcaseService struct {
	repo     IQShowcaseRepository
	settings *SettingService
	baseURL  string
	client   *http.Client
	now      func() time.Time

	sem    chan struct{}
	wg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc
}

func NewIQShowcaseService(repo IQShowcaseRepository, settings *SettingService, cfg *config.Config) *IQShowcaseService {
	port := 8080
	if cfg != nil && cfg.Server.Port > 0 {
		port = cfg.Server.Port
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &IQShowcaseService{
		repo:     repo,
		settings: settings,
		// Same loopback the intent classifier uses (intent_route_service.go):
		// the request enters the gateway like any client request.
		baseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		client: &http.Client{
			// Loopback never goes through a proxy; the context carries the deadline.
			Transport: &http.Transport{Proxy: nil, ForceAttemptHTTP2: false, MaxIdleConnsPerHost: iqShowcaseMaxConcurrent},
		},
		now:    time.Now,
		sem:    make(chan struct{}, iqShowcaseMaxConcurrent),
		ctx:    ctx,
		cancel: cancel,
	}
}

// Stop cancels running rounds and waits for them to record their outcome.
func (s *IQShowcaseService) Stop() {
	if s == nil {
		return
	}
	s.cancel()
	s.wg.Wait()
}

// ---- admin ----

func (s *IQShowcaseService) ListItems(ctx context.Context) ([]*IQShowcaseItem, error) {
	return s.repo.ListItems(ctx)
}

func (s *IQShowcaseService) CreateItem(ctx context.Context, item *IQShowcaseItem) (*IQShowcaseItem, error) {
	if err := s.normalizeItem(ctx, item); err != nil {
		return nil, err
	}
	return s.repo.CreateItem(ctx, item)
}

func (s *IQShowcaseService) UpdateItem(ctx context.Context, item *IQShowcaseItem) (*IQShowcaseItem, error) {
	if _, err := s.repo.GetItem(ctx, item.ID); err != nil {
		return nil, err
	}
	if err := s.normalizeItem(ctx, item); err != nil {
		return nil, err
	}
	return s.repo.UpdateItem(ctx, item)
}

// SetEnabled switches an item on or off without validating the rest of it,
// so an item whose key or group has gone away can still be stopped.
func (s *IQShowcaseService) SetEnabled(ctx context.Context, id int64, enabled bool) (*IQShowcaseItem, error) {
	return s.repo.SetEnabled(ctx, id, enabled)
}

func (s *IQShowcaseService) DeleteItem(ctx context.Context, id int64) error {
	return s.repo.DeleteItem(ctx, id)
}

func (s *IQShowcaseService) ListResults(ctx context.Context, itemID int64, success *bool, limit int) ([]*IQShowcaseResult, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if _, err := s.repo.GetItem(ctx, itemID); err != nil {
		return nil, err
	}
	return s.repo.ListResults(ctx, itemID, success, limit)
}

func (s *IQShowcaseService) ListKeyOptions(ctx context.Context, groupID int64) ([]*IQShowcaseKeyOption, error) {
	if groupID <= 0 {
		return nil, infraerrors.BadRequest("IQ_SHOWCASE_GROUP_REQUIRED", "group_id is required")
	}
	return s.repo.ListKeyOptions(ctx, groupID)
}

func iqShowcaseBadRequest(reason, format string, a ...any) error {
	return infraerrors.BadRequest(reason, fmt.Sprintf(format, a...))
}

func (s *IQShowcaseService) normalizeItem(ctx context.Context, item *IQShowcaseItem) error {
	item.Title = strings.TrimSpace(item.Title)
	if item.Title == "" || utf8.RuneCountInString(item.Title) > 100 {
		return iqShowcaseBadRequest("IQ_SHOWCASE_TITLE_INVALID", "title is required and at most 100 characters")
	}
	group, err := s.repo.GetGroup(ctx, item.GroupID)
	if err != nil {
		return err
	}
	if group == nil {
		return iqShowcaseBadRequest("IQ_SHOWCASE_GROUP_NOT_FOUND", "group %d does not exist", item.GroupID)
	}
	if item.APIKeyID == nil || *item.APIKeyID <= 0 {
		return iqShowcaseBadRequest("IQ_SHOWCASE_KEY_REQUIRED", "an API key of the group is required")
	}
	key, err := s.repo.GetRunKey(ctx, *item.APIKeyID)
	if err != nil {
		return err
	}
	if key == nil || key.GroupID == nil || *key.GroupID != item.GroupID {
		return iqShowcaseBadRequest("IQ_SHOWCASE_KEY_INVALID", "the API key does not exist or does not belong to the group")
	}
	switch item.Protocol {
	case IQShowcaseProtocolOpenAIResponses, IQShowcaseProtocolOpenAIChat,
		IQShowcaseProtocolAnthropicMessages, IQShowcaseProtocolGemini:
	default:
		return iqShowcaseBadRequest("IQ_SHOWCASE_PROTOCOL_INVALID", "unknown protocol %q", item.Protocol)
	}
	item.Model = strings.TrimSpace(item.Model)
	if item.Model == "" || len(item.Model) > 200 {
		return iqShowcaseBadRequest("IQ_SHOWCASE_MODEL_INVALID", "model is required and at most 200 characters")
	}
	if strings.TrimSpace(item.Prompt) == "" || len(item.Prompt) > iqShowcaseMaxPromptBytes {
		return iqShowcaseBadRequest("IQ_SHOWCASE_PROMPT_INVALID", "prompt is required and at most %d bytes", iqShowcaseMaxPromptBytes)
	}
	item.ReasoningEffort = strings.ToLower(strings.TrimSpace(item.ReasoningEffort))
	if !iqShowcaseReasoningEfforts[item.ReasoningEffort] {
		return iqShowcaseBadRequest("IQ_SHOWCASE_REASONING_INVALID", "unknown reasoning effort %q", item.ReasoningEffort)
	}
	if item.MaxOutputTokens < 0 || item.MaxOutputTokens > iqShowcaseMaxOutputTokensCap {
		return iqShowcaseBadRequest("IQ_SHOWCASE_MAX_TOKENS_INVALID", "max_output_tokens must be 0..%d", iqShowcaseMaxOutputTokensCap)
	}
	if item.IntervalMinutes == 0 {
		item.IntervalMinutes = iqShowcaseDefaultInterval
	}
	if item.IntervalMinutes < 1 || item.IntervalMinutes > iqShowcaseMaxInterval {
		return iqShowcaseBadRequest("IQ_SHOWCASE_INTERVAL_INVALID", "interval_minutes must be 1..%d", iqShowcaseMaxInterval)
	}
	if item.MaxCards == 0 {
		item.MaxCards = iqShowcaseDefaultMaxCards
	}
	if item.MaxCards < 1 || item.MaxCards > iqShowcaseMaxMaxCards {
		return iqShowcaseBadRequest("IQ_SHOWCASE_MAX_CARDS_INVALID", "max_cards must be 1..%d", iqShowcaseMaxMaxCards)
	}
	return nil
}

// ---- running ----

func newIQShowcaseLeaseToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (s *IQShowcaseService) tryAcquireSlot() bool {
	select {
	case s.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *IQShowcaseService) releaseSlot() { <-s.sem }

// RunDue starts every due item this instance has a free slot for. A slot is
// taken before the claim, so a claimed item never waits for one.
func (s *IQShowcaseService) RunDue(ctx context.Context) {
	if s.ctx.Err() != nil {
		return
	}
	ids, err := s.repo.ListDueItemIDs(ctx, iqShowcaseDueBatch)
	if err != nil {
		logger.LegacyPrintf("service.iq_showcase", "[IQShowcase] list due items: %v", err)
		return
	}
	for _, id := range ids {
		if !s.tryAcquireSlot() {
			return
		}
		token := newIQShowcaseLeaseToken()
		item, err := s.repo.ClaimItem(ctx, id, token, iqShowcaseLease, true)
		if err != nil || item == nil {
			s.releaseSlot()
			if err != nil {
				logger.LegacyPrintf("service.iq_showcase", "[IQShowcase] claim item=%d: %v", id, err)
			}
			continue
		}
		s.start(item, token)
	}
}

// RunNow starts one round of an item right away, without moving its schedule.
func (s *IQShowcaseService) RunNow(ctx context.Context, id int64) error {
	if _, err := s.repo.GetItem(ctx, id); err != nil {
		return err
	}
	if !s.tryAcquireSlot() {
		return ErrIQShowcaseNoSlot
	}
	token := newIQShowcaseLeaseToken()
	item, err := s.repo.ClaimItem(ctx, id, token, iqShowcaseLease, false)
	if err != nil {
		s.releaseSlot()
		return err
	}
	if item == nil {
		s.releaseSlot()
		return ErrIQShowcaseItemBusy
	}
	s.start(item, token)
	return nil
}

// start runs a claimed round in the background. The caller holds a slot,
// which the round gives back when it ends.
func (s *IQShowcaseService) start(item *IQShowcaseItem, token string) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.releaseSlot()
		s.executeRound(item, token)
	}()
}

// executeRound runs one claimed round. The probe gets its own deadline and the
// bookkeeping a separate one, so a probe that used up its time still records
// its failure; ownership is checked when writing, so a round that outlived its
// lease cannot write a result or release a newer lease.
func (s *IQShowcaseService) executeRound(item *IQShowcaseItem, token string) {
	execCtx, cancel := context.WithTimeout(s.ctx, iqShowcaseExecTimeout)
	result := s.probe(execCtx, item)
	cancel()

	finishCtx, stop := context.WithTimeout(context.Background(), iqShowcaseFinishTimeout)
	defer stop()
	owned, err := s.repo.CompleteRound(finishCtx, item.ID, token, result)
	switch {
	case err != nil:
		logger.LegacyPrintf("service.iq_showcase", "[IQShowcase] item=%d save result: %v (lease expires on its own)", item.ID, err)
	case !owned:
		logger.LegacyPrintf("service.iq_showcase", "[IQShowcase] item=%d lost its lease, result dropped", item.ID)
	}
}

// probe sends the item's prompt through the gateway and turns the answer into
// a result. It never returns an error: every failure is a failed result.
func (s *IQShowcaseService) probe(ctx context.Context, item *IQShowcaseItem) *IQShowcaseResult {
	started := s.now()
	result := &IQShowcaseResult{ItemID: item.ID, ConfigRev: item.ConfigRev, GroupID: item.GroupID, Model: item.Model}
	apiKey := ""
	failf := func(format string, a ...any) *IQShowcaseResult {
		msg := fmt.Sprintf(format, a...)
		if apiKey != "" {
			msg = strings.ReplaceAll(msg, apiKey, "***")
		}
		result.Success = false
		result.Error = truncateIQShowcaseError(msg)
		result.LatencyMs = int(s.now().Sub(started).Milliseconds())
		return result
	}

	group, err := s.repo.GetGroup(ctx, item.GroupID)
	if err != nil {
		return failf("load group: %v", err)
	}
	if group == nil {
		return failf("group %d has been deleted", item.GroupID)
	}
	if item.APIKeyID == nil {
		return failf("the API key has been deleted")
	}
	key, err := s.repo.GetRunKey(ctx, *item.APIKeyID)
	if err != nil {
		return failf("load API key: %v", err)
	}
	switch {
	case key == nil:
		return failf("the API key has been deleted")
	case key.GroupID == nil || *key.GroupID != item.GroupID:
		return failf("the API key no longer belongs to group %d", item.GroupID)
	case key.Status != StatusAPIKeyActive:
		return failf("the API key is %s", key.Status)
	case key.ExpiresAt != nil && !key.ExpiresAt.After(s.now()):
		return failf("the API key has expired")
	}
	apiKey = key.Key

	httpCtx, cancel := context.WithTimeout(ctx, iqShowcaseHTTPTimeout)
	defer cancel()
	req, err := BuildIQShowcaseRequest(httpCtx, s.baseURL, apiKey, item)
	if err != nil {
		return failf("build request: %v", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil || httpCtx.Err() != nil {
			return failf("timeout or cancelled: %v", err)
		}
		return failf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	result.HTTPStatus = resp.StatusCode
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, iqShowcaseMaxErrorBytes))
		return failf("gateway returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	text, err := readIQShowcaseStream(item.Protocol, resp.Body)
	if err != nil {
		if httpCtx.Err() != nil {
			return failf("timeout or cancelled: %v", err)
		}
		return failf("%v", err)
	}
	svg, err := extractIQShowcaseSVG(text)
	if err != nil {
		return failf("%v", err)
	}
	result.Success = true
	result.SVG = svg
	result.LatencyMs = int(s.now().Sub(started).Milliseconds())
	return result
}

// Maintenance prunes old failures and stray cards.
func (s *IQShowcaseService) Maintenance(ctx context.Context) {
	if err := s.repo.Maintenance(ctx, iqShowcaseFailureRetention); err != nil {
		logger.LegacyPrintf("service.iq_showcase", "[IQShowcase] maintenance: %v", err)
	}
}

// ---- user ----

func (s *IQShowcaseService) enabled(ctx context.Context) bool {
	return s.settings.IsIQShowcaseEnabled(ctx)
}

// UserView lists the enabled items by group with their current cards, without
// SVG bodies. Groups and items keep the admin's order.
func (s *IQShowcaseService) UserView(ctx context.Context) (*IQShowcaseUserView, error) {
	if !s.enabled(ctx) {
		return nil, ErrIQShowcaseDisabled
	}
	items, err := s.repo.ListVisibleItems(ctx)
	if err != nil {
		return nil, err
	}
	view := &IQShowcaseUserView{Groups: []*IQShowcaseUserGroup{}}
	var current *IQShowcaseUserGroup
	for _, it := range items {
		cards, err := s.repo.ListCardMeta(ctx, it.ID, it.ConfigRev, it.MaxCards)
		if err != nil {
			return nil, err
		}
		if cards == nil {
			cards = []*IQShowcaseCardMeta{}
		}
		if current == nil || current.GroupID != it.GroupID {
			current = &IQShowcaseUserGroup{GroupID: it.GroupID, GroupName: it.GroupName, Items: []*IQShowcaseUserItem{}}
			view.Groups = append(view.Groups, current)
		}
		current.Items = append(current.Items, &IQShowcaseUserItem{ID: it.ID, Title: it.Title, Cards: cards})
	}
	return view, nil
}

// CardSVGs returns the SVG of visible cards among ids (at most 24).
func (s *IQShowcaseService) CardSVGs(ctx context.Context, ids []int64) ([]*IQShowcaseCardSVG, error) {
	if !s.enabled(ctx) {
		return nil, ErrIQShowcaseDisabled
	}
	if len(ids) == 0 {
		return []*IQShowcaseCardSVG{}, nil
	}
	if len(ids) > iqShowcaseMaxSVGBatch {
		return nil, iqShowcaseBadRequest("IQ_SHOWCASE_TOO_MANY_IDS", "at most %d cards per request", iqShowcaseMaxSVGBatch)
	}
	out, err := s.repo.GetCardSVGs(ctx, ids)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []*IQShowcaseCardSVG{}
	}
	return out, nil
}
