//go:build unit

package provider

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

const epusdtTestSecret = "epusdt_secret_key"

func epusdtTestConfig(apiBase string) map[string]string {
	return map[string]string{
		"pid":       "1000",
		"secretKey": epusdtTestSecret,
		"apiBase":   apiBase,
		"notifyUrl": "https://merchant.example/api/v1/payment/webhook/epusdt",
		"returnUrl": "https://merchant.example/payment/result",
		"token":     "usdt",
		"network":   "tron",
		"currency":  "cny",
	}
}

// epusdtTestSignForm recomputes the documented GMPay signature from form
// values (sorted k=v pairs, HMAC-SHA256, lowercase hex), independent of the
// implementation under test.
func epusdtTestSignForm(values url.Values, secret string) string {
	pairs := make([]string, 0, len(values))
	for k := range values {
		v := values.Get(k)
		if k == "signature" || v == "" {
			continue
		}
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strings.Join(pairs, "&")))
	return hex.EncodeToString(mac.Sum(nil))
}

// epusdtTestSignedCallback builds a callback body the way the gateway does:
// marshal the notify struct, then sign the map form of that JSON.
func epusdtTestSignedCallback(t *testing.T, payload map[string]any, secret string) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	signature, err := epusdtSignValues(decoded, secret)
	require.NoError(t, err)
	payload["signature"] = signature
	signed, err := json.Marshal(payload)
	require.NoError(t, err)
	return string(signed)
}

func TestNewEpusdtValidatesConfig(t *testing.T) {
	t.Parallel()

	_, err := NewEpusdt("1", map[string]string{"secretKey": "s", "apiBase": "https://pay.example", "notifyUrl": "https://m/n"})
	require.ErrorContains(t, err, "missing required key: pid")

	_, err = NewEpusdt("1", map[string]string{"pid": "1000", "apiBase": "https://pay.example", "notifyUrl": "https://m/n"})
	require.ErrorContains(t, err, "missing required key: secretKey")

	_, err = NewEpusdt("1", map[string]string{"pid": "1000", "secretKey": "s", "apiBase": "pay.example", "notifyUrl": "https://m/n"})
	require.ErrorContains(t, err, "apiBase must be an absolute")

	_, err = NewEpusdt("1", map[string]string{"pid": "1000", "secretKey": "s", "apiBase": "https://pay.example", "notifyUrl": "https://m/n", "token": "usdt"})
	require.ErrorContains(t, err, "token and network must be set together")

	_, err = NewEpusdt("1", map[string]string{"pid": "1000", "secretKey": "s", "apiBase": "https://pay.example", "notifyUrl": "https://m/n", "currency": "RMB1"})
	require.ErrorContains(t, err, "currency")

	prov, err := NewEpusdt("1", map[string]string{
		"pid":       " 1000 ",
		"secretKey": "s",
		"apiBase":   "https://pay.example/payments/gmpay/v1/order/create-transaction",
		"notifyUrl": "https://m/n",
		"token":     "USDT",
		"network":   "Tron",
		"currency":  "usd",
	})
	require.NoError(t, err)
	require.Equal(t, payment.TypeEpusdt, prov.ProviderKey())
	require.Equal(t, []payment.PaymentType{payment.TypeUSDT}, prov.SupportedTypes())
	require.Equal(t, "https://pay.example", prov.config["apiBase"])
	require.Equal(t, "usdt", prov.config["token"])
	require.Equal(t, "tron", prov.config["network"])
	require.Equal(t, "USD", prov.config["currency"])
	require.Equal(t, map[string]string{"pid": "1000"}, prov.MerchantIdentityMetadata())

	// Both empty: the cashier lets the payer pick a chain.
	prov, err = NewEpusdt("1", map[string]string{"pid": "1000", "secretKey": "s", "apiBase": "https://pay.example/", "notifyUrl": "https://m/n"})
	require.NoError(t, err)
	require.Equal(t, payment.DefaultPaymentCurrency, prov.config["currency"])
	require.Empty(t, prov.config["token"])
}

