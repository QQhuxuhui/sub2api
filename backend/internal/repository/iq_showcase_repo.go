package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

const iqShowcaseFailureKeep = 100

type iqShowcaseRepository struct {
	db *sql.DB
}

func NewIQShowcaseRepository(db *sql.DB) service.IQShowcaseRepository {
	return &iqShowcaseRepository{db: db}
}

// ---- items ----

const iqShowcaseItemColumns = `i.id, i.title, i.group_id, i.api_key_id, i.protocol, i.model, i.prompt,
	i.reasoning_effort, i.max_output_tokens, i.interval_minutes, i.max_cards, i.enabled, i.sort_order,
	i.next_run_at, i.lease_until, i.config_rev, i.created_at, i.updated_at`

const iqShowcaseItemSelect = `
	SELECT ` + iqShowcaseItemColumns + `,
	       COALESCE(g.name, ''), COALESCE(g.platform, ''), COALESCE(k.name, ''), COALESCE(k.key, ''),
	       lr.created_at, lr.success,
	       (SELECT COUNT(*) FROM iq_showcase_results r
	         WHERE r.item_id = i.id AND r.success AND r.group_id = i.group_id),
	       (SELECT COUNT(*) FROM iq_showcase_results r
	         WHERE r.item_id = i.id AND NOT r.success AND r.created_at > NOW() - INTERVAL '24 hours')
	FROM iq_showcase_items i
	LEFT JOIN groups g ON g.id = i.group_id
	LEFT JOIN api_keys k ON k.id = i.api_key_id
	LEFT JOIN LATERAL (
	    SELECT r.created_at, r.success FROM iq_showcase_results r
	    WHERE r.item_id = i.id ORDER BY r.created_at DESC, r.id DESC LIMIT 1
	) lr ON TRUE`

func scanIQShowcaseItemCore(row rowScanner, extra ...any) (*service.IQShowcaseItem, error) {
	it := &service.IQShowcaseItem{}
	var keyID sql.NullInt64
	var lease sql.NullTime
	dest := []any{&it.ID, &it.Title, &it.GroupID, &keyID, &it.Protocol, &it.Model, &it.Prompt,
		&it.ReasoningEffort, &it.MaxOutputTokens, &it.IntervalMinutes, &it.MaxCards, &it.Enabled, &it.SortOrder,
		&it.NextRunAt, &lease, &it.ConfigRev, &it.CreatedAt, &it.UpdatedAt}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrIQShowcaseItemNotFound
		}
		return nil, err
	}
	if keyID.Valid {
		id := keyID.Int64
		it.APIKeyID = &id
	}
	if lease.Valid {
		t := lease.Time
		it.LeaseUntil = &t
	}
	return it, nil
}

func scanIQShowcaseItem(row rowScanner) (*service.IQShowcaseItem, error) {
	var rawKey string
	var lastAt sql.NullTime
	var lastOK sql.NullBool
	var groupName, groupPlatform, keyName string
	var successCards, failures int
	it, err := scanIQShowcaseItemCore(row, &groupName, &groupPlatform, &keyName, &rawKey,
		&lastAt, &lastOK, &successCards, &failures)
	if err != nil {
		return nil, err
	}
	it.GroupName, it.GroupPlatform, it.APIKeyName = groupName, groupPlatform, keyName
	it.APIKeyMasked = maskIQShowcaseKey(rawKey)
	if lastAt.Valid {
		t := lastAt.Time
		it.LastResultAt = &t
	}
	if lastOK.Valid {
		ok := lastOK.Bool
		it.LastSuccess = &ok
	}
	it.SuccessCards, it.RecentFailures = successCards, failures
	return it, nil
}

// maskIQShowcaseKey keeps only the last four characters of a key.
func maskIQShowcaseKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "****"
	}
	prefix := ""
	if len(key) > 3 && key[:3] == "sk-" {
		prefix = "sk-"
	}
	return prefix + "…" + key[len(key)-4:]
}

