package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	iqProbeMinIntervalMinutes     = 10
	iqProbeDefaultIntervalMinutes = 60
	iqProbeMaxIntervalMinutes     = 7 * 24 * 60
	iqProbeMaxConcurrentMonitors  = 3
	iqProbeQuestionTimeout        = 5 * time.Minute
	iqProbeMaxResponseBytes       = 16 << 10
	iqProbeAnswerRetention        = 30 * 24 * time.Hour
	iqProbeRunRetention           = 180 * 24 * time.Hour
	iqProbeStaleRunAfter          = 2 * time.Hour
	iqProbeMaxQuestionsPerImport  = 200
)

var (
	ErrIQProbeSetNotFound      = infraerrors.NotFound("IQ_PROBE_SET_NOT_FOUND", "question set not found")
	ErrIQProbeQuestionNotFound = infraerrors.NotFound("IQ_PROBE_QUESTION_NOT_FOUND", "question not found")
	ErrIQProbeMonitorNotFound  = infraerrors.NotFound("IQ_PROBE_MONITOR_NOT_FOUND", "monitor not found")
	ErrIQProbeSetInUse         = infraerrors.Conflict("IQ_PROBE_SET_IN_USE", "question set is used by monitors")
	ErrIQProbeMonitorDuplicate = infraerrors.Conflict("IQ_PROBE_MONITOR_DUPLICATE", "monitor already exists for this account, model and question set")
	ErrIQProbeMonitorBusy      = infraerrors.Conflict("IQ_PROBE_MONITOR_BUSY", "monitor is already running")
)

// IQProbeService manages question sets, monitors and probe rounds.
type IQProbeService struct {
	repo           IQProbeRepository
	accountRepo    AccountRepository
	accountTestSvc *AccountTestService
	billingSvc     *BillingService
	opsService     *OpsService
	emailService   *EmailService

	runningMu sync.Mutex
	running   map[int64]bool
	sem       chan struct{}
}

func NewIQProbeService(
	repo IQProbeRepository,
	accountRepo AccountRepository,
	accountTestSvc *AccountTestService,
	billingSvc *BillingService,
	opsService *OpsService,
	emailService *EmailService,
) *IQProbeService {
	return &IQProbeService{
		repo:           repo,
		accountRepo:    accountRepo,
		accountTestSvc: accountTestSvc,
		billingSvc:     billingSvc,
		opsService:     opsService,
		emailService:   emailService,
		running:        map[int64]bool{},
		sem:            make(chan struct{}, iqProbeMaxConcurrentMonitors),
	}
}

// ---- question sets ----

func (s *IQProbeService) ListSets(ctx context.Context) ([]*IQProbeQuestionSet, error) {
	return s.repo.ListSets(ctx)
}

func (s *IQProbeService) CreateSet(ctx context.Context, set *IQProbeQuestionSet) (*IQProbeQuestionSet, error) {
	if err := normalizeIQProbeSet(set); err != nil {
		return nil, err
	}
	return s.repo.CreateSet(ctx, set)
}

func (s *IQProbeService) UpdateSet(ctx context.Context, set *IQProbeQuestionSet) (*IQProbeQuestionSet, error) {
	if err := normalizeIQProbeSet(set); err != nil {
		return nil, err
	}
	return s.repo.UpdateSet(ctx, set)
}

func (s *IQProbeService) DeleteSet(ctx context.Context, id int64) error {
	set, err := s.repo.GetSet(ctx, id)
	if err != nil {
		return err
	}
	if set.MonitorCount > 0 {
		return ErrIQProbeSetInUse
	}
	return s.repo.DeleteSet(ctx, id)
}

func normalizeIQProbeSet(set *IQProbeQuestionSet) error {
	set.Name = strings.TrimSpace(set.Name)
	set.Description = strings.TrimSpace(set.Description)
	if set.Name == "" || len([]rune(set.Name)) > 100 {
		return infraerrors.BadRequest("IQ_PROBE_INVALID_SET", "name is required (max 100 chars)")
	}
	return nil
}

// ---- questions ----