func TestNormalizeEpusdtConfigReturnsCanonicalCopy(t *testing.T) {
	t.Parallel()

	input := map[string]string{
		"pid":       " 1000 ",
		"secretKey": " secret ",
		"apiBase":   " https://pay.example/payments/gmpay/v1/order/create-transaction?ignored=1 ",
		"notifyUrl": " https://merchant.example/notify ",
		"returnUrl": " https://merchant.example/return ",
		"token":     " USDT ",
		"network":   " TRON ",
		"currency":  " cny ",
	}

	normalized, err := NormalizeEpusdtConfig(input)
	require.NoError(t, err)
	require.Equal(t, "1000", normalized["pid"])
	require.Equal(t, "secret", normalized["secretKey"])
	require.Equal(t, "https://pay.example", normalized["apiBase"])
	require.Equal(t, "https://merchant.example/notify", normalized["notifyUrl"])
	require.Equal(t, "https://merchant.example/return", normalized["returnUrl"])
	require.Equal(t, "usdt", normalized["token"])
	require.Equal(t, "tron", normalized["network"])
	require.Equal(t, "CNY", normalized["currency"])

	require.Equal(t, " 1000 ", input["pid"])
	require.Equal(t, " USDT ", input["token"])
	require.Contains(t, input["apiBase"], "create-transaction")
}

func TestEpusdtSignMatchesDocumentedExample(t *testing.T) {
	t.Parallel()

	// Example from GMWalletApp/epusdt wiki/API.md "GMPay 签名".
	params := map[string]string{
		"pid":          "1000",
		"order_id":     "ORD202605230001",
		"currency":     "cny",
		"token":        "usdt",
		"network":      "tron",
		"amount":       "100",
		"notify_url":   "https://merchant.example/notify",
		"redirect_url": "https://merchant.example/return",
		"name":         "VIP",
	}
	require.Equal(t,
		"6f874b1919d95081835e2809b620e354a5866f5a6dbb2e432d1627f1eb10059d",
		epusdtSignStrings(params, epusdtTestSecret),
	)
}

func TestEpusdtSignValuesCanonicalizesNumbersLikeGateway(t *testing.T) {
	t.Parallel()

	withFloats, err := epusdtSignValues(map[string]any{
		"amount": float64(100), "actual_amount": 14.29, "status": float64(2), "pid": "1000", "empty": "", "nothing": nil,
	}, epusdtTestSecret)
	require.NoError(t, err)
	withStrings, err := epusdtSignValues(map[string]any{
		"amount": "100", "actual_amount": "14.29", "status": "2", "pid": "1000",
	}, epusdtTestSecret)
	require.NoError(t, err)
	require.Equal(t, withStrings, withFloats)

	_, err = epusdtSignValues(map[string]any{"flag": true}, epusdtTestSecret)
	require.ErrorContains(t, err, "unsupported signature value type")
}

