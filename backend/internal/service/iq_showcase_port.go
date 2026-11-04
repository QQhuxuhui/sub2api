package service

import (
	"context"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// IQ showcase protocols: which gateway endpoint a probe calls.
const (
	IQShowcaseProtocolOpenAIResponses   = "openai_responses"
	IQShowcaseProtocolOpenAIChat        = "openai_chat"
	IQShowcaseProtocolAnthropicMessages = "anthropic_messages"
	IQShowcaseProtocolGemini            = "gemini"
)

var (
	ErrIQShowcaseDisabled     = infraerrors.NotFound("IQ_SHOWCASE_DISABLED", "iq showcase is not enabled")
	ErrIQShowcaseItemNotFound = infraerrors.NotFound("IQ_SHOWCASE_ITEM_NOT_FOUND", "showcase item not found")
	ErrIQShowcaseItemBusy     = infraerrors.Conflict("IQ_SHOWCASE_ITEM_BUSY", "showcase item is already running")
	ErrIQShowcaseNoSlot       = infraerrors.TooManyRequests("IQ_SHOWCASE_NO_SLOT", "too many showcase probes are running, try again later")
)

// IQShowcaseItem is one admin-configured probe: a prompt sent with an API key
// of the group, through the real gateway, on a schedule.
type IQShowcaseItem struct {
	ID              int64      `json:"id"`
	Title           string     `json:"title"`
	GroupID         int64      `json:"group_id"`
	APIKeyID        *int64     `json:"api_key_id"`
	Protocol        string     `json:"protocol"`
	Model           string     `json:"model"`
	Prompt          string     `json:"prompt"`
	ReasoningEffort string     `json:"reasoning_effort"`
	MaxOutputTokens int        `json:"max_output_tokens"`
	IntervalMinutes int        `json:"interval_minutes"`
	MaxCards        int        `json:"max_cards"`
	Enabled         bool       `json:"enabled"`
	SortOrder       int        `json:"sort_order"`
	NextRunAt       time.Time  `json:"next_run_at"`
	LeaseUntil      *time.Time `json:"lease_until"`
	ConfigRev       int        `json:"config_rev"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`

	// Joined for the admin list. The key itself never leaves the server.
	GroupName      string     `json:"group_name"`
	GroupPlatform  string     `json:"group_platform"`
	APIKeyName     string     `json:"api_key_name"`
	APIKeyMasked   string     `json:"api_key_masked"`
	LastResultAt   *time.Time `json:"last_result_at"`
	LastSuccess    *bool      `json:"last_success"`
	SuccessCards   int        `json:"success_cards"`
	RecentFailures int        `json:"recent_failures"`
}

// IQShowcaseResult is one round. Failed rounds are admin-only.
type IQShowcaseResult struct {
	ID         int64     `json:"id"`
	ItemID     int64     `json:"item_id"`
	ConfigRev  int       `json:"config_rev"`
	GroupID    int64     `json:"group_id"`
	Model      string    `json:"model"`
	Success    bool      `json:"success"`
	SVG        string    `json:"svg,omitempty"`
	Error      string    `json:"error,omitempty"`
	HTTPStatus int       `json:"http_status"`
	LatencyMs  int       `json:"latency_ms"`
	CreatedAt  time.Time `json:"created_at"`
}

// IQShowcaseKeyOption is what the admin picker sees of an API key.
type IQShowcaseKeyOption struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Masked    string `json:"masked"`
	Status    string `json:"status"`
	UserEmail string `json:"user_email"`
}

// IQShowcaseRunKey is the key as the runner needs it. Key is plaintext and
// must only ever go into the outgoing request header.
type IQShowcaseRunKey struct {
	Key       string
	GroupID   *int64
	Status    string
	ExpiresAt *time.Time
}

// IQShowcaseGroupInfo is the group as the admin form and runner need it.
type IQShowcaseGroupInfo struct {
	ID       int64
	Name     string
	Platform string
}

// IQShowcaseCardMeta is a user-visible card without its SVG.
type IQShowcaseCardMeta struct {
	ID        int64     `json:"id"`
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"created_at"`
}

type IQShowcaseUserItem struct {
	ID    int64                 `json:"id"`
	Title string                `json:"title"`
	Cards []*IQShowcaseCardMeta `json:"cards"`
}

type IQShowcaseUserGroup struct {
	GroupID   int64                 `json:"group_id"`
	GroupName string                `json:"group_name"`
	Items     []*IQShowcaseUserItem `json:"items"`
}

// IQShowcaseUserView is the whole user page, without SVG bodies.
type IQShowcaseUserView struct {
	Groups []*IQShowcaseUserGroup `json:"groups"`
}

type IQShowcaseCardSVG struct {
	ID  int64  `json:"id"`
	SVG string `json:"svg"`
}

// IQShowcaseVisibleItem is one row of the user view query: an enabled item of
// a live group.
type IQShowcaseVisibleItem struct {
	ID        int64
	Title     string
	GroupID   int64
	GroupName string
	ConfigRev int
	MaxCards  int
}

type IQShowcaseRepository interface {
	ListItems(ctx context.Context) ([]*IQShowcaseItem, error)
	GetItem(ctx context.Context, id int64) (*IQShowcaseItem, error)
	CreateItem(ctx context.Context, item *IQShowcaseItem) (*IQShowcaseItem, error)
	// UpdateItem bumps config_rev when bumpRev is true and prunes success
	// cards that no longer belong to the current rev or exceed max_cards.
	UpdateItem(ctx context.Context, item *IQShowcaseItem, bumpRev bool) (*IQShowcaseItem, error)
	DeleteItem(ctx context.Context, id int64) error

	GetGroup(ctx context.Context, id int64) (*IQShowcaseGroupInfo, error)
	GetRunKey(ctx context.Context, id int64) (*IQShowcaseRunKey, error)
	ListKeyOptions(ctx context.Context, groupID int64) ([]*IQShowcaseKeyOption, error)

	ListDueItemIDs(ctx context.Context, limit int) ([]int64, error)
	// ClaimItem takes the lease of an item. due=true requires next_run_at to
	// have passed and moves it forward; due=false (manual run) leaves the
	// schedule alone. Returns nil when someone else holds it or it is not due.
	ClaimItem(ctx context.Context, id int64, token string, lease time.Duration, due bool) (*IQShowcaseItem, error)
	// CompleteRound stores the result, prunes cards to the item's current rev
	// and max_cards, and releases the lease, but only while token still owns
	// an unexpired lease. Returns false when ownership was lost.
	CompleteRound(ctx context.Context, itemID int64, token string, result *IQShowcaseResult) (bool, error)

	ListResults(ctx context.Context, itemID int64, success *bool, limit int) ([]*IQShowcaseResult, error)
	ListVisibleItems(ctx context.Context) ([]*IQShowcaseVisibleItem, error)
	ListCardMeta(ctx context.Context, itemID int64, rev int, limit int) ([]*IQShowcaseCardMeta, error)
	GetCardSVGs(ctx context.Context, ids []int64) ([]*IQShowcaseCardSVG, error)

	// Maintenance prunes failures older than failRetention and success cards
	// outside the current rev or max_cards.
	Maintenance(ctx context.Context, failRetention time.Duration) error
}