func (r *iqShowcaseRepository) ListItems(ctx context.Context) ([]*service.IQShowcaseItem, error) {
	rows, err := r.db.QueryContext(ctx, iqShowcaseItemSelect+` ORDER BY i.group_id, i.sort_order, i.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*service.IQShowcaseItem{}
	for rows.Next() {
		it, err := scanIQShowcaseItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (r *iqShowcaseRepository) GetItem(ctx context.Context, id int64) (*service.IQShowcaseItem, error) {
	return scanIQShowcaseItem(r.db.QueryRowContext(ctx, iqShowcaseItemSelect+` WHERE i.id = $1`, id))
}

func (r *iqShowcaseRepository) CreateItem(ctx context.Context, it *service.IQShowcaseItem) (*service.IQShowcaseItem, error) {
	var id int64
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO iq_showcase_items (title, group_id, api_key_id, protocol, model, prompt, reasoning_effort,
			max_output_tokens, interval_minutes, max_cards, enabled, sort_order)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`,
		it.Title, it.GroupID, it.APIKeyID, it.Protocol, it.Model, it.Prompt, it.ReasoningEffort,
		it.MaxOutputTokens, it.IntervalMinutes, it.MaxCards, it.Enabled, it.SortOrder).Scan(&id)
	if err != nil {
		return nil, err
	}
	return r.GetItem(ctx, id)
}

func (r *iqShowcaseRepository) UpdateItem(ctx context.Context, it *service.IQShowcaseItem) (*service.IQShowcaseItem, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// SET expressions see the row as it is when the UPDATE takes its lock,
	// so the rev decision cannot rest on a stale read of another edit.
	res, err := tx.ExecContext(ctx, `
		UPDATE iq_showcase_items SET
			config_rev = config_rev + CASE WHEN
				(group_id, api_key_id, protocol, model, prompt, reasoning_effort, max_output_tokens)
				IS DISTINCT FROM ($3::bigint, $4::bigint, $5::varchar, $6::varchar, $7::text, $8::varchar, $9::integer)
				THEN 1 ELSE 0 END,
			title = $2, group_id = $3, api_key_id = $4, protocol = $5, model = $6,
			prompt = $7, reasoning_effort = $8, max_output_tokens = $9, interval_minutes = $10, max_cards = $11,
			enabled = $12, sort_order = $13, updated_at = NOW()
		WHERE id = $1`,
		it.ID, it.Title, it.GroupID, it.APIKeyID, it.Protocol, it.Model, it.Prompt, it.ReasoningEffort,
		it.MaxOutputTokens, it.IntervalMinutes, it.MaxCards, it.Enabled, it.SortOrder)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, service.ErrIQShowcaseItemNotFound
	}
	if err := pruneIQShowcaseCards(ctx, tx, it.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r.GetItem(ctx, it.ID)
}

func (r *iqShowcaseRepository) SetEnabled(ctx context.Context, id int64, enabled bool) (*service.IQShowcaseItem, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE iq_showcase_items SET enabled = $2, updated_at = NOW() WHERE id = $1`, id, enabled)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, service.ErrIQShowcaseItemNotFound
	}
	return r.GetItem(ctx, id)
}

func (r *iqShowcaseRepository) DeleteItem(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM iq_showcase_items WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return service.ErrIQShowcaseItemNotFound
	}
	return nil
}

// ---- groups and keys ----

func (r *iqShowcaseRepository) GetGroup(ctx context.Context, id int64) (*service.IQShowcaseGroupInfo, error) {
	g := &service.IQShowcaseGroupInfo{}
	err := r.db.QueryRowContext(ctx, `SELECT id, name, platform FROM groups WHERE id = $1 AND deleted_at IS NULL`, id).
		Scan(&g.ID, &g.Name, &g.Platform)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return g, nil
}

func (r *iqShowcaseRepository) GetRunKey(ctx context.Context, id int64) (*service.IQShowcaseRunKey, error) {
	k := &service.IQShowcaseRunKey{}
	var groupID sql.NullInt64
	var expires sql.NullTime
	err := r.db.QueryRowContext(ctx, `SELECT key, group_id, status, expires_at FROM api_keys WHERE id = $1 AND deleted_at IS NULL`, id).
		Scan(&k.Key, &groupID, &k.Status, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if groupID.Valid {
		g := groupID.Int64
		k.GroupID = &g
	}
	if expires.Valid {
		t := expires.Time
		k.ExpiresAt = &t
	}
	return k, nil
}

func (r *iqShowcaseRepository) ListKeyOptions(ctx context.Context, groupID int64) ([]*service.IQShowcaseKeyOption, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT k.id, k.name, k.key, k.status, COALESCE(u.email, '')
		FROM api_keys k LEFT JOIN users u ON u.id = k.user_id
		WHERE k.group_id = $1 AND k.deleted_at IS NULL
		ORDER BY k.id`, groupID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*service.IQShowcaseKeyOption{}
	for rows.Next() {
		o := &service.IQShowcaseKeyOption{}
		var raw string
		if err := rows.Scan(&o.ID, &o.Name, &raw, &o.Status, &o.UserEmail); err != nil {
			return nil, err
		}
		o.Masked = maskIQShowcaseKey(raw)
		out = append(out, o)
	}
	return out, rows.Err()
}

