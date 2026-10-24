package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

// Keyword hits are kept in intent_route_hits so the keyword list can be
// reviewed: which keyword pulled which messages onto the routed accounts, and
// what those requests cost. The routing log in Redis keeps only the latest few
// minutes; this table keeps intentRouteHitRetention.
const (
	intentRouteHitBuffer     = 4096
	intentRouteHitBatch      = 200
	intentRouteHitFlushEvery = 2 * time.Second
	intentRouteHitRetention  = 30 * 24 * time.Hour
	intentRouteHitPruneEvery = time.Hour
	intentRouteHitExcerpt    = 80 // runes kept on each side of the keyword
	intentRouteHitMaxPage    = 200
	intentRouteHitMaxHours   = 30 * 24
)

// IntentRouteHit is one request a keyword sent to a rule's accounts.
type IntentRouteHit struct {
	ID        int64     `json:"id"`
	Time      time.Time `json:"time"`
	GroupID   int64     `json:"group_id"`
	UserID    int64     `json:"user_id"`
	APIKeyID  int64     `json:"api_key_id"`
	RequestID string    `json:"request_id"`
	Endpoint  string    `json:"endpoint"`
	Model     string    `json:"model"`
	Rule      string    `json:"rule"`
	Keyword   string    `json:"keyword"`
	// UserTurn is 0 when the latest user message matched, 1 for the one before.
	UserTurn     int `json:"user_turn"`
	MessageChars int `json:"message_chars"`
	// NewestChars is the length of the latest user message; it equals
	// MessageChars when UserTurn is 0.
	NewestChars int    `json:"newest_chars"`
	Excerpt     string `json:"excerpt"`

	// From the request's usage log, when one was written.
	AccountID   *int64   `json:"account_id,omitempty"`
	InputTokens *int64   `json:"input_tokens,omitempty"`
	TotalCost   *float64 `json:"total_cost,omitempty"`
}

// IntentRouteKeywordStat sums up the hits of one keyword.
type IntentRouteKeywordStat struct {
	Keyword  string `json:"keyword"`
	Hits     int64  `json:"hits"`
	Users    int64  `json:"users"`
	APIKeys  int64  `json:"api_keys"`
	AvgChars int64  `json:"avg_chars"`
	// AvgInputTokens includes cache reads and cache creation. Detection prompts are a few hundred
	// tokens; a hit inside a coding agent's long context is a false positive.
	AvgInputTokens int64     `json:"avg_input_tokens"`
	TotalCost      float64   `json:"total_cost"`
	LastHit        time.Time `json:"last_hit"`
}

// IntentRouteHitQuery filters the hit list; zero values mean "any".
type IntentRouteHitQuery struct {
	GroupID  int64
	UserID   int64
	Keyword  string
	Hours    int
	Page     int
	PageSize int
}

type intentRouteHitSink interface {
	add(hit IntentRouteHit)
}

type intentRouteHitDB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// intentRouteHitLog buffers hits off the request path. Service startup starts
// retention cleanup even without traffic; Stop stops admission and drains hits.
type intentRouteHitLog struct {
	db               intentRouteHitDB
	ch               chan IntentRouteHit
	mu               sync.Mutex
	started, stopped bool
	stop, done       chan struct{}
	ctx              context.Context
	cancel           context.CancelFunc
	pruneCtx         context.Context
	cancelPrune      context.CancelFunc
	dropped          atomic.Int64
}

func newIntentRouteHitLog(db intentRouteHitDB) *intentRouteHitLog {
	ctx, cancel := context.WithCancel(context.Background())
	pruneCtx, cancelPrune := context.WithCancel(ctx)
	return &intentRouteHitLog{db: db, ch: make(chan IntentRouteHit, intentRouteHitBuffer), stop: make(chan struct{}), done: make(chan struct{}), ctx: ctx, cancel: cancel, pruneCtx: pruneCtx, cancelPrune: cancelPrune}
}

func (l *intentRouteHitLog) Start() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.started || l.stopped {
		return
	}
	l.started = true
	go l.run()
}

