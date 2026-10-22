package repository

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestOpenAICacheStatsUsesActualCostAndExcludesWebSocketText(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(_, query string) error {
		if strings.Contains(query, "SUM(total_cost)") || strings.Count(query, "SUM(actual_cost)") != 2 {
			return fmt.Errorf("billed cost must aggregate actual_cost for all and text")
		}
		if !strings.Contains(query, "openai_ws_mode = false") {
			return fmt.Errorf("text bucket must exclude WebSocket requests")
		}
		return nil
	})))
	require.NoError(t, err)
	defer db.Close()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	mock.ExpectQuery("account cache stats").WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{"account_id"}))
	_, err = NewOpenAICacheStatsRepository(db).ListOpenAICacheAccountUsage(context.Background(), start, end)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAICacheDailyStatsGroupsInRequestedTimezone(t *testing.T) {
	for _, name := range []string{"Asia/Shanghai", "America/New_York", "UTC", "Local"} {
		for _, accountID := range []int64{0, 17} {
			t.Run(fmt.Sprintf("%s/account_%d", name, accountID), func(t *testing.T) {
				if name == "Local" {
					// Local is an alias for time.Local, which can already be an
					// IANA location after application timezone initialization.
					previousLocal := time.Local
					local, err := time.LoadLocation("Asia/Shanghai")
					require.NoError(t, err)
					time.Local = local
					t.Cleanup(func() { time.Local = previousLocal })
				}
				loc, err := time.LoadLocation(name)
				require.NoError(t, err)
				start := time.Date(2026, 3, 8, 0, 0, 0, 0, loc)
				end := start.AddDate(0, 0, 1)
				tzName := loc.String()
				if tzName == "" || tzName == "Local" {
					tzName = resolveUsageStatsTimezone()
				}
				db, mock, err := sqlmock.New()
				require.NoError(t, err)
				defer db.Close()
				args := []driver.Value{start, end, tzName}
				query := `(?s)TO_CHAR\(created_at AT TIME ZONE \$3, 'YYYY-MM-DD'\).*created_at >= \$1 AND created_at < \$2`
				if accountID > 0 {
					query += ` AND account_id = \$4`
					args = append(args, accountID)
				} else {
					query += ` AND account_id IN`
				}
				mock.ExpectQuery(query).WithArgs(args...).WillReturnRows(sqlmock.NewRows([]string{"date"}))
				_, err = NewOpenAICacheStatsRepository(db).ListOpenAICacheDailyUsage(context.Background(), start, end, accountID)
				require.NoError(t, err)
				require.NoError(t, mock.ExpectationsWereMet())
			})
		}
	}
}
