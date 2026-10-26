package service

import (
	"context"
	"time"
)

// Intelligence probe judge types.
const (
	IQProbeJudgeExact   = "exact"
	IQProbeJudgeChoice  = "choice"
	IQProbeJudgeNumeric = "numeric"
)

// Per-question verdicts.
const (
	IQProbeVerdictCorrect     = "correct"
	IQProbeVerdictWrong       = "wrong"
	IQProbeVerdictFormatError = "format_error"
	IQProbeVerdictError       = "error"
	IQProbeVerdictUnsupported = "unsupported"
)

// Run statuses.
const (
	IQProbeRunRunning = "running"
	IQProbeRunScored  = "scored"
	IQProbeRunInvalid = "invalid"
	IQProbeRunSkipped = "skipped"
)

// Monitor alert states (what the last alert e-mail reported).
const (
	IQProbeAlertOK      = "ok"
	IQProbeAlertLow     = "low"
	IQProbeAlertInvalid = "invalid"
)

type IQProbeQuestionSet struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	QuestionCount int       `json:"question_count"`
	EnabledCount  int       `json:"enabled_count"`
	MonitorCount  int       `json:"monitor_count"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type IQProbeQuestion struct {
	ID               int64     `json:"id"`
	SetID            int64     `json:"set_id"`
	Title            string    `json:"title"`
	Prompt           string    `json:"prompt"`
	SystemPrompt     string    `json:"system_prompt"`
	ExpectedAnswer   string    `json:"expected_answer"`
	JudgeType        string    `json:"judge_type"`
	NumericTolerance float64   `json:"numeric_tolerance"`
	Weight           float64   `json:"weight"`
	Enabled          bool      `json:"enabled"`
	Note             string    `json:"note"`
	SortOrder        int       `json:"sort_order"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type IQProbeMonitor struct {
	ID                 int64      `json:"id"`
	AccountID          int64      `json:"account_id"`
	GroupID            *int64     `json:"group_id"`
	Model              string     `json:"model"`
	SetID              int64      `json:"set_id"`
	IntervalMinutes    int        `json:"interval_minutes"`
	ReasoningEffort    string     `json:"reasoning_effort"`
	MaxTokens          int        `json:"max_tokens"`
	Enabled            bool       `json:"enabled"`
	AlertEnabled       bool       `json:"alert_enabled"`
	AlertThreshold     float64    `json:"alert_threshold"`
	AlertLowRounds     int        `json:"alert_low_rounds"`
	AlertInvalidRounds int        `json:"alert_invalid_rounds"`
	AlertState         string     `json:"alert_state"`
	ConsecutiveLow     int        `json:"consecutive_low"`
	ConsecutiveInvalid int        `json:"consecutive_invalid"`
	LastRunAt          *time.Time `json:"last_run_at"`
	NextRunAt          *time.Time `json:"next_run_at"`
	LastStatus         string     `json:"last_status"`
	LastScore          *float64   `json:"last_score"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`

	// Joined for display.
	AccountName     string `json:"account_name"`
	AccountPlatform string `json:"account_platform"`
	AccountType     string `json:"account_type"`
	AccountStatus   string `json:"account_status"`
	GroupName       string `json:"group_name"`
	SetName         string `json:"set_name"`
}

type IQProbeRun struct {
	ID               int64      `json:"id"`
	MonitorID        int64      `json:"monitor_id"`
	AccountID        int64      `json:"account_id"`
	Model            string     `json:"model"`
	SetID            int64      `json:"set_id"`
	Trigger          string     `json:"trigger"`
	Status           string     `json:"status"`
	Score            *float64   `json:"score"`
	TotalCount       int        `json:"total_count"`
	CorrectCount     int        `json:"correct_count"`
	WrongCount       int        `json:"wrong_count"`
	FormatErrorCount int        `json:"format_error_count"`
	ErrorCount       int        `json:"error_count"`
	EarnedWeight     float64    `json:"earned_weight"`
	TotalWeight      float64    `json:"total_weight"`
	InputTokens      int64      `json:"input_tokens"`
	OutputTokens     int64      `json:"output_tokens"`
	CostUSD          float64    `json:"cost_usd"`
	Message          string     `json:"message"`
	StartedAt        time.Time  `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at"`

	AccountName string `json:"account_name"`
	SetName     string `json:"set_name"`
}