func (l *intentRouteHitLog) Stop(ctx context.Context) error {
	l.mu.Lock()
	if !l.stopped {
		l.stopped = true
		// Retention cleanup must not consume the shutdown budget needed by writes.
		l.cancelPrune()
		close(l.stop)
		if !l.started {
			close(l.done)
		}
	}
	l.mu.Unlock()
	select {
	case <-l.done:
		l.cancel()
		return nil
	case <-ctx.Done():
		l.cancel()
		return ctx.Err()
	}
}

func (l *intentRouteHitLog) drop(n int, reason string) {
	if n == 0 {
		return
	}
	total := l.dropped.Add(int64(n))
	if total == int64(n) || total/1000 != (total-int64(n))/1000 {
		slog.Warn("intent_route.hit_dropped", "dropped", total, "reason", reason)
	}
}

func (l *intentRouteHitLog) add(hit IntentRouteHit) {
	l.Start()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		l.drop(1, "stopped")
		return
	}
	select {
	case l.ch <- hit:
	default:
		l.drop(1, "buffer_full")
	}
}

func (l *intentRouteHitLog) run() {
	defer close(l.done)
	defer l.cancel()
	flush := time.NewTicker(intentRouteHitFlushEvery)
	defer flush.Stop()
	prune := time.NewTicker(intentRouteHitPruneEvery)
	defer prune.Stop()
	l.prune()
	batch := make([]IntentRouteHit, 0, intentRouteHitBatch)
	appendHit := func(hit IntentRouteHit) {
		batch = append(batch, hit)
		if len(batch) >= intentRouteHitBatch {
			l.write(batch)
			batch = batch[:0]
		}
	}
	for {
		select {
		case <-l.stop:
			// Admission is already closed under mu, so this queue only shrinks.
			for {
				select {
				case hit := <-l.ch:
					appendHit(hit)
				default:
					if len(batch) > 0 {
						l.write(batch)
					}
					return
				}
			}
		case hit := <-l.ch:
			appendHit(hit)
		case <-flush.C:
			if len(batch) > 0 {
				l.write(batch)
				batch = batch[:0]
			}
		case <-prune.C:
			l.prune()
		}
	}
}

// Stop flushes the keyword log before the application closes its database.
func (s *IntentRouterService) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if log, ok := s.hits.(*intentRouteHitLog); ok {
		return log.Stop(ctx)
	}
	return nil
}

const intentRouteHitColumns = 13

func (l *intentRouteHitLog) write(batch []IntentRouteHit) {
	if len(batch) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(l.ctx, 10*time.Second)
	defer cancel()
	failed, err := l.persist(ctx, batch)
	if failed > 0 {
		l.drop(failed, "write_failed")
		slog.Warn("intent_route.hit_write_failed", "rows", failed, "error", err)
	}
}

// Only retry SQLSTATEs that establish the statement did not commit. In
// particular a broken connection after sending INSERT has an unknown outcome.
func retryableIntentHitError(err error) bool {
	var pg interface{ SQLState() string }
	if !errors.As(err, &pg) {
		return false
	}
	code := pg.SQLState()
	return code == "40001" || code == "40P01" || strings.HasPrefix(code, "53") || code == "55P03" || code == "57P03"
}
func invalidIntentHitError(err error) bool {
	var pg interface{ SQLState() string }
	return errors.As(err, &pg) && (strings.HasPrefix(pg.SQLState(), "22") || strings.HasPrefix(pg.SQLState(), "23"))
}

func (l *intentRouteHitLog) persist(ctx context.Context, batch []IntentRouteHit) (int, error) {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = ctx.Err(); err != nil {
			break
		}
		err = l.insert(ctx, batch)
		if err == nil {
			return 0, nil
		}
		if attempt == 2 || !retryableIntentHitError(err) {
			break
		}
		timer := time.NewTimer(time.Duration(1<<attempt) * 100 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return len(batch), ctx.Err()
		}
	}
	if len(batch) > 1 && invalidIntentHitError(err) && ctx.Err() == nil {
		mid := len(batch) / 2
		left, leftErr := l.persist(ctx, batch[:mid])
		right, rightErr := l.persist(ctx, batch[mid:])
		if leftErr != nil {
			return left + right, leftErr
		}
		return left + right, rightErr
	}
	return len(batch), err
}

