package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type iqProbeRepository struct {
	db *sql.DB
}

func NewIQProbeRepository(db *sql.DB) service.IQProbeRepository {
	return &iqProbeRepository{db: db}
}

// ---- question sets ----

const iqProbeSetSelect = `
	SELECT s.id, s.name, s.description, s.created_at, s.updated_at,
	       (SELECT COUNT(*) FROM iq_probe_questions q WHERE q.set_id = s.id),
	       (SELECT COUNT(*) FROM iq_probe_questions q WHERE q.set_id = s.id AND q.enabled),
	       (SELECT COUNT(*) FROM iq_probe_monitors m WHERE m.set_id = s.id)
	FROM iq_probe_question_sets s`

func scanIQProbeSet(row rowScanner) (*service.IQProbeQuestionSet, error) {
	s := &service.IQProbeQuestionSet{}
	err := row.Scan(&s.ID, &s.Name, &s.Description, &s.CreatedAt, &s.UpdatedAt, &s.QuestionCount, &s.EnabledCount, &s.MonitorCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrIQProbeSetNotFound
	}
	return s, err
}

func (r *iqProbeRepository) ListSets(ctx context.Context) ([]*service.IQProbeQuestionSet, error) {
	rows, err := r.db.QueryContext(ctx, iqProbeSetSelect+` ORDER BY s.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*service.IQProbeQuestionSet{}
	for rows.Next() {
		s, err := scanIQProbeSet(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *iqProbeRepository) GetSet(ctx context.Context, id int64) (*service.IQProbeQuestionSet, error) {
	return scanIQProbeSet(r.db.QueryRowContext(ctx, iqProbeSetSelect+` WHERE s.id = $1`, id))
}

func (r *iqProbeRepository) CreateSet(ctx context.Context, set *service.IQProbeQuestionSet) (*service.IQProbeQuestionSet, error) {
	var id int64
	if err := r.db.QueryRowContext(ctx, `
		INSERT INTO iq_probe_question_sets (name, description) VALUES ($1, $2) RETURNING id
	`, set.Name, set.Description).Scan(&id); err != nil {
		return nil, err
	}
	return r.GetSet(ctx, id)
}

func (r *iqProbeRepository) UpdateSet(ctx context.Context, set *service.IQProbeQuestionSet) (*service.IQProbeQuestionSet, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE iq_probe_question_sets SET name = $2, description = $3, updated_at = NOW() WHERE id = $1
	`, set.ID, set.Name, set.Description)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, service.ErrIQProbeSetNotFound
	}
	return r.GetSet(ctx, set.ID)
}