func (s *IQProbeService) ListQuestions(ctx context.Context, setID int64) ([]*IQProbeQuestion, error) {
	if _, err := s.repo.GetSet(ctx, setID); err != nil {
		return nil, err
	}
	return s.repo.ListQuestions(ctx, setID)
}

func (s *IQProbeService) CreateQuestion(ctx context.Context, q *IQProbeQuestion) (*IQProbeQuestion, error) {
	if _, err := s.repo.GetSet(ctx, q.SetID); err != nil {
		return nil, err
	}
	if err := normalizeIQProbeQuestion(q); err != nil {
		return nil, err
	}
	return s.repo.CreateQuestion(ctx, q)
}

func (s *IQProbeService) UpdateQuestion(ctx context.Context, q *IQProbeQuestion) (*IQProbeQuestion, error) {
	existing, err := s.repo.GetQuestion(ctx, q.ID)
	if err != nil {
		return nil, err
	}
	q.SetID = existing.SetID
	if err := normalizeIQProbeQuestion(q); err != nil {
		return nil, err
	}
	return s.repo.UpdateQuestion(ctx, q)
}

func (s *IQProbeService) DeleteQuestion(ctx context.Context, id int64) error {
	return s.repo.DeleteQuestion(ctx, id)
}

func normalizeIQProbeQuestion(q *IQProbeQuestion) error {
	q.Title = strings.TrimSpace(q.Title)
	q.Prompt = strings.TrimSpace(q.Prompt)
	q.SystemPrompt = strings.TrimSpace(q.SystemPrompt)
	q.ExpectedAnswer = strings.TrimSpace(q.ExpectedAnswer)
	q.Note = strings.TrimSpace(q.Note)
	q.JudgeType = strings.TrimSpace(strings.ToLower(q.JudgeType))
	if q.JudgeType == "" {
		q.JudgeType = IQProbeJudgeExact
	}
	bad := func(msg string) error { return infraerrors.BadRequest("IQ_PROBE_INVALID_QUESTION", msg) }
	switch q.JudgeType {
	case IQProbeJudgeExact, IQProbeJudgeChoice, IQProbeJudgeNumeric:
	default:
		return bad("judge_type must be exact, choice or numeric")
	}
	if q.Prompt == "" {
		return bad("prompt is required")
	}
	if q.ExpectedAnswer == "" {
		return bad("expected_answer is required")
	}
	if len([]rune(q.Title)) > 200 {
		return bad("title is too long (max 200 chars)")
	}
	for _, alt := range strings.Split(q.ExpectedAnswer, iqProbeAlternativesSep) {
		alt = strings.TrimSpace(alt)
		if alt == "" {
			continue
		}
		switch q.JudgeType {
		case IQProbeJudgeNumeric:
			if _, ok := parseIQProbeNumber(alt); !ok {
				return bad("numeric expected_answer must contain a number: " + alt)
			}
		case IQProbeJudgeChoice:
			if iqProbeChoiceLetters(alt) == "" {
				return bad("choice expected_answer must be option letters like B or AC: " + alt)
			}
		}
	}
	if q.NumericTolerance < 0 {
		return bad("numeric_tolerance must be >= 0")
	}
	if q.Weight <= 0 {
		q.Weight = 1
	}
	if q.Weight > 100 {
		return bad("weight must be <= 100")
	}
	return nil
}

// IQProbeSetExport is the JSON import/export format of one question set.
type IQProbeSetExport struct {
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Questions   []IQProbeQuestionExport `json:"questions"`
}

type IQProbeQuestionExport struct {
	Title            string  `json:"title"`
	Prompt           string  `json:"prompt"`
	SystemPrompt     string  `json:"system_prompt,omitempty"`
	ExpectedAnswer   string  `json:"expected_answer"`
	JudgeType        string  `json:"judge_type"`
	NumericTolerance float64 `json:"numeric_tolerance,omitempty"`
	Weight           float64 `json:"weight"`
	Enabled          *bool   `json:"enabled,omitempty"`
	Note             string  `json:"note,omitempty"`
}

