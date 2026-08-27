package repository

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type userEmptyResponseBillingRepository struct {
	db *sql.DB
}

// NewUserEmptyResponseBillingRepository 创建用户级「空返回不扣费」规则仓储。
// 返回管理面接口；计费读路径经 ProvideUserEmptyResponseBillingRepository 窄化取用。
func NewUserEmptyResponseBillingRepository(sqlDB *sql.DB) service.UserEmptyResponseBillingAdminRepository {
	return &userEmptyResponseBillingRepository{db: sqlDB}
}

// ProvideUserEmptyResponseBillingRepository 把管理面接口窄化为计费读路径所需的最小面。
func ProvideUserEmptyResponseBillingRepository(repo service.UserEmptyResponseBillingAdminRepository) service.UserEmptyResponseBillingRepository {
	return repo
}

const userEmptyResponseBillingSelectColumns = `
	SELECT id, user_id, group_id, model, enabled, COALESCE(note, ''), created_at, updated_at
	FROM user_empty_response_billing_rules
`

func scanEmptyResponseBillingRules(rows *sql.Rows) ([]service.EmptyResponseBillingRule, error) {
	rules := make([]service.EmptyResponseBillingRule, 0, 4)
	for rows.Next() {
		var (
			rule    service.EmptyResponseBillingRule
			groupID sql.NullInt64
		)
		if err := rows.Scan(
			&rule.ID, &rule.UserID, &groupID, &rule.Model,
			&rule.Enabled, &rule.Note, &rule.CreatedAt, &rule.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if groupID.Valid {
			id := groupID.Int64
			rule.GroupID = &id
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func (r *userEmptyResponseBillingRepository) ListEnabledByUserID(ctx context.Context, userID int64) ([]service.EmptyResponseBillingRule, error) {
	if userID <= 0 {
		return []service.EmptyResponseBillingRule{}, nil
	}
	rows, err := r.db.QueryContext(ctx,
		userEmptyResponseBillingSelectColumns+`WHERE user_id = $1 AND enabled ORDER BY group_id NULLS LAST, model, id`,
		userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanEmptyResponseBillingRules(rows)
}

func (r *userEmptyResponseBillingRepository) ListByUserID(ctx context.Context, userID int64) ([]service.EmptyResponseBillingRule, error) {
	rows, err := r.db.QueryContext(ctx,
		userEmptyResponseBillingSelectColumns+`WHERE user_id = $1 ORDER BY group_id NULLS LAST, model, id`,
		userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanEmptyResponseBillingRules(rows)
}

// ReplaceByUserID 事务内先删后插，保证管理页保存的是原子的全量替换：
// 并发的两次保存最终呈现其中一份完整配置，而不是两份的交错混合。
func (r *userEmptyResponseBillingRepository) ReplaceByUserID(ctx context.Context, userID int64, rules []service.EmptyResponseBillingRule) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Serialize full replacements for the same user. Without this lock, two
	// concurrent delete-then-insert transactions can commit a merged rule set.
	lockKey := "user-empty-response-billing:" + strconv.FormatInt(userID, 10)
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, advisoryLockHash(lockKey)); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM user_empty_response_billing_rules WHERE user_id = $1`, userID); err != nil {
		return err
	}
	now := time.Now()
	for i := range rules {
		rule := &rules[i]
		var groupID any
		if rule.GroupID != nil {
			groupID = *rule.GroupID
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO user_empty_response_billing_rules (user_id, group_id, model, enabled, note, created_at, updated_at)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $6)
		`, userID, groupID, rule.Model, rule.Enabled, rule.Note, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}
