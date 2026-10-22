package admin

import (
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAICacheTimeRangePreservesTimezoneAndCalendarDay(t *testing.T) {
	for _, tt := range []struct {
		name string
		tz   string
		date string
	}{
		{"shanghai", "Asia/Shanghai", "2026-03-08"},
		{"dst_start", "America/New_York", "2026-03-08"},
		{"dst_end", "America/New_York", "2026-11-01"},
		{"default", "", "2026-03-08"},
		{"invalid", "Invalid/Timezone", "2026-03-08"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			q := url.Values{"start_date": {tt.date}, "end_date": {tt.date}, "timezone": {tt.tz}}
			c.Request = httptest.NewRequest("GET", "/?"+q.Encode(), nil)
			start, end, ok := (&OpenAICacheHandler{}).timeRange(c)
			require.True(t, ok)
			loc := timezone.Location()
			if userLoc, err := time.LoadLocation(tt.tz); tt.tz != "" && err == nil {
				loc = userLoc
			}
			expected, err := time.ParseInLocation("2006-01-02", tt.date, loc)
			require.NoError(t, err)
			require.Equal(t, loc.String(), start.Location().String())
			require.True(t, start.Equal(expected))
			require.True(t, end.Equal(expected.AddDate(0, 0, 1)), "end must be midnight after the selected calendar date: got %v", end)
		})
	}
}
