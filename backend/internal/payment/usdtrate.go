package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

// USDT→CNY comes from the same public feed the Epusdt gateway uses for its own
// quotes (fawazahmed0/currency-api), with the project's documented fallback
// host. Results are cached so a burst of checkouts costs one request.
var usdtRateURLs = []string{
	"https://cdn.jsdelivr.net/npm/@fawazahmed0/currency-api@latest/v1/currencies/usdt.json",
	"https://latest.currency-api.pages.dev/v1/currencies/usdt.json",
}

const (
	usdtRateCacheTTL = 5 * time.Minute
	usdtRateTimeout  = 8 * time.Second
)

var (
	usdtRateMu       sync.Mutex
	usdtRateValue    decimal.Decimal
	usdtRateFetched  time.Time
	usdtRateClient   = &http.Client{Timeout: usdtRateTimeout}
	usdtRateNow      = time.Now
	usdtRateFetchURL = fetchUSDTRateFrom
)

// USDTCNYRate returns how many CNY one USDT is worth, rounded to 4 decimals.
// It never invents a rate: when every source fails and the cache is older than
// its TTL, the caller gets an error and must refuse to quote.
func USDTCNYRate(ctx context.Context) (decimal.Decimal, error) {
	usdtRateMu.Lock()
	defer usdtRateMu.Unlock()
	now := usdtRateNow()
	if usdtRateValue.IsPositive() && now.Sub(usdtRateFetched) < usdtRateCacheTTL {
		return usdtRateValue, nil
	}
	var lastErr error
	for _, u := range usdtRateURLs {
		rate, err := usdtRateFetchURL(ctx, u)
		if err != nil {
			lastErr = err
			continue
		}
		usdtRateValue = rate.Round(4)
		usdtRateFetched = now
		return usdtRateValue, nil
	}
	return decimal.Zero, fmt.Errorf("usdt/cny rate unavailable: %w", lastErr)
}

func fetchUSDTRateFrom(ctx context.Context, u string) (decimal.Decimal, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return decimal.Zero, err
	}
	resp, err := usdtRateClient.Do(req)
	if err != nil {
		return decimal.Zero, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return decimal.Zero, fmt.Errorf("%s: http %d", u, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return decimal.Zero, err
	}
	var payload struct {
		USDT map[string]json.Number `json:"usdt"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return decimal.Zero, fmt.Errorf("%s: decode: %w", u, err)
	}
	raw, ok := payload.USDT["cny"]
	if !ok {
		return decimal.Zero, fmt.Errorf("%s: no cny rate", u)
	}
	rate, err := decimal.NewFromString(raw.String())
	if err != nil {
		return decimal.Zero, fmt.Errorf("%s: bad cny rate %q", u, raw)
	}
	// A USDT priced outside 1..20 CNY is a broken feed, not a market move.
	if rate.LessThan(decimal.NewFromInt(1)) || rate.GreaterThan(decimal.NewFromInt(20)) {
		return decimal.Zero, fmt.Errorf("%s: implausible cny rate %s", u, rate)
	}
	return rate, nil
}