func (r *iqProbeRepository) DeleteSet(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM iq_probe_question_sets WHERE id = $1`, id)
	var pqErr *pq.Error
	// RESTRICT reports restrict_violation (23001); NO ACTION would be 23503.
	if errors.As(err, &pqErr) && (pqErr.Code == "23001" || pqErr.Code == "23503") {
		return service.ErrIQProbeSetInUse
	}
	return err
}

// ---- questions ----

const iqProbeQuestionColumns = `id, set_id, title, prompt, system_prompt, expected_answer, judge_type,
	numeric_tolerance, weight, enabled, note, sort_order, created_at, updated_at`

func scanIQProbeQuestion(row rowScanner) (*service.IQProbeQuestion, error) {
	q := &service.IQProbeQuestion{}
	err := row.Scan(&q.ID, &q.SetID, &q.Title, &q.Prompt, &q.SystemPrompt, &q.ExpectedAnswer, &q.JudgeType,
		&q.NumericTolerance, &q.Weight, &q.Enabled, &q.Note, &q.SortOrder, &q.CreatedAt, &q.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrIQProbeQuestionNotFound
	}
	return q, err
}

func (r *iqProbeRepository) ListQuestions(ctx context.Context, setID int64) ([]*service.IQProbeQuestion, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+iqProbeQuestionColumns+` FROM iq_probe_questions
		WHERE set_id = $1 ORDER BY sort_order, id`, setID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*service.IQProbeQuestion{}
	for rows.Next() {
		q, err := scanIQProbeQuestion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func (r *iqProbeRepository) GetQuestion(ctx context.Context, id int64) (*service.IQProbeQuestion, error) {
	return scanIQProbeQuestion(r.db.QueryRowContext(ctx, `SELECT `+iqProbeQuestionColumns+` FROM iq_probe_questions WHERE id = $1`, id))
}

func (r *iqProbeRepository) CreateQuestion(ctx context.Context, q *service.IQProbeQuestion) (*service.IQProbeQuestion, error) {
	return scanIQProbeQuestion(r.db.QueryRowContext(ctx, `
		INSERT INTO iq_probe_questions (set_id, title, prompt, system_prompt, expected_answer, judge_type,
			numeric_tolerance, weight, enabled, note, sort_order)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING `+iqProbeQuestionColumns,
		q.SetID, q.Title, q.Prompt, q.SystemPrompt, q.ExpectedAnswer, q.JudgeType,
		q.NumericTolerance, q.Weight, q.Enabled, q.Note, q.SortOrder))
}

func (r *iqProbeRepository) UpdateQuestion(ctx context.Context, q *service.IQProbeQuestion) (*service.IQProbeQuestion, error) {
	return scanIQProbeQuestion(r.db.QueryRowContext(ctx, `
		UPDATE iq_probe_questions SET title = $2, prompt = $3, system_prompt = $4, expected_answer = $5,
			judge_type = $6, numeric_tolerance = $7, weight = $8, enabled = $9, note = $10, sort_order = $11,
			updated_at = NOW()
		WHERE id = $1
		RETURNING `+iqProbeQuestionColumns,
		q.ID, q.Title, q.Prompt, q.SystemPrompt, q.ExpectedAnswer, q.JudgeType,
		q.NumericTolerance, q.Weight, q.Enabled, q.Note, q.SortOrder))
}

func (r *iqProbeRepository) DeleteQuestion(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM iq_probe_questions WHERE id = $1`, id)
	return err
}

// ---- monitors ----

const iqProbeMonitorSelect = `
	SELECT m.id, m.account_id, m.group_id, m.model, m.set_id, m.interval_minutes, m.reasoning_effort, m.max_tokens,
	       m.enabled, m.alert_enabled, m.alert_threshold, m.alert_low_rounds, m.alert_invalid_rounds, m.alert_state,
	       m.consecutive_low, m.consecutive_invalid, m.last_run_at, m.next_run_at, m.last_status, m.last_score,
	       m.created_at, m.updated_at,
	       COALESCE(a.name, ''), COALESCE(a.platform, ''), COALESCE(a.type, ''), COALESCE(a.status, ''),
	       COALESCE(g.name, ''), COALESCE(s.name, '')
	FROM iq_probe_monitors m
	LEFT JOIN accounts a ON a.id = m.account_id
	LEFT JOIN groups g ON g.id = m.group_id
	LEFT JOIN iq_probe_question_sets s ON s.id = m.set_id`

func scanIQProbeMonitor(row rowScanner) (*service.IQProbeMonitor, error) {
	m := &service.IQProbeMonitor{}
	var groupID sql.NullInt64
	var lastRun, nextRun sql.NullTime
	var lastScore sql.NullFloat64
	err := row.Scan(&m.ID, &m.AccountID, &groupID, &m.Model, &m.SetID, &m.IntervalMinutes, &m.ReasoningEffort, &m.MaxTokens,
		&m.Enabled, &m.AlertEnabled, &m.AlertThreshold, &m.AlertLowRounds, &m.AlertInvalidRounds, &m.AlertState,
		&m.ConsecutiveLow, &m.ConsecutiveInvalid, &lastRun, &nextRun, &m.LastStatus, &lastScore,
		&m.CreatedAt, &m.UpdatedAt,
		&m.AccountName, &m.AccountPlatform, &m.AccountType, &m.AccountStatus, &m.GroupName, &m.SetName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrIQProbeMonitorNotFound
	}
	if err != nil {
		return nil, err
	}
	if groupID.Valid {
		m.GroupID = &groupID.Int64
	}
	if lastRun.Valid {
		m.LastRunAt = &lastRun.Time
	}
	if nextRun.Valid {
		m.NextRunAt = &nextRun.Time
	}
	if lastScore.Valid {
		m.LastScore = &lastScore.Float64
	}
	return m, nil
}

