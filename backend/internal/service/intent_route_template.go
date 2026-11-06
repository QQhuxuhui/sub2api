package service

import (
	"container/list"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Doubtful Jev verdicts are grouped by message template for an admin to judge;
// a judged template then decides every later request with that template, in
// every group, without asking Jev. A template is the latest user message with
// digits and random-looking tokens replaced by placeholders, so a probe that
// carries a fresh nonce on every request is still one template.

const (
	intentTemplateVersion = "v1"

	IntentTemplatePending = "pending"
	IntentTemplateProbe   = "probe"
	IntentTemplateHealth  = "health"
	IntentTemplateNormal  = "normal"

	// Reason A: Jev called it a health check, yet the probe score is high and
	// clearly above the health score.
	intentTemplateHighProbe   = 0.9
	intentTemplateProbeMargin = 0.1
	// Scores like 0.9-0.8 come out just under 0.1 in float64.
	intentTemplateScoreEpsilon = 1e-9
	// Reason C: probe score just under the routing threshold.
	intentTemplateNearMissFloor = 0.6

	intentTemplateLRUSize     = 100_000
	intentTemplateFlipReemit  = 10 * time.Minute
	intentTemplateMaxIDs      = 10
	intentTemplateBuffer      = 4096
	intentTemplateBatch       = 200
	intentTemplateFlushEvery  = 2 * time.Second
	intentTemplatePruneEvery  = time.Hour
	intentTemplateRetention   = 30 * 24 * time.Hour
	intentTemplatePruneChunk  = 1000
	intentTemplatePruneRounds = 100
	intentTemplateMaxPageSize = 100
)

// Jev conclusions, as bits of the flip detector.
const (
	intentJevConclProbe  uint8 = 1
	intentJevConclHealth uint8 = 2
	intentJevConclLow    uint8 = 4
)

// Final outcome of a judged request, for the per-template counters.
const (
	intentTemplateFinalRoute  = "route"
	intentTemplateFinalHealth = "health"
	intentTemplateFinalNormal = "normal"
)

func validIntentTemplateDecision(status string) bool {
	return status == IntentTemplateProbe || status == IntentTemplateHealth || status == IntentTemplateNormal
}

// ---- normalization ----

// intentTemplateOf returns the template of a message and its key. text must
// already be cut to what Jev sees (intentJevMaxTextRunes).
func intentTemplateOf(text string) (key, template string) {
	template = normalizeIntentTemplate(text)
	sum := sha256.Sum256([]byte(intentTemplateVersion + "\x00" + template))
	return hex.EncodeToString(sum[:]), template
}

func isIntentTemplateQuote(r rune) bool {
	switch r {
	case '"', '\'', '“', '”', '‘', '’', '`':
		return true
	}
	return false
}

func isASCIIAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

func normalizeIntentTemplate(text string) string {
	runes := []rune(strings.Join(strings.Fields(strings.ToLower(text)), " "))
	var b strings.Builder
	b.Grow(len(runes))
	for i := 0; i < len(runes); {
		if !isASCIIAlnum(runes[i]) {
			b.WriteRune(runes[i])
			i++
			continue
		}
		j := i
		for j < len(runes) && isASCIIAlnum(runes[j]) {
			j++
		}
		token := runes[i:j]
		quoted := i > 0 && j < len(runes) && isIntentTemplateQuote(runes[i-1]) && isIntentTemplateQuote(runes[j])
		if randomIntentToken(token, quoted) {
			b.WriteString("<rand>")
		} else {
			for k := 0; k < len(token); {
				if token[k] >= '0' && token[k] <= '9' {
					for k < len(token) && token[k] >= '0' && token[k] <= '9' {
						k++
					}
					b.WriteString("<num>")
					continue
				}
				b.WriteRune(token[k])
				k++
			}
		}
		i = j
	}
	return b.String()
}

// randomIntentToken: a long hex string, a long string mixing letters and
// digits, or a short quoted value with at least two of each (a code like 7Q2K).
func randomIntentToken(token []rune, quoted bool) bool {
	letters, digits, hexOnly := 0, 0, true
	for _, r := range token {
		if r >= '0' && r <= '9' {
			digits++
		} else {
			letters++
			if r > 'f' {
				hexOnly = false
			}
		}
	}
	n := len(token)
	if n >= 8 && (hexOnly || (letters > 0 && digits > 0)) {
		return true
	}
	return quoted && n >= 4 && n <= 7 && letters >= 2 && digits >= 2
}

// ---- flip detection (reason B) ----

type intentTemplateFlipEntry struct {
	key      string
	bits     uint8
	lastEmit time.Time
	retry    bool
}

// intentTemplateFlips remembers, per template, which Jev conclusions were seen
// on this instance. It is bounded; the least recently used template goes.
type intentTemplateFlips struct {
	mu    sync.Mutex
	size  int
	order *list.List
	items map[string]*list.Element
}

func newIntentTemplateFlips(size int) *intentTemplateFlips {
	return &intentTemplateFlips{size: size, order: list.New(), items: make(map[string]*list.Element)}
}

// see records a conclusion and reports whether a reason-B observation is due:
// the template has been judged two ways, and the conclusion is new, the last
// observation was lost, or the last one was sent long enough ago.
func (f *intentTemplateFlips) see(key string, bit uint8, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	el, ok := f.items[key]
	if !ok {
		el = f.order.PushFront(&intentTemplateFlipEntry{key: key})
		f.items[key] = el
		for f.order.Len() > f.size {
			last := f.order.Back()
			f.order.Remove(last)
			delete(f.items, last.Value.(*intentTemplateFlipEntry).key)
		}
	} else {
		f.order.MoveToFront(el)
	}
	e := el.Value.(*intentTemplateFlipEntry)
	fresh := e.bits&bit == 0
	e.bits |= bit
	if e.bits&(e.bits-1) == 0 { // one conclusion so far
		return false
	}
	return fresh || e.retry || now.Sub(e.lastEmit) >= intentTemplateFlipReemit
}

// sent records whether the due observation made it into the write queue.
func (f *intentTemplateFlips) sent(key string, ok bool, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	el, found := f.items[key]
	if !found {
		return
	}
	e := el.Value.(*intentTemplateFlipEntry)
	if ok {
		e.lastEmit, e.retry = now, false
	} else {
		e.retry = true
	}
}

// ---- observations ----

// intentTemplateObservation is one doubtful verdict.
type intentTemplateObservation struct {
	Key       string
	Template  string
	Sample    string
	TextChars int
	Time      time.Time
	GroupID   int64
	UserID    int64
	ReasonA   bool
	ReasonB   bool
	ReasonC   bool
	Final     string
	Probe     float64
	Health    *float64
}

// intentTemplateAgg is what one write batch adds to a template row.
type intentTemplateAgg struct {
	Key, Template, Sample string
	TextChars             int
	First, Last           time.Time
	Seen                  int64
	A, B, C               int64
	Route, Health, Normal int64
	ProbeMin, ProbeMax    float64
	HealthMin, HealthMax  *float64
	UserIDs, GroupIDs     []int64 // newest first, distinct, at most intentTemplateMaxIDs
}

func pushRecentID(ids []int64, id int64) []int64 {
	if id <= 0 {
		return ids
	}
	out := make([]int64, 0, intentTemplateMaxIDs)
	out = append(out, id)
	for _, v := range ids {
		if v != id && len(out) < intentTemplateMaxIDs {
			out = append(out, v)
		}
	}
	return out
}

// mergeIntentTemplateObservations folds a batch into one aggregate per key, so
// one INSERT never touches a row twice.
func mergeIntentTemplateObservations(batch []intentTemplateObservation) []intentTemplateAgg {
	index := make(map[string]int, len(batch))
	aggs := make([]intentTemplateAgg, 0, len(batch))
	for _, o := range batch {
		i, ok := index[o.Key]
		if !ok {
			i = len(aggs)
			index[o.Key] = i
			aggs = append(aggs, intentTemplateAgg{Key: o.Key, First: o.Time, ProbeMin: o.Probe, ProbeMax: o.Probe})
		}
		a := &aggs[i]
		a.Template, a.Sample, a.TextChars = o.Template, o.Sample, o.TextChars
		if o.Time.Before(a.First) {
			a.First = o.Time
		}
		if o.Time.After(a.Last) {
			a.Last = o.Time
		}
		a.Seen++
		if o.ReasonA {
			a.A++
		}
		if o.ReasonB {
			a.B++
		}
		if o.ReasonC {
			a.C++
		}
		switch o.Final {
		case intentTemplateFinalRoute:
			a.Route++
		case intentTemplateFinalHealth:
			a.Health++
		default:
			a.Normal++
		}
		if o.Probe < a.ProbeMin {
			a.ProbeMin = o.Probe
		}
		if o.Probe > a.ProbeMax {
			a.ProbeMax = o.Probe
		}
		if o.Health != nil {
			h := *o.Health
			if a.HealthMin == nil || h < *a.HealthMin {
				a.HealthMin = &h
			}
			if a.HealthMax == nil || h > *a.HealthMax {
				hh := h
				a.HealthMax = &hh
			}
		}
		a.UserIDs = pushRecentID(a.UserIDs, o.UserID)
		a.GroupIDs = pushRecentID(a.GroupIDs, o.GroupID)
	}
	return aggs
}

// ---- storage ----

// IntentRouteTemplate is one template row as the admin sees it.
type IntentRouteTemplate struct {
	Key           string     `json:"key"`
	Template      string     `json:"template"`
	Sample        string     `json:"sample"`
	TextChars     int        `json:"text_chars"`
	Status        string     `json:"status"`
	FirstSeenAt   time.Time  `json:"first_seen_at"`
	LastSeenAt    time.Time  `json:"last_seen_at"`
	SeenCount     int64      `json:"seen_count"`
	ReasonA       int64      `json:"reason_a"`
	ReasonB       int64      `json:"reason_b"`
	ReasonC       int64      `json:"reason_c"`
	VerdictRoute  int64      `json:"verdict_route"`
	VerdictHealth int64      `json:"verdict_health"`
	VerdictNormal int64      `json:"verdict_normal"`
	ProbeMin      *float64   `json:"probe_min"`
	ProbeMax      *float64   `json:"probe_max"`
	HealthMin     *float64   `json:"health_min"`
	HealthMax     *float64   `json:"health_max"`
	UserIDs       []int64    `json:"user_ids"`
	GroupIDs      []int64    `json:"group_ids"`
	DecidedAt     *time.Time `json:"decided_at"`
	DecidedBy     *int64     `json:"decided_by"`
	Revision      int64      `json:"revision"`
}

// IntentRouteTemplateQuery filters the admin list.
type IntentRouteTemplateQuery struct {
	Status   string // pending (default) | probe | health | normal | all
	Reason   string // "" | a | b | c
	Search   string
	Page     int
	PageSize int
}

type intentTemplateStore interface {
	upsert(ctx context.Context, aggs []intentTemplateAgg) error
	decided(ctx context.Context) (map[string]string, error)
	list(ctx context.Context, q IntentRouteTemplateQuery) ([]IntentRouteTemplate, int64, error)
	decide(ctx context.Context, key, status string, revision, by int64, now time.Time) (*IntentRouteTemplate, error)
	revoke(ctx context.Context, key string, revision int64) (*IntentRouteTemplate, error)
	prune(ctx context.Context, now time.Time) error
}

var (
	errIntentTemplateNotFound = infraerrors.NotFound("INTENT_TEMPLATE_NOT_FOUND", "template not found")
	errIntentTemplateConflict = infraerrors.Conflict("INTENT_TEMPLATE_CONFLICT", "the template was changed by someone else; reload and try again")
)

type pgIntentTemplateStore struct{ db intentRouteHitDB }

func pgInt64Array(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func parsePGInt64Array(raw string) []int64 {
	raw = strings.Trim(raw, "{}")
	if raw == "" {
		return []int64{}
	}
	out := make([]int64, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		if v, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil {
			out = append(out, v)
		}
	}
	return out
}

func nullFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

const intentTemplateUpsertColumns = 20

// Newest ids first, distinct, at most 10: the batch's ids come before the row's.
const intentTemplateMergeIDs = `(SELECT COALESCE(array_agg(id ORDER BY ord), '{}') FROM (SELECT id, min(ord) AS ord FROM unnest(EXCLUDED.%[1]s || intent_route_templates.%[1]s) WITH ORDINALITY AS t(id, ord) GROUP BY id ORDER BY min(ord) LIMIT 10) s)`

func (p *pgIntentTemplateStore) upsert(ctx context.Context, aggs []intentTemplateAgg) error {
	if len(aggs) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(`INSERT INTO intent_route_templates (template_key, template, sample, text_chars, first_seen_at, last_seen_at, seen_count, reason_a, reason_b, reason_c, verdict_route, verdict_health, verdict_normal, probe_min, probe_max, health_min, health_max, user_ids, group_ids, status) VALUES `)
	args := make([]any, 0, len(aggs)*intentTemplateUpsertColumns)
	for i, a := range aggs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteByte('(')
		for j := 1; j <= intentTemplateUpsertColumns; j++ {
			if j > 1 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "$%d", i*intentTemplateUpsertColumns+j)
			if j == 18 || j == 19 {
				b.WriteString("::bigint[]")
			}
		}
		b.WriteByte(')')
		args = append(args, a.Key, intentHitDBText(a.Template), intentHitDBText(a.Sample), a.TextChars, a.First, a.Last, a.Seen,
			a.A, a.B, a.C, a.Route, a.Health, a.Normal, a.ProbeMin, a.ProbeMax, nullFloat(a.HealthMin), nullFloat(a.HealthMax),
			pgInt64Array(a.UserIDs), pgInt64Array(a.GroupIDs), IntentTemplatePending)
	}
	b.WriteString(` ON CONFLICT (template_key) DO UPDATE SET
		template = EXCLUDED.template,
		sample = EXCLUDED.sample,
		text_chars = EXCLUDED.text_chars,
		first_seen_at = LEAST(intent_route_templates.first_seen_at, EXCLUDED.first_seen_at),
		last_seen_at = GREATEST(intent_route_templates.last_seen_at, EXCLUDED.last_seen_at),
		seen_count = intent_route_templates.seen_count + EXCLUDED.seen_count,
		reason_a = intent_route_templates.reason_a + EXCLUDED.reason_a,
		reason_b = intent_route_templates.reason_b + EXCLUDED.reason_b,
		reason_c = intent_route_templates.reason_c + EXCLUDED.reason_c,
		verdict_route = intent_route_templates.verdict_route + EXCLUDED.verdict_route,
		verdict_health = intent_route_templates.verdict_health + EXCLUDED.verdict_health,
		verdict_normal = intent_route_templates.verdict_normal + EXCLUDED.verdict_normal,
		probe_min = LEAST(intent_route_templates.probe_min, EXCLUDED.probe_min),
		probe_max = GREATEST(intent_route_templates.probe_max, EXCLUDED.probe_max),
		health_min = LEAST(intent_route_templates.health_min, EXCLUDED.health_min),
		health_max = GREATEST(intent_route_templates.health_max, EXCLUDED.health_max),
		user_ids = ` + fmt.Sprintf(intentTemplateMergeIDs, "user_ids") + `,
		group_ids = ` + fmt.Sprintf(intentTemplateMergeIDs, "group_ids"))
	_, err := p.db.ExecContext(ctx, b.String(), args...)
	return err
}

