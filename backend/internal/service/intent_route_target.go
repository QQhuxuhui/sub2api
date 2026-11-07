package service

import (
	"context"
	"fmt"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/apikey"
)

// Problems that make an API-key rule target unusable. A rule whose key has a
// problem still matches; its requests are refused with the router's
// unavailable status rather than scheduled anywhere else.
const (
	intentKeyProblemNotFound      = "key_not_found"
	intentKeyProblemDisabled      = "key_disabled"
	intentKeyProblemExpired       = "key_expired"
	intentKeyProblemNoGroup       = "key_no_group"
	intentKeyProblemGroupNotFound = "group_not_found"
	intentKeyProblemGroupInactive = "group_inactive"
)

// intentRuleTarget is what an API-key rule resolves to: the key's group, or
// the reason it cannot be used.
type intentRuleTarget struct {
	GroupID  int64
	Platform string
	Problem  string
}

func (t intentRuleTarget) usable() bool { return t.Problem == "" && t.GroupID > 0 }

// intentKeyTargetFromEnt judges one loaded API key (nil = not found). Only the
// key's role as a pointer matters: quota_exhausted keys still point at their
// group, because the key itself is never billed.
func intentKeyTargetFromEnt(row *dbent.APIKey, now time.Time) intentRuleTarget {
	switch {
	case row == nil:
		return intentRuleTarget{Problem: intentKeyProblemNotFound}
	case row.Status == StatusAPIKeyDisabled:
		return intentRuleTarget{Problem: intentKeyProblemDisabled}
	case row.Status == StatusAPIKeyExpired || (row.ExpiresAt != nil && !row.ExpiresAt.After(now)):
		return intentRuleTarget{Problem: intentKeyProblemExpired}
	case row.GroupID == nil || *row.GroupID <= 0:
		return intentRuleTarget{Problem: intentKeyProblemNoGroup}
	}
	group := row.Edges.Group
	if group == nil {
		return intentRuleTarget{Problem: intentKeyProblemGroupNotFound}
	}
	if group.Status != StatusActive {
		return intentRuleTarget{GroupID: group.ID, Platform: group.Platform, Problem: intentKeyProblemGroupInactive}
	}
	return intentRuleTarget{GroupID: group.ID, Platform: group.Platform}
}

// loadIntentKeyTargets resolves API keys to their rule targets with one query.
// Every requested ID is present in the result (missing keys as not found).
func loadIntentKeyTargets(ctx context.Context, client *dbent.Client, ids []int64, now time.Time) (map[int64]intentRuleTarget, []*dbent.APIKey, error) {
	targets := make(map[int64]intentRuleTarget, len(ids))
	if len(ids) == 0 {
		return targets, nil, nil
	}
	rows, err := client.APIKey.Query().Where(apikey.IDIn(ids...)).WithGroup().WithUser().All(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("load intent rule api keys: %w", err)
	}
	byID := make(map[int64]*dbent.APIKey, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	for _, id := range ids {
		targets[id] = intentKeyTargetFromEnt(byID[id], now)
	}
	return targets, rows, nil
}

// ---- nested scheduling ----

type intentRouteNestedContextKey struct{}

// intentRouteNested marks a selection made on behalf of an API-key rule: the
// request is scheduled in the key's group, but stays the original group's
// request for billing purposes.
type intentRouteNested struct {
	billingGroupID int64
}

// withIntentRouteNested marks ctx for scheduling in a rule's target group.
// Intent routing steps aside inside (no recursion), channel pricing keeps
// checking the original group, and Claude Code restriction must not move the
// request to a fallback group.
func withIntentRouteNested(ctx context.Context, billingGroupID int64) context.Context {
	return context.WithValue(ctx, intentRouteNestedContextKey{}, intentRouteNested{billingGroupID: billingGroupID})
}

func intentRouteNestedFromContext(ctx context.Context) (intentRouteNested, bool) {
	if ctx == nil {
		return intentRouteNested{}, false
	}
	nested, ok := ctx.Value(intentRouteNestedContextKey{}).(intentRouteNested)
	return nested, ok
}

// intentRouteChannelGroup is the group whose channel pricing restricts the
// request: the original (billing) group inside a nested selection, groupID
// otherwise.
func intentRouteChannelGroup(ctx context.Context, groupID *int64) *int64 {
	if nested, ok := intentRouteNestedFromContext(ctx); ok && nested.billingGroupID > 0 {
		id := nested.billingGroupID
		return &id
	}
	return groupID
}

// intentRouteChannelGroupID is intentRouteChannelGroup for a plain ID.
func intentRouteChannelGroupID(ctx context.Context, groupID int64) int64 {
	if nested, ok := intentRouteNestedFromContext(ctx); ok && nested.billingGroupID > 0 {
		return nested.billingGroupID
	}
	return groupID
}

// IntentRouteStickyGroup reports the group a sticky session for accountID
// must be bound in when an API-key rule selected that account for this
// attempt. viaGroup=false means the caller's ordinary handling applies.
func IntentRouteStickyGroup(ctx context.Context, accountID int64) (groupID int64, viaGroup bool) {
	d := IntentRouteDecisionFromContext(ctx)
	if d == nil || accountID <= 0 {
		return 0, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.selectedViaGroup && d.selected == accountID && d.TargetGroupID > 0 {
		return d.TargetGroupID, true
	}
	return 0, false
}

// IntentRouteStickyGroupOr returns the target group of an API-key rule that
// selected accountID, or fallback.
func IntentRouteStickyGroupOr(ctx context.Context, accountID int64, fallback *int64) *int64 {
	if groupID, ok := IntentRouteStickyGroup(ctx, accountID); ok {
		return &groupID
	}
	return fallback
}