func (r *iqProbeRepository) ListMonitors(ctx context.Context) ([]*service.IQProbeMonitor, error) {
	rows, err := r.db.QueryContext(ctx, iqProbeMonitorSelect+` ORDER BY m.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*service.IQProbeMonitor{}
	for rows.Next() {
		m, err := scanIQProbeMonitor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *iqProbeRepository) GetMonitor(ctx context.Context, id int64) (*service.IQProbeMonitor, error) {
	return scanIQProbeMonitor(r.db.QueryRowContext(ctx, iqProbeMonitorSelect+` WHERE m.id = $1`, id))
}

func iqProbeMonitorWriteErr(err error) error {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		return service.ErrIQProbeMonitorDuplicate
	}
	return err
}

func (r *iqProbeRepository) CreateMonitor(ctx context.Context, m *service.IQProbeMonitor) (*service.IQProbeMonitor, error) {
	var id int64
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO iq_probe_monitors (account_id, group_id, model, set_id, interval_minutes, reasoning_effort, max_tokens,
			enabled, alert_enabled, alert_threshold, alert_low_rounds, alert_invalid_rounds, next_run_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING id`,
		m.AccountID, m.GroupID, m.Model, m.SetID, m.IntervalMinutes, m.ReasoningEffort, m.MaxTokens,
		m.Enabled, m.AlertEnabled, m.AlertThreshold, m.AlertLowRounds, m.AlertInvalidRounds, m.NextRunAt).Scan(&id)
	if err != nil {
		return nil, iqProbeMonitorWriteErr(err)
	}
	return r.GetMonitor(ctx, id)
}

func (r *iqProbeRepository) UpdateMonitor(ctx context.Context, m *service.IQProbeMonitor) (*service.IQProbeMonitor, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE iq_probe_monitors SET group_id = $2, model = $3, set_id = $4, interval_minutes = $5,
			reasoning_effort = $6, max_tokens = $7, enabled = $8, alert_enabled = $9, alert_threshold = $10,
			alert_low_rounds = $11, alert_invalid_rounds = $12, next_run_at = $13, consecutive_low = $14,
			consecutive_invalid = $15, alert_state = $16, updated_at = NOW()
		WHERE id = $1`,
		m.ID, m.GroupID, m.Model, m.SetID, m.IntervalMinutes, m.ReasoningEffort, m.MaxTokens, m.Enabled,
		m.AlertEnabled, m.AlertThreshold, m.AlertLowRounds, m.AlertInvalidRounds, m.NextRunAt,
		m.ConsecutiveLow, m.ConsecutiveInvalid, m.AlertState)
	if err != nil {
		return nil, iqProbeMonitorWriteErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, service.ErrIQProbeMonitorNotFound
	}
	return r.GetMonitor(ctx, m.ID)
}

func (r *iqProbeRepository) DeleteMonitor(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM iq_probe_monitors WHERE id = $1`, id)
	return err
}