// ---- running ----

func (r *iqShowcaseRepository) ListDueItemIDs(ctx context.Context, limit int) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id FROM iq_showcase_items
		WHERE enabled AND next_run_at <= NOW() AND (lease_until IS NULL OR lease_until < NOW())
		ORDER BY next_run_at, id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *iqShowcaseRepository) ClaimItem(ctx context.Context, id int64, token string, lease time.Duration, due bool) (*service.IQShowcaseItem, error) {
	query := `
		UPDATE iq_showcase_items i SET lease_until = NOW() + make_interval(secs => $3), lease_token = $2
		WHERE i.id = $1 AND (i.lease_until IS NULL OR i.lease_until < NOW())
		RETURNING ` + iqShowcaseItemColumns
	if due {
		query = `
		UPDATE iq_showcase_items i SET lease_until = NOW() + make_interval(secs => $3), lease_token = $2,
			next_run_at = NOW() + make_interval(mins => i.interval_minutes)
		WHERE i.id = $1 AND i.enabled AND i.next_run_at <= NOW() AND (i.lease_until IS NULL OR i.lease_until < NOW())
		RETURNING ` + iqShowcaseItemColumns
	}
	it, err := scanIQShowcaseItemCore(r.db.QueryRowContext(ctx, query, id, token, lease.Seconds()))
	if errors.Is(err, service.ErrIQShowcaseItemNotFound) {
		return nil, nil
	}
	return it, err
}