func (p *pgIntentTemplateStore) decided(ctx context.Context) (map[string]string, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT template_key, status FROM intent_route_templates WHERE status IN ('probe', 'health', 'normal')`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]string)
	for rows.Next() {
		var key, status string
		if err := rows.Scan(&key, &status); err != nil {
			return nil, err
		}
		out[strings.TrimSpace(key)] = status
	}
	return out, rows.Err()
}

const intentTemplateSelect = `SELECT template_key, template, sample, text_chars, status, first_seen_at, last_seen_at, seen_count, reason_a, reason_b, reason_c, verdict_route, verdict_health, verdict_normal, probe_min, probe_max, health_min, health_max, user_ids::text, group_ids::text, decided_at, decided_by, revision FROM intent_route_templates`

type intentTemplateScanner interface{ Scan(dest ...any) error }

func scanIntentTemplate(row intentTemplateScanner) (*IntentRouteTemplate, error) {
	var t IntentRouteTemplate
	var probeMin, probeMax, healthMin, healthMax sql.NullFloat64
	var users, groups string
	var decidedAt sql.NullTime
	var decidedBy sql.NullInt64
	if err := row.Scan(&t.Key, &t.Template, &t.Sample, &t.TextChars, &t.Status, &t.FirstSeenAt, &t.LastSeenAt, &t.SeenCount,
		&t.ReasonA, &t.ReasonB, &t.ReasonC, &t.VerdictRoute, &t.VerdictHealth, &t.VerdictNormal,
		&probeMin, &probeMax, &healthMin, &healthMax, &users, &groups, &decidedAt, &decidedBy, &t.Revision); err != nil {
		return nil, err
	}
	t.Key = strings.TrimSpace(t.Key)
	fl := func(v sql.NullFloat64) *float64 {
		if !v.Valid {
			return nil
		}
		f := v.Float64
		return &f
	}
	t.ProbeMin, t.ProbeMax, t.HealthMin, t.HealthMax = fl(probeMin), fl(probeMax), fl(healthMin), fl(healthMax)
	t.UserIDs, t.GroupIDs = parsePGInt64Array(users), parsePGInt64Array(groups)
	if decidedAt.Valid {
		at := decidedAt.Time
		t.DecidedAt = &at
	}
	if decidedBy.Valid {
		by := decidedBy.Int64
		t.DecidedBy = &by
	}
	return &t, nil
}

func intentTemplateWhere(q IntentRouteTemplateQuery) (string, []any) {
	var conds []string
	var args []any
	switch q.Status {
	case "all":
	case IntentTemplateProbe, IntentTemplateHealth, IntentTemplateNormal:
		args = append(args, q.Status)
		conds = append(conds, fmt.Sprintf("status = $%d", len(args)))
	default:
		conds = append(conds, "status = 'pending'")
	}
	switch q.Reason {
	case "a":
		conds = append(conds, "reason_a > 0")
	case "b":
		conds = append(conds, "reason_b > 0")
	case "c":
		conds = append(conds, "reason_c > 0")
	}
	if s := strings.TrimSpace(q.Search); s != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s) + "%"
		args = append(args, like)
		conds = append(conds, fmt.Sprintf("(template ILIKE $%[1]d OR sample ILIKE $%[1]d)", len(args)))
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func (p *pgIntentTemplateStore) list(ctx context.Context, q IntentRouteTemplateQuery) ([]IntentRouteTemplate, int64, error) {
	where, args := intentTemplateWhere(q)
	var total int64
	countRows, err := p.db.QueryContext(ctx, `SELECT COUNT(*) FROM intent_route_templates`+where, args...)
	if err != nil {
		return nil, 0, err
	}
	if countRows.Next() {
		if err := countRows.Scan(&total); err != nil {
			_ = countRows.Close()
			return nil, 0, err
		}
	}
	_ = countRows.Close()
	limit, offset := q.PageSize, (q.Page-1)*q.PageSize
	args = append(args, limit, offset)
	rows, err := p.db.QueryContext(ctx, intentTemplateSelect+where+fmt.Sprintf(" ORDER BY seen_count DESC, last_seen_at DESC, template_key LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]IntentRouteTemplate, 0, limit)
	for rows.Next() {
		t, err := scanIntentTemplate(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *t)
	}
	return items, total, rows.Err()
}

// changeIntentTemplate runs a revision-guarded UPDATE ... RETURNING and tells a
// missing row from a stale revision.
func (p *pgIntentTemplateStore) changeIntentTemplate(ctx context.Context, query string, args ...any) (*IntentRouteTemplate, error) {
	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var updated *IntentRouteTemplate
	if rows.Next() {
		updated, err = scanIntentTemplate(rows)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if updated != nil {
		return updated, nil
	}
	exists, err := p.db.QueryContext(ctx, `SELECT 1 FROM intent_route_templates WHERE template_key = $1`, args[0])
	if err != nil {
		return nil, err
	}
	defer func() { _ = exists.Close() }()
	if exists.Next() {
		return nil, errIntentTemplateConflict
	}
	return nil, errIntentTemplateNotFound
}

const intentTemplateReturning = ` RETURNING template_key, template, sample, text_chars, status, first_seen_at, last_seen_at, seen_count, reason_a, reason_b, reason_c, verdict_route, verdict_health, verdict_normal, probe_min, probe_max, health_min, health_max, user_ids::text, group_ids::text, decided_at, decided_by, revision`

func (p *pgIntentTemplateStore) decide(ctx context.Context, key, status string, revision, by int64, now time.Time) (*IntentRouteTemplate, error) {
	var decidedBy any
	if by > 0 {
		decidedBy = by
	}
	return p.changeIntentTemplate(ctx, `UPDATE intent_route_templates SET status = $2, decided_at = $3, decided_by = $4, revision = revision + 1 WHERE template_key = $1 AND revision = $5`+intentTemplateReturning,
		key, status, now, decidedBy, revision)
}

func (p *pgIntentTemplateStore) revoke(ctx context.Context, key string, revision int64) (*IntentRouteTemplate, error) {
	return p.changeIntentTemplate(ctx, `UPDATE intent_route_templates SET status = 'pending', decided_at = NULL, decided_by = NULL, revision = revision + 1 WHERE template_key = $1 AND revision = $2`+intentTemplateReturning,
		key, revision)
}

func (p *pgIntentTemplateStore) prune(ctx context.Context, now time.Time) error {
	cutoff := now.Add(-intentTemplateRetention)
	for i := 0; i < intentTemplatePruneRounds; i++ {
		// The outer conditions repeat the subquery's: a row judged or seen again
		// while the DELETE waited for its lock is re-checked and kept.
		res, err := p.db.ExecContext(ctx, `DELETE FROM intent_route_templates WHERE template_key IN (SELECT template_key FROM intent_route_templates WHERE status = 'pending' AND last_seen_at < $1 LIMIT $2) AND status = 'pending' AND last_seen_at < $1`, cutoff, intentTemplatePruneChunk)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n < intentTemplatePruneChunk {
			break
		}
	}
	// Judged templates keep deciding; only the request text they were seen with goes.
	_, err := p.db.ExecContext(ctx, `UPDATE intent_route_templates SET sample = '', user_ids = '{}', group_ids = '{}' WHERE status <> 'pending' AND last_seen_at < $1 AND (sample <> '' OR cardinality(user_ids) > 0 OR cardinality(group_ids) > 0)`, cutoff)
	return err
}

// ---- write queue ----

// intentTemplateLog buffers observations off the request path and writes them
// in merged batches; it also prunes. Stop drains what was admitted.
type intentTemplateLog struct {
	store            intentTemplateStore
	ch               chan intentTemplateObservation
	mu               sync.Mutex
	started, stopped bool
	stop, done       chan struct{}
	ctx              context.Context
	cancel           context.CancelFunc
	// Pruning has its own context so Stop can end a prune stuck on a lock
	// and leave the shutdown budget to draining the queue.
	pruneCtx    context.Context
	pruneCancel context.CancelFunc
	dropped     atomic.Int64
	now         func() time.Time
}

func newIntentTemplateLog(store intentTemplateStore) *intentTemplateLog {
	ctx, cancel := context.WithCancel(context.Background())
	pruneCtx, pruneCancel := context.WithCancel(ctx)
	return &intentTemplateLog{store: store, ch: make(chan intentTemplateObservation, intentTemplateBuffer), stop: make(chan struct{}), done: make(chan struct{}), ctx: ctx, cancel: cancel, pruneCtx: pruneCtx, pruneCancel: pruneCancel, now: time.Now}
}

func (l *intentTemplateLog) Start() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.started || l.stopped {
		return
	}
	l.started = true
	go l.run()
}

// add reports whether the observation was queued.
func (l *intentTemplateLog) add(o intentTemplateObservation) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return false
	}
	select {
	case l.ch <- o:
		return true
	default:
		if n := l.dropped.Add(1); n == 1 || n%1000 == 0 {
			slog.Warn("intent_route.template_observation_dropped", "reason", "buffer_full", "total", n)
		}
		return false
	}
}

func (l *intentTemplateLog) Stop(ctx context.Context) error {
	l.mu.Lock()
	if l.stopped {
		l.mu.Unlock()
		return nil
	}
	l.stopped = true
	started := l.started
	l.mu.Unlock()
	if !started {
		l.cancel()
		return nil
	}
	l.pruneCancel()
	close(l.stop)
	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		l.cancel()
		return ctx.Err()
	}
}

func (l *intentTemplateLog) run() {
	defer close(l.done)
	defer l.cancel()
	flush := time.NewTicker(intentTemplateFlushEvery)
	defer flush.Stop()
	prune := time.NewTicker(intentTemplatePruneEvery)
	defer prune.Stop()
	l.prune()
	batch := make([]intentTemplateObservation, 0, intentTemplateBatch)
	push := func(o intentTemplateObservation) {
		batch = append(batch, o)
		if len(batch) >= intentTemplateBatch {
			l.write(batch)
			batch = batch[:0]
		}
	}
	for {
		select {
		case <-l.stop:
			for {
				select {
				case o := <-l.ch:
					push(o)
				default:
					if len(batch) > 0 {
						l.write(batch)
					}
					return
				}
			}
		case o := <-l.ch:
			push(o)
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

func (l *intentTemplateLog) write(batch []intentTemplateObservation) {
	aggs := mergeIntentTemplateObservations(batch)
	ctx, cancel := context.WithTimeout(l.ctx, 10*time.Second)
	defer cancel()
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = l.store.upsert(ctx, aggs); err == nil || !retryableIntentHitError(err) {
			break
		}
		timer := time.NewTimer(time.Duration(1<<attempt) * 100 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
		}
	}
	if err != nil {
		l.dropped.Add(int64(len(batch)))
		slog.Warn("intent_route.template_write_failed", "observations", len(batch), "templates", len(aggs), "error", err)
	}
}

func (l *intentTemplateLog) prune() {
	ctx, cancel := context.WithTimeout(l.pruneCtx, time.Minute)
	defer cancel()
	if err := l.store.prune(ctx, l.now()); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("intent_route.template_prune_failed", "error", err)
	}
}

// ---- service side ----

type intentTemplateSink interface {
	add(o intentTemplateObservation) bool
}

// intentManualVerdict is what an admin judgement says about this request.
type intentManualVerdict struct {
	Label string
	Key   string
}

// manualLabel looks the text up in the judged templates of the snapshot.
func (s *IntentRouterService) manualLabel(labels map[string]string, text string) intentManualVerdict {
	if len(labels) == 0 || text == "" {
		return intentManualVerdict{}
	}
	key, _ := intentTemplateOf(truncateRunes(text, intentJevMaxTextRunes))
	return intentManualVerdict{Label: labels[key], Key: key}
}

func (s *IntentRouterService) templateLabels() map[string]string {
	if p := s.templates.Load(); p != nil {
		return *p
	}
	return nil
}

// reloadTemplateLabels is called by reloadConfigs (configMu held). A failed
// load keeps the previous snapshot.
func (s *IntentRouterService) reloadTemplateLabels(ctx context.Context) {
	if s.templateStore == nil {
		return
	}
	labels, err := s.templateStore.decided(ctx)
	if err != nil {
		slog.Warn("intent_route.template_load_failed", "error", err)
		return
	}
	s.templates.Store(&labels)
}

// manualSnapshot returns the judged templates. With fresh, a snapshot never
// loaded or older than intentRouteConfigTTL is reloaded first (the admin
// test waits for it); without, the request path's lazy refresh applies.
func (s *IntentRouterService) manualSnapshot(ctx context.Context, fresh bool) (map[string]string, error) {
	if fresh && s.templateStore != nil && (s.templates.Load() == nil || s.now().UnixNano()-s.configLoaded.Load() > int64(intentRouteConfigTTL)) {
		_ = s.reloadConfigs(ctx)
		if s.templates.Load() == nil {
			return nil, infraerrors.InternalServer("INTENT_TEMPLATE_SNAPSHOT_UNAVAILABLE", "judged templates could not be loaded")
		}
	}
	return s.templateLabels(), nil
}

// intentJevObservation is what classify keeps of a Jev answer to file a
// doubtful verdict once the final decision is known.
type intentJevObservation struct {
	text   string
	p      float64
	health *float64
	concl  uint8
	cfg    *intentRouterConfig
}

// observeTemplate files a doubtful verdict, if this one is.
func (s *IntentRouterService) observeTemplate(in IntentRouteInput, obs *intentJevObservation, routed bool) {
	if obs == nil || s.templateSink == nil {
		return
	}
	// Runs after classify's own recover: it must not take the request down either.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("intent_route.template_observe_panic", "group_id", in.GroupID, "panic", r)
		}
	}()
	cfg := obs.cfg
	final := intentTemplateFinalNormal
	switch {
	case routed:
		final = intentTemplateFinalRoute
	case obs.concl == intentJevConclHealth:
		final = intentTemplateFinalHealth
	}
	reasonA := obs.health != nil && *obs.health >= cfg.Jev.HealthThreshold && obs.p >= intentTemplateHighProbe && obs.p-*obs.health >= intentTemplateProbeMargin-intentTemplateScoreEpsilon
	reasonC := final == intentTemplateFinalNormal && obs.p >= intentTemplateNearMissFloor && obs.p < cfg.Jev.Threshold
	cut := truncateRunes(obs.text, intentJevMaxTextRunes)
	key, template := intentTemplateOf(cut)
	now := s.now()
	reasonB := s.templateFlips != nil && s.templateFlips.see(key, obs.concl, now)
	if !reasonA && !reasonB && !reasonC {
		return
	}
	ok := s.templateSink.add(intentTemplateObservation{
		Key: key, Template: template, Sample: cut, TextChars: utf8.RuneCountInString(obs.text), Time: now,
		GroupID: in.GroupID, UserID: in.UserID, ReasonA: reasonA, ReasonB: reasonB, ReasonC: reasonC,
		Final: final, Probe: obs.p, Health: obs.health,
	})
	if reasonB {
		s.templateFlips.sent(key, ok, now)
	}
}

// manualTarget is the rule a template judged "probe" routes to: the Jev
// target rule, whether or not Jev itself is switched on.
func (c *intentRouterConfig) manualTarget() (domain.IntentRule, bool) {
	if c == nil || c.Jev.Rule == "" {
		return domain.IntentRule{}, false
	}
	return c.rule(c.Jev.Rule)
}

// ---- admin ----

func validIntentTemplateKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	for _, r := range key {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func (s *IntentRouterService) intentTemplateStoreOrErr() (intentTemplateStore, error) {
	if s.templateStore == nil {
		return nil, infraerrors.ServiceUnavailable("INTENT_TEMPLATE_UNAVAILABLE", "template judgements need the database")
	}
	return s.templateStore, nil
}

// ListTemplates lists templates for judging.
func (s *IntentRouterService) ListTemplates(ctx context.Context, q IntentRouteTemplateQuery) ([]IntentRouteTemplate, int64, error) {
	store, err := s.intentTemplateStoreOrErr()
	if err != nil {
		return nil, 0, err
	}
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 || q.PageSize > intentTemplateMaxPageSize {
		q.PageSize = 20
	}
	return store.list(ctx, q)
}

// DecideTemplate judges a template; it applies on this instance at once.
func (s *IntentRouterService) DecideTemplate(ctx context.Context, key, status string, revision, adminID int64) (*IntentRouteTemplate, error) {
	store, err := s.intentTemplateStoreOrErr()
	if err != nil {
		return nil, err
	}
	if !validIntentTemplateKey(key) {
		return nil, infraerrors.BadRequest("INTENT_TEMPLATE_KEY_INVALID", "template key must be 64 lowercase hex characters")
	}
	if !validIntentTemplateDecision(status) {
		return nil, infraerrors.BadRequest("INTENT_TEMPLATE_STATUS_INVALID", "status must be probe, health or normal")
	}
	t, err := store.decide(ctx, key, status, revision, adminID, s.now())
	if err != nil {
		return nil, err
	}
	s.InvalidateConfig(ctx)
	return t, nil
}

// RevokeTemplate returns a judged template to the queue.
func (s *IntentRouterService) RevokeTemplate(ctx context.Context, key string, revision int64) (*IntentRouteTemplate, error) {
	store, err := s.intentTemplateStoreOrErr()
	if err != nil {
		return nil, err
	}
	if !validIntentTemplateKey(key) {
		return nil, infraerrors.BadRequest("INTENT_TEMPLATE_KEY_INVALID", "template key must be 64 lowercase hex characters")
	}
	t, err := store.revoke(ctx, key, revision)
	if err != nil {
		return nil, err
	}
	s.InvalidateConfig(ctx)
	return t, nil
}