func (r *iqProbeRepository) ListDueMonitorIDs(ctx context.Context, now time.Time, limit int) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id FROM iq_probe_monitors
		WHERE enabled = TRUE AND (next_run_at IS NULL OR next_run_at <= $1)
		ORDER BY next_run_at NULLS FIRST LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *iqProbeRepository) ClaimMonitor(ctx context.Context, id int64, expected *time.Time, next time.Time) (bool, error) {
	var res sql.Result
	var err error
	if expected == nil {
		res, err = r.db.ExecContext(ctx, `UPDATE iq_probe_monitors SET next_run_at = $2
			WHERE id = $1 AND enabled = TRUE AND next_run_at IS NULL`, id, next)
	} else {
		res, err = r.db.ExecContext(ctx, `UPDATE iq_probe_monitors SET next_run_at = $2
			WHERE id = $1 AND enabled = TRUE AND next_run_at = $3 AND next_run_at <= NOW()`, id, next, *expected)
	}
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (r *iqProbeRepository) UpdateMonitorRunState(ctx context.Context, id int64, st service.IQProbeMonitorRunState) (bool, error) {
	return updateIQProbeMonitorRunState(ctx, r.db, id, st)
}

func updateIQProbeMonitorRunState(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, id int64, st service.IQProbeMonitorRunState) (bool, error) {
	res, err := db.ExecContext(ctx, `
		UPDATE iq_probe_monitors SET last_run_at = $2, last_status = $3, last_score = $4,
			consecutive_low = $5, consecutive_invalid = $6, alert_state = $7
		WHERE id = $1 AND model = $8 AND set_id = $9 AND reasoning_effort = $10`,
		id, st.LastRunAt, st.LastStatus, st.LastScore, st.ConsecutiveLow, st.ConsecutiveInvalid, st.AlertState,
		st.Model, st.SetID, st.ReasoningEffort)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ---- runs & answers ----

func (r *iqProbeRepository) CreateRun(ctx context.Context, run *service.IQProbeRun) (int64, error) {
	var id int64
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO iq_probe_runs (monitor_id, account_id, model, set_id, trigger, status, started_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		run.MonitorID, run.AccountID, run.Model, run.SetID, run.Trigger, run.Status, run.StartedAt).Scan(&id)
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		// uq_iq_probe_runs_monitor_running: a round of this monitor is already running.
		return 0, service.ErrIQProbeMonitorBusy
	}
	return id, err
}

func (r *iqProbeRepository) HeartbeatRun(ctx context.Context, runID int64) (bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE iq_probe_runs SET heartbeat_at = NOW() WHERE id = $1 AND status = 'running'`, runID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (r *iqProbeRepository) FinishRun(ctx context.Context, run *service.IQProbeRun, state *service.IQProbeMonitorRunState) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
		UPDATE iq_probe_runs SET status = $2, score = $3, total_count = $4, correct_count = $5, wrong_count = $6,
			format_error_count = $7, error_count = $8, earned_weight = $9, total_weight = $10,
			input_tokens = $11, output_tokens = $12, cost_usd = $13, message = $14, finished_at = $15
		WHERE id = $1 AND status = 'running'`,
		run.ID, run.Status, run.Score, run.TotalCount, run.CorrectCount, run.WrongCount,
		run.FormatErrorCount, run.ErrorCount, run.EarnedWeight, run.TotalWeight,
		run.InputTokens, run.OutputTokens, run.CostUSD, run.Message, run.FinishedAt)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	applied := true
	if state != nil {
		applied, err = updateIQProbeMonitorRunState(ctx, tx, run.MonitorID, *state)
		if err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return applied, nil
}

func (r *iqProbeRepository) InsertAnswer(ctx context.Context, a *service.IQProbeAnswer) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO iq_probe_answers (run_id, question_id, question_title, expected_answer, judge_type, weight,
			verdict, extracted_answer, response_text, error_message, upstream_model, response_model,
			input_tokens, output_tokens, reasoning_tokens, latency_ms, first_token_ms, cost_usd)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`,
		a.RunID, a.QuestionID, a.QuestionTitle, a.ExpectedAnswer, a.JudgeType, a.Weight,
		a.Verdict, a.ExtractedAnswer, a.ResponseText, a.ErrorMessage, a.UpstreamModel, a.ResponseModel,
		a.InputTokens, a.OutputTokens, a.ReasoningTokens, a.LatencyMs, a.FirstTokenMs, a.CostUSD)
	return err
}

func (r *iqProbeRepository) ListRuns(ctx context.Context, f service.IQProbeRunFilter) ([]*service.IQProbeRun, int64, error) {
	var where []string
	var args []any
	if f.RunID > 0 {
		args = append(args, f.RunID)
		where = append(where, "r.id = $"+itoa(len(args)))
	}
	if f.MonitorID > 0 {
		args = append(args, f.MonitorID)
		where = append(where, "r.monitor_id = $"+itoa(len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		where = append(where, "r.status = $"+itoa(len(args)))
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM iq_probe_runs r`+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.PageSize, (f.Page-1)*f.PageSize)
	rows, err := r.db.QueryContext(ctx, `
		SELECT r.id, r.monitor_id, r.account_id, r.model, r.set_id, r.trigger, r.status, r.score, r.total_count,
		       r.correct_count, r.wrong_count, r.format_error_count, r.error_count, r.earned_weight, r.total_weight,
		       r.input_tokens, r.output_tokens, r.cost_usd, r.message, r.started_at, r.finished_at,
		       COALESCE(a.name, ''), COALESCE(s.name, '')
		FROM iq_probe_runs r
		LEFT JOIN accounts a ON a.id = r.account_id
		LEFT JOIN iq_probe_question_sets s ON s.id = r.set_id`+cond+`
		ORDER BY r.started_at DESC, r.id DESC
		LIMIT $`+itoa(len(args)-1)+` OFFSET $`+itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	out := []*service.IQProbeRun{}
	for rows.Next() {
		run := &service.IQProbeRun{}
		var score sql.NullFloat64
		var finished sql.NullTime
		if err := rows.Scan(&run.ID, &run.MonitorID, &run.AccountID, &run.Model, &run.SetID, &run.Trigger, &run.Status,
			&score, &run.TotalCount, &run.CorrectCount, &run.WrongCount, &run.FormatErrorCount, &run.ErrorCount,
			&run.EarnedWeight, &run.TotalWeight, &run.InputTokens, &run.OutputTokens, &run.CostUSD, &run.Message,
			&run.StartedAt, &finished, &run.AccountName, &run.SetName); err != nil {
			return nil, 0, err
		}
		if score.Valid {
			run.Score = &score.Float64
		}
		if finished.Valid {
			run.FinishedAt = &finished.Time
		}
		out = append(out, run)
	}
	return out, total, rows.Err()
}