func TestEpusdtCreatePaymentSignsFormAndReturnsCashierURL(t *testing.T) {
	t.Parallel()

	var gotForm url.Values
	var gotContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, epusdtCreateTransactionPath, r.URL.Path)
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		form, err := url.ParseQuery(string(body))
		require.NoError(t, err)
		gotForm = form
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status_code":200,"message":"success","data":{"trade_id":"20260523171652123456001","order_id":"sub2_20260917abcdefgh","amount":100,"currency":"CNY","actual_amount":14.29,"receive_address":"TTestTronAddress001","token":"USDT","status":1,"expiration_time":4102444800,"payment_url":"/pay/checkout-counter/20260523171652123456001"},"request_id":"r1"}`))
	}))
	defer server.Close()

	prov, err := NewEpusdt("7", epusdtTestConfig(server.URL))
	require.NoError(t, err)

	resp, err := prov.CreatePayment(context.Background(), payment.CreatePaymentRequest{
		OrderID:     "sub2_20260917abcdefgh",
		Amount:      "100.00",
		PaymentType: payment.TypeUSDT,
		Subject:     "余额充值 100.00 CNY",
		ReturnURL:   "https://merchant.example/payment/result?order_id=9",
	})
	require.NoError(t, err)
	require.Equal(t, "20260523171652123456001", resp.TradeNo)
	require.Equal(t, server.URL+"/pay/checkout-counter/20260523171652123456001", resp.PayURL)
	require.True(t, resp.ExpiresAt.Equal(time.Unix(4102444800, 0)))
	require.Empty(t, resp.QRCode)

	require.Contains(t, gotContentType, "application/x-www-form-urlencoded")
	require.Equal(t, "1000", gotForm.Get("pid"))
	require.Equal(t, "sub2_20260917abcdefgh", gotForm.Get("order_id"))
	require.Equal(t, "100.00", gotForm.Get("amount"), "amount string must be forwarded verbatim")
	require.Equal(t, "cny", gotForm.Get("currency"))
	require.Equal(t, "usdt", gotForm.Get("token"))
	require.Equal(t, "tron", gotForm.Get("network"))
	require.Equal(t, "https://merchant.example/api/v1/payment/webhook/epusdt", gotForm.Get("notify_url"))
	require.Equal(t, "https://merchant.example/payment/result?order_id=9", gotForm.Get("redirect_url"))
	require.Equal(t, "余额充值 100.00 CNY", gotForm.Get("name"))
	require.False(t, gotForm.Has("payment_type"))
	require.Equal(t, epusdtTestSignForm(gotForm, epusdtTestSecret), gotForm.Get("signature"))
}

func TestEpusdtCashierBaseRewritesPayerFacingLink(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status_code":200,"message":"success","data":{"trade_id":"t-9","status":1,"payment_url":"https://gateway.example/pay/checkout-counter/t-9?lang=zh"}}`))
	}))
	defer server.Close()

	cfg := epusdtTestConfig(server.URL)
	cfg["cashierBase"] = " https://pay.merchant.example/ignored/path?x=1 "
	prov, err := NewEpusdt("7", cfg)
	require.NoError(t, err)
	require.Equal(t, "https://pay.merchant.example", prov.config["cashierBase"])

	resp, err := prov.CreatePayment(context.Background(), payment.CreatePaymentRequest{OrderID: "sub2_x", Amount: "10.00"})
	require.NoError(t, err)
	require.Equal(t, "https://pay.merchant.example/pay/checkout-counter/t-9?lang=zh", resp.PayURL)

	// Third-party hosted checkout links are not on the gateway cashier routes.
	require.Equal(t, "https://okpay.example/c/abc", rewriteEpusdtCashierURL("https://okpay.example/c/abc", "https://pay.merchant.example"))
	require.Equal(t, "https://gateway.example/pay/x", rewriteEpusdtCashierURL("https://gateway.example/pay/x", ""))

	cfg["cashierBase"] = "pay.merchant.example"
	_, err = NewEpusdt("7", cfg)
	require.ErrorContains(t, err, "cashierBase must be an absolute")
}

func TestEpusdtCreatePaymentOmitsChainWhenUnset(t *testing.T) {
	t.Parallel()

	var gotForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(body))
		_, _ = w.Write([]byte(`{"status_code":200,"message":"success","data":{"trade_id":"t-4","status":4,"payment_url":"https://cashier.example/pay/checkout-counter/t-4"}}`))
	}))
	defer server.Close()

	cfg := epusdtTestConfig(server.URL)
	delete(cfg, "token")
	delete(cfg, "network")
	prov, err := NewEpusdt("7", cfg)
	require.NoError(t, err)

	resp, err := prov.CreatePayment(context.Background(), payment.CreatePaymentRequest{OrderID: "sub2_x", Amount: "10.00"})
	require.NoError(t, err)
	require.Equal(t, "https://cashier.example/pay/checkout-counter/t-4", resp.PayURL)
	require.False(t, gotForm.Has("token"))
	require.False(t, gotForm.Has("network"))
	require.Equal(t, "https://merchant.example/payment/result", gotForm.Get("redirect_url"))
	require.Equal(t, epusdtTestSignForm(gotForm, epusdtTestSecret), gotForm.Get("signature"))
}