func (s *IQProbeService) ExportSet(ctx context.Context, setID int64) (*IQProbeSetExport, error) {
	set, err := s.repo.GetSet(ctx, setID)
	if err != nil {
		return nil, err
	}
	questions, err := s.repo.ListQuestions(ctx, setID)
	if err != nil {
		return nil, err
	}
	out := &IQProbeSetExport{Name: set.Name, Description: set.Description, Questions: make([]IQProbeQuestionExport, 0, len(questions))}
	for _, q := range questions {
		enabled := q.Enabled
		out.Questions = append(out.Questions, IQProbeQuestionExport{
			Title: q.Title, Prompt: q.Prompt, SystemPrompt: q.SystemPrompt, ExpectedAnswer: q.ExpectedAnswer,
			JudgeType: q.JudgeType, NumericTolerance: q.NumericTolerance, Weight: q.Weight, Enabled: &enabled, Note: q.Note,
		})
	}
	return out, nil
}

// ImportSet creates a new set from an export. Every question is validated
// before anything is written, so a bad file leaves no half-imported set.
func (s *IQProbeService) ImportSet(ctx context.Context, data *IQProbeSetExport) (*IQProbeQuestionSet, error) {
	set := &IQProbeQuestionSet{Name: data.Name, Description: data.Description}
	if err := normalizeIQProbeSet(set); err != nil {
		return nil, err
	}
	if len(data.Questions) == 0 || len(data.Questions) > iqProbeMaxQuestionsPerImport {
		return nil, infraerrors.BadRequest("IQ_PROBE_INVALID_IMPORT", fmt.Sprintf("questions must have 1..%d items", iqProbeMaxQuestionsPerImport))
	}
	questions := make([]*IQProbeQuestion, 0, len(data.Questions))
	for i, e := range data.Questions {
		enabled := true
		if e.Enabled != nil {
			enabled = *e.Enabled
		}
		q := &IQProbeQuestion{
			Title: e.Title, Prompt: e.Prompt, SystemPrompt: e.SystemPrompt, ExpectedAnswer: e.ExpectedAnswer,
			JudgeType: e.JudgeType, NumericTolerance: e.NumericTolerance, Weight: e.Weight, Enabled: enabled,
			Note: e.Note, SortOrder: i,
		}
		if err := normalizeIQProbeQuestion(q); err != nil {
			return nil, infraerrors.BadRequest("IQ_PROBE_INVALID_IMPORT", fmt.Sprintf("question #%d: %s", i+1, infraerrors.Message(err)))
		}
		questions = append(questions, q)
	}
	created, err := s.repo.CreateSet(ctx, set)
	if err != nil {
		return nil, err
	}
	for _, q := range questions {
		q.SetID = created.ID
		if _, err := s.repo.CreateQuestion(ctx, q); err != nil {
			return nil, err
		}
	}
	return s.repo.GetSet(ctx, created.ID)
}

// ---- monitors ----

// IQProbeMonitorView adds the recent score trend to a monitor row.
type IQProbeMonitorView struct {
	*IQProbeMonitor
	Trend []IQProbeScorePoint `json:"trend"`
}