func intentHitDBText(text string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(text, "�"), "\x00", "�")
}

func (l *intentRouteHitLog) insert(ctx context.Context, batch []IntentRouteHit) error {
	var b strings.Builder
	b.WriteString(`INSERT INTO intent_route_hits (created_at, group_id, user_id, api_key_id, request_id, endpoint, model, rule_name, keyword, user_turn, message_chars, newest_chars, excerpt) VALUES `)
	args := make([]any, 0, len(batch)*intentRouteHitColumns)
	for i, h := range batch {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteByte('(')
		for j := 1; j <= intentRouteHitColumns; j++ {
			if j > 1 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "$%d", i*intentRouteHitColumns+j)
		}
		b.WriteByte(')')
		args = append(args, h.Time, h.GroupID, h.UserID, h.APIKeyID, truncateRunes(intentHitDBText(h.RequestID), 160),
			truncateRunes(intentHitDBText(h.Endpoint), 128), truncateRunes(intentHitDBText(h.Model), 255), truncateRunes(intentHitDBText(h.Rule), 64),
			intentHitDBText(h.Keyword), h.UserTurn, h.MessageChars, h.NewestChars, intentHitDBText(h.Excerpt))
	}
	_, err := l.db.ExecContext(ctx, b.String(), args...)
	return err
}

func (l *intentRouteHitLog) prune() {
	ctx, cancel := context.WithTimeout(l.pruneCtx, time.Minute)
	defer cancel()
	cutoff := time.Now().Add(-intentRouteHitRetention)
	if _, err := l.db.ExecContext(ctx, `DELETE FROM intent_route_hits WHERE created_at < $1`, cutoff); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("intent_route.hit_prune_failed", "error", err)
	}
}

// intentHitExcerpt cuts the part of text around keyword, matched the way
// matchKeywords matches it (case-insensitively), from the original text.
func intentHitExcerpt(text, keyword string, radius int) string {
	runes := []rune(text)
	lower := make([]rune, len(runes))
	for i, r := range runes {
		lower[i] = unicode.ToLower(r)
	}
	needle := []rune(strings.ToLower(strings.TrimSpace(keyword)))
	at := indexRunes(lower, needle)
	if at < 0 {
		at, needle = 0, nil
	}
	start, end := max(0, at-radius), min(len(runes), at+len(needle)+radius)
	var b strings.Builder
	if start > 0 {
		b.WriteString("…")
	}
	b.WriteString(string(runes[start:end]))
	if end < len(runes) {
		b.WriteString("…")
	}
	return b.String()
}

func indexRunes(haystack, needle []rune) int {
	if len(needle) == 0 {
		return -1
	}
outer:
	for i := 0; i+len(needle) <= len(haystack); i++ {
		for j, r := range needle {
			if haystack[i+j] != r {
				continue outer
			}
		}
		return i
	}
	return -1
}

// ---- reading ----

// The usage log is joined per hit on its unique (request_id, api_key_id) index.
const intentRouteHitUsageJoin = `LEFT JOIN LATERAL (
	SELECT ul.account_id, ul.input_tokens + ul.cache_read_tokens + ul.cache_creation_tokens AS input_tokens, ul.total_cost
	FROM usage_logs ul
	WHERE h.request_id <> '' AND ul.request_id = h.request_id AND ul.api_key_id = h.api_key_id
	LIMIT 1
) u ON TRUE`

