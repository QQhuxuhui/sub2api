package service

import (
	"context"
	"sync"
)

// IntentRouteDecision is the outcome of classifying one request. It is made
// once, before the account failover loop, and travels in the request context
// so every selection attempt of the request sees the same targets.
//
// It is a preference, never a restriction: when none of the target accounts
// can serve the request, selection continues exactly as if intent routing did
// not exist.
type IntentRouteDecision struct {
	GroupID int64
	Intent  string
	// AccountIDs are the rule's targets. They may belong to any group.
	AccountIDs []int64
	// PinnedAccountID is the account this conversation was first served by.
	PinnedAccountID int64

	mu sync.Mutex
	// selected is the routed account of the latest attempt; announced is the
	// account onSelect was last told about. They differ on purpose: every
	// attempt resets selected, but a retry landing on the same account must
	// not re-announce the pin.
	selected  int64
	announced int64
	onSelect  func(accountID int64)
}

// orderedAccountIDs lists the pinned account first so a conversation stays on
// the account it started on, then the rule's remaining targets.
func (d *IntentRouteDecision) orderedAccountIDs() []int64 {
	if d == nil {
		return nil
	}
	ordered := make([]int64, 0, len(d.AccountIDs)+1)
	seen := make(map[int64]struct{}, len(d.AccountIDs)+1)
	add := func(id int64) {
		if id <= 0 {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		ordered = append(ordered, id)
	}
	// A pin only holds while the account is still a target of the rule:
	// removing an account from a rule must take effect for running sessions.
	for _, id := range d.AccountIDs {
		if id == d.PinnedAccountID {
			add(id)
		}
	}
	for _, id := range d.AccountIDs {
		add(id)
	}
	return ordered
}

// markSelected records the account that was picked and pins the conversation
// to it. Safe to call from every attempt; only a change is reported.
func (d *IntentRouteDecision) markSelected(accountID int64) {
	if d == nil || accountID <= 0 {
		return
	}
	d.mu.Lock()
	d.selected = accountID
	changed := d.announced != accountID && d.PinnedAccountID != accountID
	d.announced = accountID
	notify := d.onSelect
	d.mu.Unlock()
	if changed && notify != nil {
		notify(accountID)
	}
}

// SelectedAccountID reports the routed account of the latest attempt (0 when
// the request fell back to ordinary scheduling).
func (d *IntentRouteDecision) SelectedAccountID() int64 {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.selected
}

// clearSelected notes that an attempt fell back to ordinary scheduling.
func (d *IntentRouteDecision) clearSelected() {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.selected = 0
	d.mu.Unlock()
}

type intentRouteDecisionContextKey struct{}

// WithIntentRouteDecision attaches a decision to the request context.
func WithIntentRouteDecision(ctx context.Context, decision *IntentRouteDecision) context.Context {
	if decision == nil || len(decision.AccountIDs) == 0 {
		return ctx
	}
	return context.WithValue(ctx, intentRouteDecisionContextKey{}, decision)
}

// IntentRouteDecisionFromContext returns the request's decision, or nil.
func IntentRouteDecisionFromContext(ctx context.Context) *IntentRouteDecision {
	if ctx == nil {
		return nil
	}
	decision, _ := ctx.Value(intentRouteDecisionContextKey{}).(*IntentRouteDecision)
	return decision
}
