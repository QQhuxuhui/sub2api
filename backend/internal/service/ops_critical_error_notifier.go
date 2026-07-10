package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	opsNotifyCooldownKeyPrefix    = "ops:notify:cooldown:"
	opsCriticalErrorProcessTimeout = 20 * time.Second
)

// OpsCriticalErrorNotifier 在错误日志落库后对每条记录做关键条件匹配,
// 命中则秒级推送到通知通道。冷却键为「账号+状态码」,优先 Redis(跨实例),无 Redis 降级进程内。
type OpsCriticalErrorNotifier struct {
	opsService  *OpsService
	dispatcher  *OpsNotifyDispatcher
	redisClient *redis.Client
	accountRepo AccountRepository

	mu            sync.Mutex
	localCooldown map[string]time.Time
}

func NewOpsCriticalErrorNotifier(
	opsService *OpsService,
	dispatcher *OpsNotifyDispatcher,
	redisClient *redis.Client,
	accountRepo AccountRepository,
) *OpsCriticalErrorNotifier {
	return &OpsCriticalErrorNotifier{
		opsService:    opsService,
		dispatcher:    dispatcher,
		redisClient:   redisClient,
		accountRepo:   accountRepo,
		localCooldown: map[string]time.Time{},
	}
}

// Observe 由错误落库路径在插入成功后调用;异步处理,绝不阻塞调用方。
func (n *OpsCriticalErrorNotifier) Observe(entries []*OpsInsertErrorLogInput) {
	if n == nil || n.dispatcher == nil || len(entries) == 0 {
		return
	}
	go n.process(entries)
}

func (n *OpsCriticalErrorNotifier) process(entries []*OpsInsertErrorLogInput) {
	ctx, cancel := context.WithTimeout(context.Background(), opsCriticalErrorProcessTimeout)
	defer cancel()

	cfg := n.dispatcher.getSettings(ctx)
	if cfg == nil || !cfg.CriticalError.Enabled || len(cfg.Channels) == 0 {
		return
	}
	cooldown := time.Duration(cfg.CriticalError.CooldownMinutes) * time.Minute

	for _, e := range entries {
		code, ok := matchOpsCriticalError(&cfg.CriticalError, e)
		if !ok {
			continue
		}
		if !n.allowCooldown(ctx, opsCriticalErrorCooldownKey(e, code), cooldown) {
			continue
		}
		// 同步分发:process 本身已在独立 goroutine 中。
		n.dispatcher.dispatch(n.buildMessage(ctx, e, code))
	}
}

// matchOpsCriticalError 判定一条错误是否命中即时通知条件,返回生效状态码。
// 仅上游账号问题(owner=provider)且非业务限制、状态码在名单内才命中。
func matchOpsCriticalError(cfg *OpsCriticalErrorNotifyConfig, e *OpsInsertErrorLogInput) (int, bool) {
	if cfg == nil || e == nil {
		return 0, false
	}
	if strings.ToLower(strings.TrimSpace(e.ErrorOwner)) != "provider" {
		return 0, false
	}
	if e.IsBusinessLimited {
		return 0, false
	}
	code := e.StatusCode
	if e.UpstreamStatusCode != nil && *e.UpstreamStatusCode > 0 {
		code = *e.UpstreamStatusCode
	}
	for _, want := range cfg.StatusCodes {
		if code == want {
			return code, true
		}
	}
	return 0, false
}

func opsCriticalErrorCooldownKey(e *OpsInsertErrorLogInput, code int) string {
	if e.AccountID != nil && *e.AccountID > 0 {
		return "account:" + strconv.FormatInt(*e.AccountID, 10) + ":" + strconv.Itoa(code)
	}
	return "platform:" + strings.TrimSpace(e.Platform) + ":" + strconv.Itoa(code)
}

// allowCooldown 返回 true 表示本次允许发送并占用冷却窗口。
// Redis 出错时降级进程内(不 fail-closed:漏报比偶尔重复更糟)。
func (n *OpsCriticalErrorNotifier) allowCooldown(ctx context.Context, key string, ttl time.Duration) bool {
	if ttl <= 0 {
		return true
	}
	if n.redisClient != nil && ctx != nil {
		ok, err := n.redisClient.SetNX(ctx, opsNotifyCooldownKeyPrefix+key, 1, ttl).Result()
		if err == nil {
			return ok
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	now := time.Now()
	for k, until := range n.localCooldown {
		if now.After(until) {
			delete(n.localCooldown, k)
		}
	}
	if until, ok := n.localCooldown[key]; ok && now.Before(until) {
		return false
	}
	n.localCooldown[key] = now.Add(ttl)
	return true
}

func opsNotifyValueOrDash(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "-"
	}
	return v
}

func (n *OpsCriticalErrorNotifier) buildMessage(ctx context.Context, e *OpsInsertErrorLogInput, code int) *OpsNotifyMessage {
	accountLabel := "-"
	if e.AccountID != nil && *e.AccountID > 0 {
		accountLabel = "#" + strconv.FormatInt(*e.AccountID, 10)
		if n.accountRepo != nil && ctx != nil {
			lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			if acc, err := n.accountRepo.GetByID(lookupCtx, *e.AccountID); err == nil && acc != nil && strings.TrimSpace(acc.Name) != "" {
				accountLabel = fmt.Sprintf("%s (#%d)", strings.TrimSpace(acc.Name), *e.AccountID)
			}
			cancel()
		}
	}
	message := strings.TrimSpace(e.ErrorMessage)
	if e.UpstreamErrorMessage != nil && strings.TrimSpace(*e.UpstreamErrorMessage) != "" {
		message = strings.TrimSpace(*e.UpstreamErrorMessage)
	}
	message = truncateString(message, 200)

	occurred := e.CreatedAt
	if occurred.IsZero() {
		occurred = time.Now()
	}
	return &OpsNotifyMessage{
		Kind:     OpsNotifyKindCriticalError,
		Severity: "P0",
		Title:    fmt.Sprintf("上游账号严重错误: %s %d", opsNotifyValueOrDash(e.Platform), code),
		Fields: []OpsNotifyField{
			{Label: "平台", Value: opsNotifyValueOrDash(e.Platform)},
			{Label: "账号", Value: accountLabel},
			{Label: "状态码", Value: strconv.Itoa(code)},
			{Label: "错误类型", Value: opsNotifyValueOrDash(e.ErrorType)},
			{Label: "错误摘要", Value: opsNotifyValueOrDash(message)},
			{Label: "模型", Value: opsNotifyValueOrDash(e.Model)},
			{Label: "路径", Value: opsNotifyValueOrDash(e.RequestPath)},
		},
		OccurredAt: occurred,
	}
}
