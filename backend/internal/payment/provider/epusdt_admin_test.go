//go:build unit

package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

func TestEpusdtGatewayOrdersSinceLogsInAndPagesUntilWindowStart(t *testing.T) {
	var logins, lists atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case epusdtAdminLoginPath:
			logins.Add(1)
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "reader", body["username"])
			require.Equal(t, "pw", body["password"])
			_, _ = w.Write([]byte(`{"status_code":200,"message":"success","data":{"token":"jwt-1","username":"reader","user_id":2}}`))
		case epusdtAdminOrdersPath:
			lists.Add(1)
			require.Equal(t, "Bearer jwt-1", r.Header.Get("Authorization"))
			q := r.URL.Query()
			require.Equal(t, "binance", q.Get("network"))
			require.Equal(t, "USDT", q.Get("token"))
			require.Equal(t, "0xabc", q.Get("address"))
			_, _ = w.Write([]byte(`{"status_code":200,"message":"success","data":{"list":[
				{"trade_id":"t3","order_id":"tomapi-9","actual_amount":150,"receive_address":"0xabc","token":"USDT","network":"binance","status":1,"created_at":"2026-10-27 09:50:00"},
				{"trade_id":"t2","order_id":"sub2_x","block_transaction_id":"0xdead","actual_amount":149.93,"receive_address":"0xabc","token":"USDT","network":"binance","status":2,"created_at":"2026-10-27 09:40:00"},
				{"trade_id":"t1","order_id":"old","actual_amount":10,"receive_address":"0xabc","token":"USDT","network":"binance","status":3,"created_at":"2026-10-27 06:00:00"}
			],"total":3,"page":1,"page_size":200}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := epusdtTestConfig(srv.URL)
	cfg["adminUsername"] = "reader"
	cfg["adminPassword"] = "pw"
	cfg["network"] = "binance"
	cfg["token"] = "usdt"
	cfg["receiveAddresses"] = "TP525BEN7X9N1pfkVdc9Pv1W43M2LzU6pe, 0x4c1349a30c3a91d2cd69329c48dfb02c10d812c7"
	e, err := NewEpusdt("1", cfg)
	require.NoError(t, err)
	require.True(t, e.AutoSettleConfigured())
	network, token, addresses := e.AutoSettleScope()
	require.Equal(t, "binance", network)
	require.Equal(t, "USDT", token)
	require.Equal(t, []string{"0x4c1349a30c3a91d2cd69329c48dfb02c10d812c7"}, addresses, "TRON addresses do not belong to a BSC scan")

	since := time.Date(2026, 10, 27, 9, 0, 0, 0, epusdtGatewayZone)
	orders, err := e.GatewayOrdersSince(context.Background(), "binance", "USDT", "0xabc", since)
	require.NoError(t, err)
	require.Len(t, orders, 2, "orders before the window are cut off")
	require.Equal(t, payment.GatewayOrderWaiting, orders[0].Status)
	require.Equal(t, "150", orders[0].ActualAmount.String())
	require.Equal(t, payment.GatewayOrderPaid, orders[1].Status)
	require.Equal(t, "0xdead", orders[1].TxHash)
	require.Equal(t, time.Date(2026, 10, 27, 1, 40, 0, 0, time.UTC), orders[1].CreatedAt.UTC())

	_, err = e.GatewayOrdersSince(context.Background(), "binance", "USDT", "0xabc", since)
	require.NoError(t, err)
	require.Equal(t, int32(1), logins.Load(), "the login token is reused")
	require.Equal(t, int32(2), lists.Load())
}

func TestEpusdtAdminLoginMustBeComplete(t *testing.T) {
	cfg := epusdtTestConfig("https://pay.example")
	cfg["adminUsername"] = "reader"
	_, err := NewEpusdt("1", cfg)
	require.ErrorContains(t, err, "adminUsername and adminPassword")

	delete(cfg, "adminUsername")
	e, err := NewEpusdt("1", cfg)
	require.NoError(t, err)
	require.False(t, e.AutoSettleConfigured())
}