func intentRouteHitWhere(q IntentRouteHitQuery, now time.Time) (string, []any) {
	hours := q.Hours
	if hours <= 0 {
		hours = 24
	}
	hours = min(hours, intentRouteHitMaxHours)
	conds := []string{"h.created_at >= $1"}
	args := []any{now.Add(-time.Duration(hours) * time.Hour)}
	if q.GroupID > 0 {
		args = append(args, q.GroupID)
		conds = append(conds, fmt.Sprintf("h.group_id = $%d", len(args)))
	}
	if q.UserID > 0 {
		args = append(args, q.UserID)
		conds = append(conds, fmt.Sprintf("h.user_id = $%d", len(args)))
	}
	if q.Keyword != "" {
		args = append(args, q.Keyword)
		conds = append(conds, fmt.Sprintf("h.keyword = $%d", len(args)))
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}

// ListHits returns keyword hits, newest first, with the usage of each request.
func (s *IntentRouterService) ListHits(ctx context.Context, q IntentRouteHitQuery) ([]IntentRouteHit, int64, error) {
	if s.entClient == nil {
		return []IntentRouteHit{}, 0, nil
	}
	where, args := intentRouteHitWhere(q, s.now())
	var total int64
	rows, err := s.entClient.QueryContext(ctx, `SELECT COUNT(*) FROM intent_route_hits h `+where, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("count intent route hits: %w", err)
	}
	if rows.Next() {
		err = rows.Scan(&total)
	}
	_ = rows.Close()
	if err != nil {
		return nil, 0, fmt.Errorf("count intent route hits: %w", err)
	}

	size := q.PageSize
	if size <= 0 {
		size = 50
	}
	size = min(size, intentRouteHitMaxPage)
	page := max(q.Page, 1)
	args = append(args, size, (page-1)*size)
	rows, err = s.entClient.QueryContext(ctx, `SELECT h.id, h.created_at, h.group_id, h.user_id, h.api_key_id, h.request_id,
		h.endpoint, h.model, h.rule_name, h.keyword, h.user_turn, h.message_chars, h.newest_chars, h.excerpt,
		u.account_id, u.input_tokens, u.total_cost
		FROM intent_route_hits h `+intentRouteHitUsageJoin+` `+where+
		fmt.Sprintf(` ORDER BY h.id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list intent route hits: %w", err)
	}
	defer func() { _ = rows.Close() }()
	hits := []IntentRouteHit{}
	for rows.Next() {
		var h IntentRouteHit
		var account, tokens sql.NullInt64
		var cost sql.NullFloat64
		if err := rows.Scan(&h.ID, &h.Time, &h.GroupID, &h.UserID, &h.APIKeyID, &h.RequestID,
			&h.Endpoint, &h.Model, &h.Rule, &h.Keyword, &h.UserTurn, &h.MessageChars, &h.NewestChars, &h.Excerpt,
			&account, &tokens, &cost); err != nil {
			return nil, 0, fmt.Errorf("scan intent route hit: %w", err)
		}
		if account.Valid {
			h.AccountID = &account.Int64
		}
		if tokens.Valid {
			h.InputTokens = &tokens.Int64
		}
		if cost.Valid {
			h.TotalCost = &cost.Float64
		}
		hits = append(hits, h)
	}
	return hits, total, rows.Err()
}

// KeywordHitStats sums hits per keyword, busiest first.
func (s *IntentRouterService) KeywordHitStats(ctx context.Context, q IntentRouteHitQuery) ([]IntentRouteKeywordStat, error) {
	if s.entClient == nil {
		return []IntentRouteKeywordStat{}, nil
	}
	where, args := intentRouteHitWhere(q, s.now())
	rows, err := s.entClient.QueryContext(ctx, `SELECT h.keyword, COUNT(*), COUNT(DISTINCT h.user_id), COUNT(DISTINCT h.api_key_id),
		COALESCE(AVG(h.message_chars), 0)::BIGINT, COALESCE(AVG(u.input_tokens), 0)::BIGINT,
		COALESCE(SUM(u.total_cost), 0)::FLOAT8, MAX(h.created_at)
		FROM intent_route_hits h `+intentRouteHitUsageJoin+` `+where+`
		GROUP BY h.keyword ORDER BY COUNT(*) DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("intent route keyword stats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	stats := []IntentRouteKeywordStat{}
	for rows.Next() {
		var st IntentRouteKeywordStat
		if err := rows.Scan(&st.Keyword, &st.Hits, &st.Users, &st.APIKeys, &st.AvgChars, &st.AvgInputTokens, &st.TotalCost, &st.LastHit); err != nil {
			return nil, fmt.Errorf("scan intent route keyword stat: %w", err)
		}
		stats = append(stats, st)
	}
	return stats, rows.Err()
}