func (s *IQProbeService) ListMonitors(ctx context.Context) ([]*IQProbeMonitorView, error) {
	monitors, err := s.repo.ListMonitors(ctx)
	if err != nil {
		return nil, err
	}
	points, err := s.repo.ListScorePoints(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	out := make([]*IQProbeMonitorView, 0, len(monitors))
	for _, m := range monitors {
		out = append(out, &IQProbeMonitorView{IQProbeMonitor: m, Trend: points[m.ID]})
	}
	return out, nil
}

func (s *IQProbeService) GetMonitor(ctx context.Context, id int64) (*IQProbeMonitor, error) {
	return s.repo.GetMonitor(ctx, id)
}

// CreateMonitors creates one monitor per account (batch from the picker).
// Duplicates of an existing account+model+set are skipped and reported.
func (s *IQProbeService) CreateMonitors(ctx context.Context, tmpl *IQProbeMonitor, accountIDs []int64) (created []*IQProbeMonitor, skipped []int64, err error) {
	if len(accountIDs) == 0 {
		return nil, nil, infraerrors.BadRequest("IQ_PROBE_INVALID_MONITOR", "account_ids is required")
	}
	if err := normalizeIQProbeMonitor(tmpl); err != nil {
		return nil, nil, err
	}
	if _, err := s.repo.GetSet(ctx, tmpl.SetID); err != nil {
		return nil, nil, err
	}
	for _, accountID := range accountIDs {
		if _, err := s.accountRepo.GetByID(ctx, accountID); err != nil {
			return created, skipped, infraerrors.BadRequest("IQ_PROBE_INVALID_MONITOR", fmt.Sprintf("account %d not found", accountID))
		}
		m := *tmpl
		m.AccountID = accountID
		// Spread first runs over the first interval so a batch does not fire at once.
		first := time.Now().Add(time.Duration(rand.Int64N(int64(time.Duration(m.IntervalMinutes) * time.Minute / 4))))
		m.NextRunAt = &first
		row, err := s.repo.CreateMonitor(ctx, &m)
		if errors.Is(err, ErrIQProbeMonitorDuplicate) {
			skipped = append(skipped, accountID)
			continue
		}
		if err != nil {
			return created, skipped, err
		}
		created = append(created, row)
	}
	return created, skipped, nil
}

func (s *IQProbeService) UpdateMonitor(ctx context.Context, m *IQProbeMonitor) (*IQProbeMonitor, error) {
	existing, err := s.repo.GetMonitor(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	if err := normalizeIQProbeMonitor(m); err != nil {
		return nil, err
	}
	if _, err := s.repo.GetSet(ctx, m.SetID); err != nil {
		return nil, err
	}
	m.AccountID = existing.AccountID
	m.NextRunAt = existing.NextRunAt
	if m.IntervalMinutes != existing.IntervalMinutes || (m.Enabled && !existing.Enabled) || m.NextRunAt == nil {
		next := time.Now().Add(iqProbeJitteredInterval(m.IntervalMinutes))
		if existing.LastRunAt != nil {
			if fromLast := existing.LastRunAt.Add(time.Duration(m.IntervalMinutes) * time.Minute); fromLast.Before(next) {
				next = fromLast
			}
		}
		m.NextRunAt = &next
	}
	// Changing what is being measured restarts the alert streaks.
	if m.Model != existing.Model || m.SetID != existing.SetID || m.ReasoningEffort != existing.ReasoningEffort {
		m.ConsecutiveLow, m.ConsecutiveInvalid, m.AlertState = 0, 0, IQProbeAlertOK
	} else {
		m.ConsecutiveLow, m.ConsecutiveInvalid, m.AlertState = existing.ConsecutiveLow, existing.ConsecutiveInvalid, existing.AlertState
	}
	return s.repo.UpdateMonitor(ctx, m)
}

func (s *IQProbeService) DeleteMonitor(ctx context.Context, id int64) error {
	return s.repo.DeleteMonitor(ctx, id)
}

func normalizeIQProbeMonitor(m *IQProbeMonitor) error {
	bad := func(msg string) error { return infraerrors.BadRequest("IQ_PROBE_INVALID_MONITOR", msg) }
	m.Model = strings.TrimSpace(m.Model)
	m.ReasoningEffort = strings.TrimSpace(strings.ToLower(m.ReasoningEffort))
	if m.Model == "" {
		return bad("model is required")
	}
	if m.SetID <= 0 {
		return bad("set_id is required")
	}
	if m.IntervalMinutes == 0 {
		m.IntervalMinutes = iqProbeDefaultIntervalMinutes
	}
	if m.IntervalMinutes < iqProbeMinIntervalMinutes || m.IntervalMinutes > iqProbeMaxIntervalMinutes {
		return bad(fmt.Sprintf("interval_minutes must be %d..%d", iqProbeMinIntervalMinutes, iqProbeMaxIntervalMinutes))
	}
	switch m.ReasoningEffort {
	case "", "minimal", "low", "medium", "high", "xhigh":
	default:
		return bad("reasoning_effort must be empty, minimal, low, medium, high or xhigh")
	}
	if m.MaxTokens < 0 || m.MaxTokens > 128000 {
		return bad("max_tokens must be 0..128000")
	}
	if m.AlertThreshold <= 0 || m.AlertThreshold > 1 {
		return bad("alert_threshold must be in (0, 1]")
	}
	if m.AlertLowRounds <= 0 {
		m.AlertLowRounds = 2
	}
	if m.AlertInvalidRounds <= 0 {
		m.AlertInvalidRounds = 3
	}
	if m.AlertState == "" {
		m.AlertState = IQProbeAlertOK
	}
	return nil
}

// iqProbeJitteredInterval adds ±10% jitter so monitors drift apart.
func iqProbeJitteredInterval(minutes int) time.Duration {
	base := time.Duration(minutes) * time.Minute
	jitter := base / 10
	return base - jitter + time.Duration(rand.Int64N(int64(2*jitter)+1))
}

// ---- runs ----

func (s *IQProbeService) ListRuns(ctx context.Context, filter IQProbeRunFilter) ([]*IQProbeRun, int64, error) {
	if filter.PageSize <= 0 || filter.PageSize > 200 {
		filter.PageSize = 50
	}
	if filter.Page <= 0 {
		filter.Page = 1
	}
	return s.repo.ListRuns(ctx, filter)
}

// IQProbeMatrix is the rounds × questions grid of one monitor.
type IQProbeMatrix struct {
	Runs    []*IQProbeRun    `json:"runs"`
	Answers []*IQProbeAnswer `json:"answers"`
}

func (s *IQProbeService) GetMatrix(ctx context.Context, monitorID int64, limit int) (*IQProbeMatrix, error) {
	if _, err := s.repo.GetMonitor(ctx, monitorID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	runs, _, err := s.repo.ListRuns(ctx, IQProbeRunFilter{MonitorID: monitorID, Page: 1, PageSize: limit})
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(runs))
	for _, r := range runs {
		ids = append(ids, r.ID)
	}
	answers, err := s.repo.ListAnswersByRuns(ctx, ids, false)
	if err != nil {
		return nil, err
	}
	return &IQProbeMatrix{Runs: runs, Answers: answers}, nil
}

func (s *IQProbeService) GetAnswer(ctx context.Context, id int64) (*IQProbeAnswer, error) {
	return s.repo.GetAnswer(ctx, id)
}

// RunNow starts a manual round in the background and returns immediately.
func (s *IQProbeService) RunNow(ctx context.Context, monitorID int64) error {
	m, err := s.repo.GetMonitor(ctx, monitorID)
	if err != nil {
		return err
	}
	if !s.tryMarkRunning(m.ID) {
		return ErrIQProbeMonitorBusy
	}
	go func() {
		defer s.unmarkRunning(m.ID)
		s.sem <- struct{}{}
		defer func() { <-s.sem }()
		s.runRound(context.Background(), m, "manual")
	}()
	return nil
}

func (s *IQProbeService) tryMarkRunning(id int64) bool {
	s.runningMu.Lock()
	defer s.runningMu.Unlock()
	if s.running[id] {
		return false
	}
	s.running[id] = true
	return true
}

func (s *IQProbeService) unmarkRunning(id int64) {
	s.runningMu.Lock()
	delete(s.running, id)
	s.runningMu.Unlock()
}

// RunDue claims every due monitor and runs it, at most
// iqProbeMaxConcurrentMonitors at a time. Called by the runner each minute.
func (s *IQProbeService) RunDue(ctx context.Context) {
	ids, err := s.repo.ListDueMonitorIDs(ctx, time.Now(), 100)
	if err != nil {
		logger.LegacyPrintf("service.iq_probe", "[IQProbe] list due failed: %v", err)
		return
	}
	for _, id := range ids {
		m, err := s.repo.GetMonitor(ctx, id)
		if err != nil || !m.Enabled {
			continue
		}
		// Another instance may have claimed this ID since the due-list snapshot.
		if m.NextRunAt != nil && m.NextRunAt.After(time.Now()) {
			continue
		}
		if !s.tryMarkRunning(m.ID) {
			continue
		}
		next := time.Now().Add(iqProbeJitteredInterval(m.IntervalMinutes))
		claimed, err := s.repo.ClaimMonitor(ctx, m.ID, m.NextRunAt, next)
		if err != nil || !claimed {
			s.unmarkRunning(m.ID)
			continue
		}
		go func(m *IQProbeMonitor) {
			defer s.unmarkRunning(m.ID)
			s.sem <- struct{}{}
			defer func() { <-s.sem }()
			s.runRound(context.Background(), m, "schedule")
		}(m)
	}
}

// runRound asks every enabled question of the monitor's set, one at a time,
// then scores the round, updates the monitor and fires alerts.
func (s *IQProbeService) runRound(ctx context.Context, m *IQProbeMonitor, trigger string) {
	run := &IQProbeRun{
		MonitorID: m.ID, AccountID: m.AccountID, Model: m.Model, SetID: m.SetID,
		Trigger: trigger, Status: IQProbeRunRunning, StartedAt: time.Now(),
	}
	runID, err := s.repo.CreateRun(ctx, run)
	if err != nil {
		logger.LegacyPrintf("service.iq_probe", "[IQProbe] monitor=%d create run failed: %v", m.ID, err)
		return
	}
	run.ID = runID

	skip := func(msg string) {
		run.Status = IQProbeRunSkipped
		run.Message = msg
		s.finishRound(ctx, m, run)
	}

	account, err := s.accountRepo.GetByID(ctx, m.AccountID)
	if err != nil {
		skip("账号不存在")
		return
	}
	if !account.IsSchedulable() {
		skip(fmt.Sprintf("账号当前不可调度（状态 %s），本轮跳过", account.Status))
		return
	}
	questions, err := s.repo.ListQuestions(ctx, m.SetID)
	if err != nil {
		skip("读取题目失败: " + err.Error())
		return
	}
	enabled := questions[:0]
	for _, q := range questions {
		if q.Enabled {
			enabled = append(enabled, q)
		}
	}
	if len(enabled) == 0 {
		skip("题组没有启用的题目")
		return
	}

	answers := make([]*IQProbeAnswer, 0, len(enabled))
	for _, q := range enabled {
		a := s.askQuestion(ctx, m, q)
		a.RunID = run.ID
		if err := s.repo.InsertAnswer(ctx, a); err != nil {
			logger.LegacyPrintf("service.iq_probe", "[IQProbe] run=%d insert answer failed: %v", run.ID, err)
		}
		run.InputTokens += a.InputTokens
		run.OutputTokens += a.OutputTokens
		run.CostUSD += a.CostUSD
		answers = append(answers, a)
	}
	iqProbeScoreRun(run, answers)
	s.finishRound(ctx, m, run)
}

func (s *IQProbeService) askQuestion(ctx context.Context, m *IQProbeMonitor, q *IQProbeQuestion) *IQProbeAnswer {
	qctx, cancel := context.WithTimeout(ctx, iqProbeQuestionTimeout)
	defer cancel()
	res := s.accountTestSvc.RunProbe(qctx, m.AccountID, m.Model, AccountProbeRequest{
		Prompt:          buildIQProbePrompt(q.Prompt),
		System:          q.SystemPrompt,
		ReasoningEffort: m.ReasoningEffort,
		MaxTokens:       m.MaxTokens,
	})
	verdict, extracted := iqProbeVerdict(q, res)
	a := &IQProbeAnswer{
		QuestionID: q.ID, QuestionTitle: q.Title, ExpectedAnswer: q.ExpectedAnswer, JudgeType: q.JudgeType, Weight: q.Weight,
		Verdict: verdict, ExtractedAnswer: extracted,
		ResponseText:  truncateIQProbeText(res.ResponseText),
		ErrorMessage:  truncateIQProbeText(res.ErrorMessage),
		UpstreamModel: res.UpstreamModel, ResponseModel: res.ResponseModel,
		InputTokens: res.InputTokens, OutputTokens: res.OutputTokens, ReasoningTokens: res.ReasoningTokens,
		LatencyMs: res.LatencyMs, FirstTokenMs: res.FirstTokenMs,
	}
	a.CostUSD = s.estimateCost(m.Model, res)
	return a
}

// estimateCost prices the probe at catalog rates of the requested model,
// falling back to the model actually sent upstream.
func (s *IQProbeService) estimateCost(model string, res *AccountProbeResult) float64 {
	if s.billingSvc == nil || (res.InputTokens == 0 && res.OutputTokens == 0) {
		return 0
	}
	for _, candidate := range []string{model, res.UpstreamModel} {
		if candidate == "" {
			continue
		}
		if cost, err := s.billingSvc.GetEstimatedCost(candidate, int(res.InputTokens), int(res.OutputTokens)); err == nil && cost > 0 {
			return cost
		}
	}
	return 0
}

func truncateIQProbeText(s string) string {
	if len(s) <= iqProbeMaxResponseBytes {
		return s
	}
	cut := iqProbeMaxResponseBytes
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "\n…(已截断)"
}

func (s *IQProbeService) finishRound(ctx context.Context, m *IQProbeMonitor, run *IQProbeRun) {
	now := time.Now()
	run.FinishedAt = &now
	if err := s.repo.FinishRun(ctx, run); err != nil {
		logger.LegacyPrintf("service.iq_probe", "[IQProbe] run=%d finish failed: %v", run.ID, err)
	}

	// Re-read so concurrent edits to thresholds are honoured.
	if fresh, err := s.repo.GetMonitor(ctx, m.ID); err == nil {
		m = fresh
	}
	state, alert := nextIQProbeAlertState(m, run)
	state.LastRunAt = run.StartedAt
	if err := s.repo.UpdateMonitorRunState(ctx, m.ID, state); err != nil {
		logger.LegacyPrintf("service.iq_probe", "[IQProbe] monitor=%d update state failed: %v", m.ID, err)
	}
	if alert != "" {
		s.sendAlert(ctx, m, run, alert)
	}
}

// nextIQProbeAlertState advances the streak counters for one finished round
// and reports which alert (low / invalid / recovered) to send, if any.
// Skipped rounds leave streaks untouched.
func nextIQProbeAlertState(m *IQProbeMonitor, run *IQProbeRun) (IQProbeMonitorRunState, string) {
	state := IQProbeMonitorRunState{
		LastStatus:         run.Status,
		LastScore:          m.LastScore,
		ConsecutiveLow:     m.ConsecutiveLow,
		ConsecutiveInvalid: m.ConsecutiveInvalid,
		AlertState:         m.AlertState,
	}
	if state.AlertState == "" {
		state.AlertState = IQProbeAlertOK
	}
	switch run.Status {
	case IQProbeRunScored:
		state.LastScore = run.Score
		state.ConsecutiveInvalid = 0
		if *run.Score < m.AlertThreshold {
			state.ConsecutiveLow++
		} else {
			state.ConsecutiveLow = 0
		}
	case IQProbeRunInvalid:
		state.ConsecutiveLow = 0
		state.ConsecutiveInvalid++
	default:
		return state, ""
	}
	if !m.AlertEnabled {
		state.AlertState = IQProbeAlertOK
		return state, ""
	}
	switch {
	case run.Status == IQProbeRunScored && state.ConsecutiveLow >= m.AlertLowRounds && state.AlertState != IQProbeAlertLow:
		state.AlertState = IQProbeAlertLow
		return state, IQProbeAlertLow
	case run.Status == IQProbeRunInvalid && state.ConsecutiveInvalid >= m.AlertInvalidRounds && state.AlertState != IQProbeAlertInvalid:
		state.AlertState = IQProbeAlertInvalid
		return state, IQProbeAlertInvalid
	case run.Status == IQProbeRunScored && state.ConsecutiveLow == 0 && state.AlertState != IQProbeAlertOK:
		state.AlertState = IQProbeAlertOK
		return state, IQProbeAlertOK
	}
	return state, ""
}

// sendAlert mails the ops alert recipients. The notify-channel feature only
// exists on dev; switch to it once it is merged into this branch.
func (s *IQProbeService) sendAlert(ctx context.Context, m *IQProbeMonitor, run *IQProbeRun, kind string) {
	if s.opsService == nil || s.emailService == nil {
		return
	}
	cfg, err := s.opsService.GetEmailNotificationConfig(ctx)
	if err != nil || cfg == nil || !cfg.Alert.Enabled || len(cfg.Alert.Recipients) == 0 {
		logger.LegacyPrintf("service.iq_probe", "[IQProbe] monitor=%d alert=%s not sent: ops alert e-mail disabled or no recipients", m.ID, kind)
		return
	}
	target := fmt.Sprintf("%s (#%d) / %s / 题组 %s", m.AccountName, m.AccountID, m.Model, m.SetName)
	var subject, headline string
	switch kind {
	case IQProbeAlertLow:
		subject = "[智力探测] 得分偏低: " + target
		headline = fmt.Sprintf("连续 %d 轮得分低于阈值 %.0f%%，本轮得分 %.0f%%。", m.AlertLowRounds, m.AlertThreshold*100, *run.Score*100)
	case IQProbeAlertInvalid:
		subject = "[智力探测] 连续无效: " + target
		headline = fmt.Sprintf("连续 %d 轮超过一半题目请求失败，无法评分。", m.AlertInvalidRounds)
	default:
		subject = "[智力探测] 已恢复: " + target
		headline = fmt.Sprintf("本轮得分 %.0f%%，已回到阈值 %.0f%% 以上。", *run.Score*100, m.AlertThreshold*100)
	}
	body := fmt.Sprintf(`<p>%s</p><ul><li>账号：%s (#%d, %s)</li><li>模型：%s</li><li>题组：%s</li><li>本轮：答对 %d / 答错 %d / 格式不合规 %d / 请求失败 %d</li><li>时间：%s</li></ul>`,
		headline, m.AccountName, m.AccountID, m.AccountPlatform, m.Model, m.SetName,
		run.CorrectCount, run.WrongCount, run.FormatErrorCount, run.ErrorCount, run.StartedAt.Format(time.RFC3339))
	if run.Message != "" {
		body += "<p>" + run.Message + "</p>"
	}
	for _, to := range cfg.Alert.Recipients {
		to = strings.TrimSpace(to)
		if to == "" {
			continue
		}
		if err := s.emailService.SendEmail(ctx, to, subject, body); err != nil {
			logger.LegacyPrintf("service.iq_probe", "[IQProbe] monitor=%d alert mail to %s failed: %v", m.ID, to, err)
		}
	}
}

// Maintenance closes runs orphaned by a crash and prunes old rows.
func (s *IQProbeService) Maintenance(ctx context.Context) {
	now := time.Now()
	if err := s.repo.MarkStaleRunsInvalid(ctx, now.Add(-iqProbeStaleRunAfter)); err != nil {
		logger.LegacyPrintf("service.iq_probe", "[IQProbe] close stale runs failed: %v", err)
	}
	a, err1 := s.repo.PruneAnswers(ctx, now.Add(-iqProbeAnswerRetention))
	r, err2 := s.repo.PruneRuns(ctx, now.Add(-iqProbeRunRetention))
	if err := errors.Join(err1, err2); err != nil {
		logger.LegacyPrintf("service.iq_probe", "[IQProbe] prune failed: %v", err)
		return
	}
	if a > 0 || r > 0 {
		logger.LegacyPrintf("service.iq_probe", "[IQProbe] pruned answers=%d runs=%d", a, r)
	}
}

// MarshalIQProbeExport renders an export as indented JSON for download.
func MarshalIQProbeExport(e *IQProbeSetExport) ([]byte, error) {
	return json.MarshalIndent(e, "", "  ")
}