const iqProbeAnswerColumns = `id, run_id, question_id, question_title, expected_answer, judge_type, weight, verdict,
	extracted_answer, %s, error_message, upstream_model, response_model, input_tokens, output_tokens,
	reasoning_tokens, latency_ms, first_token_ms, cost_usd, created_at`

func scanIQProbeAnswer(row rowScanner) (*service.IQProbeAnswer, error) {
	a := &service.IQProbeAnswer{}
	err := row.Scan(&a.ID, &a.RunID, &a.QuestionID, &a.QuestionTitle, &a.ExpectedAnswer, &a.JudgeType, &a.Weight,
		&a.Verdict, &a.ExtractedAnswer, &a.ResponseText, &a.ErrorMessage, &a.UpstreamModel, &a.ResponseModel,
		&a.InputTokens, &a.OutputTokens, &a.ReasoningTokens, &a.LatencyMs, &a.FirstTokenMs, &a.CostUSD, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrIQProbeQuestionNotFound
	}
	return a, err
}

func iqProbeAnswerSelect(withText bool) string {
	text := "''"
	if withText {
		text = "response_text"
	}
	return "SELECT " + strings.Replace(iqProbeAnswerColumns, "%s", text, 1) + " FROM iq_probe_answers"
}

func (r *iqProbeRepository) ListAnswersByRuns(ctx context.Context, runIDs []int64, withText bool) ([]*service.IQProbeAnswer, error) {
	out := []*service.IQProbeAnswer{}
	if len(runIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, iqProbeAnswerSelect(withText)+` WHERE run_id = ANY($1) ORDER BY run_id, id`, pq.Array(runIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		a, err := scanIQProbeAnswer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *iqProbeRepository) GetAnswer(ctx context.Context, id int64) (*service.IQProbeAnswer, error) {
	return scanIQProbeAnswer(r.db.QueryRowContext(ctx, iqProbeAnswerSelect(true)+` WHERE id = $1`, id))
}

func (r *iqProbeRepository) ListScorePoints(ctx context.Context, since time.Time) (map[int64][]service.IQProbeScorePoint, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT monitor_id, started_at, score, status FROM iq_probe_runs
		WHERE started_at >= $1 AND status IN ('scored', 'invalid')
		ORDER BY monitor_id, started_at`, since)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64][]service.IQProbeScorePoint{}
	for rows.Next() {
		var id int64
		var p service.IQProbeScorePoint
		var score sql.NullFloat64
		if err := rows.Scan(&id, &p.At, &score, &p.Status); err != nil {
			return nil, err
		}
		if score.Valid {
			v := score.Float64
			p.Score = &v
		}
		out[id] = append(out[id], p)
	}
	return out, rows.Err()
}

func (r *iqProbeRepository) MarkStaleRunsInvalid(ctx context.Context, before time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE iq_probe_runs SET status = 'invalid', message = '运行中断（实例重启或超时）', finished_at = NOW()
		WHERE status = 'running' AND heartbeat_at < $1`, before)
	return err
}

func (r *iqProbeRepository) PruneAnswers(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM iq_probe_answers WHERE created_at < $1`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *iqProbeRepository) PruneRuns(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM iq_probe_runs WHERE started_at < $1`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