func TestEpusdtCreatePaymentReturnsConfiguredCurrency(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status_code":200,"message":"success","data":{"trade_id":"trade-usd","status":1,"payment_url":"https://cashier.example/pay/trade-usd"}}`))
	}))
	defer server.Close()

	cfg := epusdtTestConfig(server.URL)
	cfg["currency"] = "usd"
	prov, err := NewEpusdt("7", cfg)
	require.NoError(t, err)

	resp, err := prov.CreatePayment(context.Background(), payment.CreatePaymentRequest{
		OrderID: "sub2_usd",
		Amount:  "10.00",
	})
	require.NoError(t, err)
	require.Equal(t, "USD", resp.Currency)
}

func TestEpusdtCreatePaymentSurfacesGatewayErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status_code":10003,"message":"no available wallet address","data":null}`))
	}))
	defer server.Close()

	prov, err := NewEpusdt("7", epusdtTestConfig(server.URL))
	require.NoError(t, err)
	_, err = prov.CreatePayment(context.Background(), payment.CreatePaymentRequest{OrderID: "sub2_x", Amount: "10.00"})
	require.ErrorContains(t, err, "gateway error 10003")
	require.ErrorContains(t, err, "no available wallet address")

	htmlServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`<html>bad gateway</html>`))
	}))
	defer htmlServer.Close()
	prov, err = NewEpusdt("7", epusdtTestConfig(htmlServer.URL))
	require.NoError(t, err)
	_, err = prov.CreatePayment(context.Background(), payment.CreatePaymentRequest{OrderID: "sub2_x", Amount: "10.00"})
	require.ErrorContains(t, err, "non-JSON response (HTTP 502)")
}

func TestEpusdtQueryOrder(t *testing.T) {
	t.Parallel()

	status := 1
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, epusdtCheckStatusPath):
			require.Equal(t, epusdtCheckStatusPath+"trade-1", r.URL.Path)
			_, _ = w.Write([]byte(`{"status_code":200,"message":"success","data":{"trade_id":"trade-1","status":` + string(rune('0'+status)) + `}}`))
		case strings.HasPrefix(r.URL.Path, epusdtCheckoutInfoPath):
			require.Equal(t, epusdtCheckoutInfoPath+"trade-1", r.URL.Path)
			_, _ = w.Write([]byte(`{"status_code":200,"message":"success","data":{"trade_id":"trade-1","amount":100,"actual_amount":14.29,"token":"USDT","currency":"CNY","receive_address":"TTest","network":"tron","status":2}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	prov, err := NewEpusdt("7", epusdtTestConfig(server.URL))
	require.NoError(t, err)

	resp, err := prov.QueryOrder(context.Background(), "trade-1")
	require.NoError(t, err)
	require.Equal(t, payment.ProviderStatusPending, resp.Status)
	require.Equal(t, "trade-1", resp.TradeNo)
	require.Zero(t, resp.Amount)
	require.Equal(t, "1000", resp.Metadata["pid"])

	status = 2
	resp, err = prov.QueryOrder(context.Background(), "trade-1")
	require.NoError(t, err)
	require.Equal(t, payment.ProviderStatusPaid, resp.Status)
	require.InDelta(t, 100, resp.Amount, 1e-9)
	require.Equal(t, "USDT", resp.Metadata["token"])
	require.Equal(t, "tron", resp.Metadata["network"])
	require.Equal(t, "14.29", resp.Metadata["actual_amount"])

	_, err = prov.QueryOrder(context.Background(), "  ")
	require.ErrorContains(t, err, "missing trade_id")
}

func TestEpusdtQueryOrderUnknownTrade(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status_code":10008,"message":"order does not exist","data":null}`))
	}))
	defer server.Close()

	prov, err := NewEpusdt("7", epusdtTestConfig(server.URL))
	require.NoError(t, err)
	_, err = prov.QueryOrder(context.Background(), "nope")
	require.ErrorContains(t, err, "gateway error 10008")
}