type IQProbeAnswer struct {
	ID              int64     `json:"id"`
	RunID           int64     `json:"run_id"`
	QuestionID      int64     `json:"question_id"`
	QuestionTitle   string    `json:"question_title"`
	ExpectedAnswer  string    `json:"expected_answer"`
	JudgeType       string    `json:"judge_type"`
	Weight          float64   `json:"weight"`
	Verdict         string    `json:"verdict"`
	ExtractedAnswer string    `json:"extracted_answer"`
	ResponseText    string    `json:"response_text,omitempty"`
	ErrorMessage    string    `json:"error_message"`
	UpstreamModel   string    `json:"upstream_model"`
	ResponseModel   string    `json:"response_model"`
	InputTokens     int64     `json:"input_tokens"`
	OutputTokens    int64     `json:"output_tokens"`
	ReasoningTokens int64     `json:"reasoning_tokens"`
	LatencyMs       int64     `json:"latency_ms"`
	FirstTokenMs    int64     `json:"first_token_ms"`
	CostUSD         float64   `json:"cost_usd"`
	CreatedAt       time.Time `json:"created_at"`
}

// IQProbeScorePoint is one scored round for the list sparkline.
type IQProbeScorePoint struct {
	At     time.Time `json:"at"`
	Score  *float64  `json:"score"`
	Status string    `json:"status"`
}

type IQProbeRunFilter struct {
	MonitorID int64
	Status    string
	Page      int
	PageSize  int
}

// IQProbeMonitorRunState is written back to the monitor after each round.
type IQProbeMonitorRunState struct {
	LastRunAt          time.Time
	LastStatus         string
	LastScore          *float64
	ConsecutiveLow     int
	ConsecutiveInvalid int
	AlertState         string
}

type IQProbeRepository interface {
	ListSets(ctx context.Context) ([]*IQProbeQuestionSet, error)
	GetSet(ctx context.Context, id int64) (*IQProbeQuestionSet, error)
	CreateSet(ctx context.Context, set *IQProbeQuestionSet) (*IQProbeQuestionSet, error)
	UpdateSet(ctx context.Context, set *IQProbeQuestionSet) (*IQProbeQuestionSet, error)
	DeleteSet(ctx context.Context, id int64) error

	ListQuestions(ctx context.Context, setID int64) ([]*IQProbeQuestion, error)
	GetQuestion(ctx context.Context, id int64) (*IQProbeQuestion, error)
	CreateQuestion(ctx context.Context, q *IQProbeQuestion) (*IQProbeQuestion, error)
	UpdateQuestion(ctx context.Context, q *IQProbeQuestion) (*IQProbeQuestion, error)
	DeleteQuestion(ctx context.Context, id int64) error

	ListMonitors(ctx context.Context) ([]*IQProbeMonitor, error)
	GetMonitor(ctx context.Context, id int64) (*IQProbeMonitor, error)
	CreateMonitor(ctx context.Context, m *IQProbeMonitor) (*IQProbeMonitor, error)
	UpdateMonitor(ctx context.Context, m *IQProbeMonitor) (*IQProbeMonitor, error)
	DeleteMonitor(ctx context.Context, id int64) error
	ListDueMonitorIDs(ctx context.Context, now time.Time, limit int) ([]int64, error)
	// ClaimMonitor moves next_run_at forward only for an enabled, still-due
	// monitor whose timestamp equals expected (or is NULL when expected is nil).
	ClaimMonitor(ctx context.Context, id int64, expected *time.Time, next time.Time) (bool, error)
	UpdateMonitorRunState(ctx context.Context, id int64, state IQProbeMonitorRunState) error

	CreateRun(ctx context.Context, run *IQProbeRun) (int64, error)
	FinishRun(ctx context.Context, run *IQProbeRun) error
	InsertAnswer(ctx context.Context, a *IQProbeAnswer) error
	ListRuns(ctx context.Context, filter IQProbeRunFilter) ([]*IQProbeRun, int64, error)
	ListAnswersByRuns(ctx context.Context, runIDs []int64, withText bool) ([]*IQProbeAnswer, error)
	GetAnswer(ctx context.Context, id int64) (*IQProbeAnswer, error)
	ListScorePoints(ctx context.Context, since time.Time) (map[int64][]IQProbeScorePoint, error)
	// MarkStaleRunsInvalid closes runs left "running" by a crashed instance.
	MarkStaleRunsInvalid(ctx context.Context, before time.Time) error

	PruneAnswers(ctx context.Context, before time.Time) (int64, error)
	PruneRuns(ctx context.Context, before time.Time) (int64, error)
}
