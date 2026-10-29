package domain

import "time"

// IntentRule sends requests classified as one intent to specific accounts.
// The accounts may belong to any group: intent routing is bound to the group a
// request arrives on, but its targets are plain account IDs.
type IntentRule struct {
	// Name is the label the classifier model answers with. Unique per router.
	Name string `json:"name"`
	// Description tells the classifier model when this intent applies. A rule
	// without one is matched by its keywords only.
	Description string `json:"description"`
	// Keywords route a request to this rule without asking the classifier:
	// any one of them appearing in the user's latest message is a match
	// (case-insensitive substring). Checked on every turn, before the model.
	Keywords   []string `json:"keywords,omitempty"`
	AccountIDs []int64  `json:"account_ids"`
	Enabled    bool     `json:"enabled"`
}

const (
	// IntentClassifierProtocolOpenAIChat posts to {base}/v1/chat/completions.
	IntentClassifierProtocolOpenAIChat = "openai_chat"
	// IntentClassifierProtocolGemini posts to {base}/v1beta/models/{model}:generateContent.
	IntentClassifierProtocolGemini = "gemini"
)

const (
	// IntentJevScopeAll asks Jev about every eligible request of the group.
	IntentJevScopeAll = "all"
	// IntentJevScopeListed asks only for the listed users and API keys.
	IntentJevScopeListed = "listed"
)

// IntentJevAPIKey is an API key Jev is narrowed to. UserID is filled in by
// the server from the key itself.
type IntentJevAPIKey struct {
	ID     int64 `json:"id"`
	UserID int64 `json:"user_id"`
}

// IntentJevConfig sends keyword misses to TypeSafe Jev, which judges whether a
// request is a test of the model itself (benchmark puzzle, fingerprinting
// question, drawing benchmark, connectivity check) rather than real work. A
// request judged a probe is routed like a keyword hit of Rule. The API key is
// stored separately and never part of this document.
type IntentJevConfig struct {
	Enabled bool `json:"enabled"`
	// Rule names the rule whose accounts serve requests judged to be probes.
	Rule string `json:"rule"`
	// Threshold is the probe probability (0-1) at or above which a request
	// counts as a probe.
	Threshold float64 `json:"threshold"`
	// MaxTurns limits the check to short conversations: probes are one-shot,
	// coding sessions carry dozens of items and are never sent.
	MaxTurns  int    `json:"max_turns"`
	TimeoutMS int    `json:"timeout_ms"`
	Model     string `json:"model"`
	// Scope is IntentJevScopeAll or IntentJevScopeListed. A listed scope
	// covers the listed users — all their API keys, or only the ones in
	// APIKeys when any of theirs are listed there.
	Scope   string            `json:"scope"`
	UserIDs []int64           `json:"user_ids"`
	APIKeys []IntentJevAPIKey `json:"api_keys"`
	// SamplePercent (1-100) of the eligible requests are sent to Jev; the rest
	// are scheduled as a keyword miss without asking, to cap the cost.
	SamplePercent int `json:"sample_percent"`

	// HealthEnabled asks Jev a second question on the same call: is this a
	// liveness/health check (sub2api's channel monitor, "hi", "reply OK", a
	// trivial sum) rather than a test of the model's intelligence? Requests
	// at or above HealthThreshold go to HealthRule, or — when it is empty —
	// to the group's ordinary scheduling, instead of the probe rule.
	HealthEnabled   bool    `json:"health_enabled"`
	HealthThreshold float64 `json:"health_threshold"`
	HealthRule      string  `json:"health_rule"`

	// BalanceUSD is the account balance the admin read off the TypeSafe
	// console at BalanceSetAt (Jev has no balance API); spending recorded
	// since then is subtracted to estimate what is left. Set by the server.
	BalanceUSD   float64    `json:"balance_usd"`
	BalanceSetAt *time.Time `json:"balance_set_at,omitempty"`
	// LowBalanceUSD flags the estimate on the admin page; it never switches
	// Jev off — a real 402 does. nil (never set) means the default; 0 is a
	// real setting that turns the alert off.
	LowBalanceUSD *float64 `json:"low_balance_usd"`

	// Set by the server when Jev answered with an account-level error (key,
	// payment, region) and was switched off; cleared when an admin enables
	// Jev again.
	AutoDisabledStatus int        `json:"auto_disabled_status,omitempty"`
	AutoDisabledDetail string     `json:"auto_disabled_detail,omitempty"`
	AutoDisabledAt     *time.Time `json:"auto_disabled_at,omitempty"`

	// Revision is a random token the server draws on every save and every
	// switch-off. A switch-off applies only to the revision the failing call
	// was made under; being random, a revision is never reused — not by a
	// router deleted and created again, not by two saves racing each other.
	Revision string `json:"revision"`
}