func TestEpusdtVerifyNotification(t *testing.T) {
	t.Parallel()

	prov, err := NewEpusdt("7", epusdtTestConfig("https://pay.example"))
	require.NoError(t, err)

	base := func() map[string]any {
		return map[string]any{
			"pid":                  "1000",
			"trade_id":             "20260523171652123456001",
			"order_id":             "sub2_20260917abcdefgh",
			"amount":               100.5,
			"actual_amount":        14.29,
			"receive_address":      "TTestTronAddress001",
			"token":                "USDT",
			"block_transaction_id": "0xabc123",
			"status":               2,
		}
	}

	body := epusdtTestSignedCallback(t, base(), epusdtTestSecret)
	n, err := prov.VerifyNotification(context.Background(), body, nil)
	require.NoError(t, err)
	require.Equal(t, payment.ProviderStatusSuccess, n.Status)
	require.Equal(t, "20260523171652123456001", n.TradeNo)
	require.Equal(t, "sub2_20260917abcdefgh", n.OrderID)
	require.InDelta(t, 100.5, n.Amount, 1e-9)
	require.Equal(t, body, n.RawData)
	require.Equal(t, "1000", n.Metadata["pid"])
	require.Equal(t, "USDT", n.Metadata["token"])
	require.Equal(t, "0xabc123", n.Metadata["block_transaction_id"])
	require.Equal(t, "14.29", n.Metadata["actual_amount"])

	// Signature is verified before status is interpreted; an unpaid status is
	// reported as failed rather than rejected.
	unpaid := base()
	unpaid["status"] = 1
	n, err = prov.VerifyNotification(context.Background(), epusdtTestSignedCallback(t, unpaid, epusdtTestSecret), nil)
	require.NoError(t, err)
	require.Equal(t, payment.ProviderStatusFailed, n.Status)

	// Tampered amount.
	tampered := strings.Replace(body, `"amount":100.5`, `"amount":1`, 1)
	require.NotEqual(t, body, tampered)
	_, err = prov.VerifyNotification(context.Background(), tampered, nil)
	require.ErrorContains(t, err, "invalid signature")

	// Wrong secret.
	_, err = prov.VerifyNotification(context.Background(), epusdtTestSignedCallback(t, base(), "other"), nil)
	require.ErrorContains(t, err, "invalid signature")

	// Missing signature / malformed body.
	unsigned, _ := json.Marshal(base())
	_, err = prov.VerifyNotification(context.Background(), string(unsigned), nil)
	require.ErrorContains(t, err, "missing signature")
	_, err = prov.VerifyNotification(context.Background(), "pid=1000&signature=x", nil)
	require.ErrorContains(t, err, "parse notify")
}

func TestEpusdtVerifyNotificationRejectsIncompletePaidPayload(t *testing.T) {
	t.Parallel()

	prov, err := NewEpusdt("7", epusdtTestConfig("https://pay.example"))
	require.NoError(t, err)

	base := func() map[string]any {
		return map[string]any{
			"pid":           "1000",
			"trade_id":      "trade-1",
			"order_id":      "sub2_order_1",
			"amount":        100,
			"actual_amount": 14.29,
			"status":        2,
		}
	}

	tests := []struct {
		name        string
		mutate      func(map[string]any)
		errContains string
	}{
		{
			name: "missing pid",
			mutate: func(payload map[string]any) {
				delete(payload, "pid")
			},
			errContains: "missing pid",
		},
		{
			name: "missing trade id",
			mutate: func(payload map[string]any) {
				delete(payload, "trade_id")
			},
			errContains: "missing trade_id",
		},
		{
			name: "missing order id",
			mutate: func(payload map[string]any) {
				delete(payload, "order_id")
			},
			errContains: "missing order_id",
		},
		{
			name: "zero amount",
			mutate: func(payload map[string]any) {
				payload["amount"] = 0
			},
			errContains: "invalid amount",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			payload := base()
			tt.mutate(payload)
			_, err := prov.VerifyNotification(
				context.Background(),
				epusdtTestSignedCallback(t, payload, epusdtTestSecret),
				nil,
			)
			require.ErrorContains(t, err, tt.errContains)
		})
	}
}

