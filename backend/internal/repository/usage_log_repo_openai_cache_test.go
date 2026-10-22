package repository

import (
	"context"
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
