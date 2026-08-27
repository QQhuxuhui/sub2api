package service

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// EmptyResponseBillingRule 是一条用户级「空返回不扣费」规则。
//
// GroupID 为 nil 表示不限分组；Model 为空表示不限模型。两者都留空即该用户全局生效。
type EmptyResponseBillingRule struct {
	ID        int64
	UserID    int64
	GroupID   *int64
	Model     string
	Enabled   bool
	Note      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// UserEmptyResponseBillingRepository 读取用户级空返回免费规则。
type UserEmptyResponseBillingRepository interface {
	// ListEnabledByUserID 返回该用户全部已启用的规则；无规则时返回空切片而非错误。
	ListEnabledByUserID(ctx context.Context, userID int64) ([]EmptyResponseBillingRule, error)
}

// UserEmptyResponseBillingAdminRepository 是后台管理面：在计费读路径之上加全量读写。
type UserEmptyResponseBillingAdminRepository interface {
	UserEmptyResponseBillingRepository
	// ListByUserID 返回该用户全部规则（含已停用），供管理页展示。
	ListByUserID(ctx context.Context, userID int64) ([]EmptyResponseBillingRule, error)
	// ReplaceByUserID 以 rules 全量替换该用户的规则集（事务内先删后插）。
	ReplaceByUserID(ctx context.Context, userID int64, rules []EmptyResponseBillingRule) error
}

// EmptyResponseBillingRuleInvalidator is retained for handler wiring compatibility.
// Rule reads are uncached, so invalidation is intentionally a no-op.
type EmptyResponseBillingRuleInvalidator interface {
	InvalidateEmptyResponseBillingRules(userID int64)
}

// MatchEmptyResponseBillingRule 判断规则集里是否有一条覆盖本次请求。
//
// models 传本次请求相关的全部模型名（请求模型 / 计费模型 / 上游模型）：站长配规则时
// 写的是他在后台看到的那个名字，而同一笔请求这三者可能因渠道映射而不同，只比对其中
// 一个必然出现「配了不生效」的困惑。模型名大小写与首尾空白不敏感。
func MatchEmptyResponseBillingRule(rules []EmptyResponseBillingRule, groupID *int64, models []string) *EmptyResponseBillingRule {
	if len(rules) == 0 {
		return nil
	}
	normalizedModels := make(map[string]struct{}, len(models))
	for _, model := range models {
		if normalized := strings.ToLower(strings.TrimSpace(model)); normalized != "" {
			normalizedModels[normalized] = struct{}{}
		}
	}
	for i := range rules {
		rule := &rules[i]
		if !rule.Enabled {
			continue
		}
		if rule.GroupID != nil {
			if groupID == nil || *groupID != *rule.GroupID {
				continue
			}
		}
		if ruleModel := strings.ToLower(strings.TrimSpace(rule.Model)); ruleModel != "" {
			if _, ok := normalizedModels[ruleModel]; !ok {
				continue
			}
		}
		return rule
	}
	return nil
}

type userEmptyResponseBillingResolver struct {
	repo         UserEmptyResponseBillingRepository
	logComponent string
}

func newUserEmptyResponseBillingResolver(
	repo UserEmptyResponseBillingRepository,
	logComponent string,
) *userEmptyResponseBillingResolver {
	if logComponent == "" {
		logComponent = "service.gateway"
	}
	return &userEmptyResponseBillingResolver{
		repo:         repo,
		logComponent: logComponent,
	}
}

// Resolve returns the user's current enabled rules directly from PostgreSQL.
// It is called only after a response is conclusively empty, so avoiding a local
// cache gives multi-instance deployments immediate, race-free rule updates.
func (r *userEmptyResponseBillingResolver) Resolve(ctx context.Context, userID int64) []EmptyResponseBillingRule {
	if r == nil || userID <= 0 || r.repo == nil {
		return nil
	}
	rules, err := r.repo.ListEnabledByUserID(ctx, userID)
	if err != nil {
		// 读不到规则时收费，是这里唯一安全的失败方向：错误地免费会直接漏收入，
		// 而错误地收费用户会来反馈、可人工退。
		logger.LegacyPrintf(r.logComponent,
			"list user empty-response billing rules failed, billing as usual: user=%d err=%v", userID, err)
		return nil
	}
	if rules == nil {
		return []EmptyResponseBillingRule{}
	}
	return rules
}

// Invalidate is a compatibility no-op because Resolve does not cache rules.
func (r *userEmptyResponseBillingResolver) Invalidate(userID int64) {
	if r == nil || userID <= 0 {
		return
	}
}

// waiveCostForEmptyResponse 把一份费用清成 $0，保留 BillingMode 以免用量行丢失
// 「本来按什么模式计费」的信息。cost 为 nil 时返回一份零值明细，让调用方后续流程
// （建用量行、扣费）拿到的始终是一个非 nil 的确定对象。
func waiveCostForEmptyResponse(cost *CostBreakdown) *CostBreakdown {
	if cost == nil {
		return &CostBreakdown{}
	}
	waived := *cost
	waived.InputCost = 0
	waived.ImageInputCost = 0
	waived.OutputCost = 0
	waived.ImageOutputCost = 0
	waived.CacheCreationCost = 0
	waived.CacheReadCost = 0
	waived.TotalCost = 0
	waived.ActualCost = 0
	return &waived
}

// emptyResponseBillingWaiver 汇总一次免单判定的结果，供调用方打日志。
type emptyResponseBillingWaiver struct {
	Applied bool
	RuleID  int64
	// WaivedCost 是本来要收的钱，只用于日志与后续对账，不参与扣费。
	WaivedCost float64
}

// resolveEmptyResponseBillingWaiver 判定是否对本次请求免单。
//
// 三步与短路顺序都是有意的：先看是不是空返回（纯内存判断，绝大多数请求在此返回），
// 再查该用户的规则（带缓存），最后做规则匹配。反过来会让每一笔正常请求都白查一次规则。
func resolveEmptyResponseBillingWaiver(
	rules []EmptyResponseBillingRule,
	isEmptyImageResponse bool,
	groupID *int64,
	models []string,
	cost *CostBreakdown,
) emptyResponseBillingWaiver {
	if !isEmptyImageResponse || len(rules) == 0 {
		return emptyResponseBillingWaiver{}
	}
	rule := MatchEmptyResponseBillingRule(rules, groupID, models)
	if rule == nil {
		return emptyResponseBillingWaiver{}
	}
	waived := emptyResponseBillingWaiver{Applied: true, RuleID: rule.ID}
	if cost != nil {
		waived.WaivedCost = cost.ActualCost
	}
	return waived
}