func TestEpusdtRefundUnsupported(t *testing.T) {
	t.Parallel()

	prov, err := NewEpusdt("7", epusdtTestConfig("https://pay.example"))
	require.NoError(t, err)
	_, err = prov.Refund(context.Background(), payment.RefundRequest{TradeNo: "t", Amount: "1.00"})
	require.ErrorContains(t, err, "does not support refunds")
}

func TestResolveEpusdtReturnedRef(t *testing.T) {
	t.Parallel()

	require.Equal(t, "https://pay.example/pay/checkout-counter/t", resolveEpusdtReturnedRef("https://pay.example", "/pay/checkout-counter/t"))
	require.Equal(t, "https://cdn.example/x", resolveEpusdtReturnedRef("https://pay.example", "https://cdn.example/x"))
	require.Equal(t, "", resolveEpusdtReturnedRef("https://pay.example", "  "))
}

func TestEpusdtOnChainSettlementTarget(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pay/checkout-counter-resp/trade-expired" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		// Shape of a real expired order: the gateway keeps the chain quote.
		_, _ = w.Write([]byte(`{"status_code":200,"message":"success","data":{"trade_id":"trade-expired","amount":30,"actual_amount":4.48,"token":"USDT","currency":"CNY","receive_address":"0x4c1349a30c3a91d2cd69329c48dfb02c10d812c7","network":"binance","status":3}}`))
	}))
	defer server.Close()

	cfg := epusdtTestConfig(server.URL)
	cfg["receiveAddresses"] = "TP525BEN7X9N1pfkVdc9Pv1W43M2LzU6pe, 0x1111111111111111111111111111111111111111"
	cfg["chainRpc"] = "bsc=https://rpc.example.com"
	prov, err := NewEpusdt("1", cfg)
	if err != nil {
		t.Fatalf("NewEpusdt: %v", err)
	}
	target, err := prov.OnChainSettlementTarget(context.Background(), " trade-expired ")
	if err != nil {
		t.Fatalf("OnChainSettlementTarget: %v", err)
	}
	if target.Network != "binance" || target.Token != "USDT" || target.ExpectedAmount != "4.48" ||
		target.ReceiveAddress != "0x4c1349a30c3a91d2cd69329c48dfb02c10d812c7" {
		t.Fatalf("unexpected target: %+v", target)
	}
	if len(target.TrustedAddresses) != 2 || target.TrustedAddresses[0] != "TP525BEN7X9N1pfkVdc9Pv1W43M2LzU6pe" {
		t.Fatalf("unexpected trusted addresses: %v", target.TrustedAddresses)
	}
	if got := target.ChainRPC["binance"]; len(got) != 1 || got[0] != "https://rpc.example.com" {
		t.Fatalf("unexpected chain rpc overrides: %v", target.ChainRPC)
	}
	var _ payment.OnChainSettlementProvider = prov
}

func TestNormalizeEpusdtConfigRejectsBadChainRPC(t *testing.T) {
	t.Parallel()

	cfg := epusdtTestConfig("https://pay.example.com")
	cfg["chainRpc"] = "binance=http://insecure.example.com"
	if _, err := NormalizeEpusdtConfig(cfg); err == nil || !strings.Contains(err.Error(), "chainRpc") {
		t.Fatalf("expected chainRpc validation error, got %v", err)
	}
}