func (r *iqShowcaseRepository) CompleteRound(ctx context.Context, itemID int64, token string, res *service.IQShowcaseResult) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var locked int64
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM iq_showcase_items
		WHERE id = $1 AND lease_token = $2 AND lease_until > NOW()
		FOR UPDATE`, itemID, token).Scan(&locked)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO iq_showcase_results (item_id, config_rev, group_id, model, success, svg, error, http_status, latency_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		itemID, res.ConfigRev, res.GroupID, res.Model, res.Success, res.SVG, res.Error, res.HTTPStatus, res.LatencyMs); err != nil {
		return false, err
	}
	if err := pruneIQShowcaseCards(ctx, tx, itemID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM iq_showcase_results WHERE item_id = $1 AND NOT success AND id NOT IN (
			SELECT id FROM iq_showcase_results WHERE item_id = $1 AND NOT success
			ORDER BY created_at DESC, id DESC LIMIT $2)`, itemID, iqShowcaseFailureKeep); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE iq_showcase_items SET lease_until = NULL, lease_token = NULL
		WHERE id = $1 AND lease_token = $2`, itemID, token); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// pruneIQShowcaseCards keeps the item's newest max_cards success cards of its
// current group. Cards drawn before an edit of the prompt or model stay: the
// page is a history of what the group answered, and each card keeps the model
// it ran with. Only cards of a group the item moved away from go.
func pruneIQShowcaseCards(ctx context.Context, tx *sql.Tx, itemID int64) error {
	_, err := tx.ExecContext(ctx, iqShowcasePruneCardsSQL+` AND r2.item_id = $1) x
		WHERE r.id = x.id AND (x.stale OR x.rn > x.max_cards)`, itemID)
	return err
}

// iqShowcasePruneCardsSQL ranks success cards per item, those of the item's
// current group apart from stale ones; callers close the subquery and filter.
const iqShowcasePruneCardsSQL = `
	DELETE FROM iq_showcase_results r USING (
		SELECT r2.id, r2.group_id <> i.group_id AS stale,
		       ROW_NUMBER() OVER (PARTITION BY r2.item_id, r2.group_id = i.group_id
		                          ORDER BY r2.created_at DESC, r2.id DESC) AS rn,
		       i.max_cards
		FROM iq_showcase_results r2 JOIN iq_showcase_items i ON i.id = r2.item_id
		WHERE r2.success`

// ---- results ----

func (r *iqShowcaseRepository) ListResults(ctx context.Context, itemID int64, success *bool, limit int) ([]*service.IQShowcaseResult, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, item_id, config_rev, group_id, model, success, svg, error, http_status, latency_ms, created_at
		FROM iq_showcase_results
		WHERE item_id = $1 AND ($2::boolean IS NULL OR success = $2)
		ORDER BY created_at DESC, id DESC LIMIT $3`, itemID, success, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*service.IQShowcaseResult{}
	for rows.Next() {
		res := &service.IQShowcaseResult{}
		if err := rows.Scan(&res.ID, &res.ItemID, &res.ConfigRev, &res.GroupID, &res.Model, &res.Success,
			&res.SVG, &res.Error, &res.HTTPStatus, &res.LatencyMs, &res.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, rows.Err()
}

func (r *iqShowcaseRepository) ListVisibleItems(ctx context.Context) ([]*service.IQShowcaseVisibleItem, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT i.id, i.title, i.group_id, g.name, i.config_rev, i.max_cards
		FROM iq_showcase_items i JOIN groups g ON g.id = i.group_id AND g.deleted_at IS NULL
		WHERE i.enabled
		ORDER BY g.sort_order, g.id, i.sort_order, i.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*service.IQShowcaseVisibleItem{}
	for rows.Next() {
		v := &service.IQShowcaseVisibleItem{}
		if err := rows.Scan(&v.ID, &v.Title, &v.GroupID, &v.GroupName, &v.ConfigRev, &v.MaxCards); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *iqShowcaseRepository) ListCardMeta(ctx context.Context, itemID, groupID int64, limit int) ([]*service.IQShowcaseCardMeta, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, model, created_at FROM iq_showcase_results
		WHERE item_id = $1 AND group_id = $2 AND success
		ORDER BY created_at DESC, id DESC LIMIT $3`, itemID, groupID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*service.IQShowcaseCardMeta{}
	for rows.Next() {
		c := &service.IQShowcaseCardMeta{}
		if err := rows.Scan(&c.ID, &c.Model, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *iqShowcaseRepository) GetCardSVGs(ctx context.Context, ids []int64) ([]*service.IQShowcaseCardSVG, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT r.id, r.svg FROM iq_showcase_results r
		JOIN iq_showcase_items i ON i.id = r.item_id AND i.enabled AND i.group_id = r.group_id
		JOIN groups g ON g.id = i.group_id AND g.deleted_at IS NULL
		WHERE r.id = ANY($1) AND r.success`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*service.IQShowcaseCardSVG{}
	for rows.Next() {
		c := &service.IQShowcaseCardSVG{}
		if err := rows.Scan(&c.ID, &c.SVG); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *iqShowcaseRepository) Maintenance(ctx context.Context, failRetention time.Duration) error {
	if _, err := r.db.ExecContext(ctx, `
		DELETE FROM iq_showcase_results WHERE NOT success AND created_at < NOW() - make_interval(secs => $1)`,
		failRetention.Seconds()); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, iqShowcasePruneCardsSQL+`) x
		WHERE r.id = x.id AND (x.stale OR x.rn > x.max_cards)`)
	return err
}
