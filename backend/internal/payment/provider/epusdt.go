package provider

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

// Epusdt 常量。接口契约来自 GMWalletApp/epusdt v2 的 wiki/API.md（GMPay 协议）。
const (
	epusdtHTTPTimeout     = 10 * time.Second
	maxEpusdtResponseSize = 1 << 20 // 1MB

	epusdtStatusCodeOK = 200

	epusdtCreateTransactionPath = "/payments/gmpay/v1/order/create-transaction"
	epusdtCheckStatusPath       = "/pay/check-status/"
	epusdtCheckoutInfoPath      = "/pay/checkout-counter-resp/"
	epusdtCheckoutCounterPath   = "/pay/checkout-counter/"

	// 网关订单状态：1 等待支付、2 支付成功、3 已过期、4 等待选择网络/币种。
	// 只有 2 触发确认，其余一律视为未支付。
	epusdtOrderStatusPaid = 2

	epusdtSignatureField = "signature"
)

// Epusdt implements payment.Provider for the self-hosted Epusdt (GM Pay)
// crypto gateway. Orders are created through the GMPay JSON API, the payer is
// redirected to the gateway's hosted cashier, and payment confirmation arrives
// as an HMAC-SHA256 signed JSON callback.
//
// The gateway settles in crypto (USDT/TRX/...) but is driven with fiat amounts:
// the instance currency (default CNY) and the order amount are sent as-is and
// the gateway converts them at its own rate. The callback echoes the fiat
// amount, which is what the fulfillment amount check compares against.
type Epusdt struct {
	instanceID string
	config     map[string]string
	httpClient *http.Client
}

// NewEpusdt creates a new Epusdt provider.
// config keys: pid, secretKey, apiBase, notifyUrl, returnUrl, token, network, currency, cashierBase
//
// cashierBase is optional: the gateway always builds payment_url from its own
// app_uri, so a site that fronts the same gateway under its own domain sets
// cashierBase to have the payer-facing cashier link rewritten to that origin.
//
// token/network must be set together (a fixed chain such as usdt/tron) or
// left empty together (the cashier lets the payer pick a supported chain).
func NewEpusdt(instanceID string, config map[string]string) (*Epusdt, error) {
	cfg, err := NormalizeEpusdtConfig(config)
	if err != nil {
		return nil, err
	}
	return &Epusdt{
		instanceID: instanceID,
		config:     cfg,
		httpClient: &http.Client{Timeout: epusdtHTTPTimeout},
	}, nil
}

// NormalizeEpusdtConfig validates an Epusdt config and returns a canonical
// copy suitable for both persistence and runtime use.
func NormalizeEpusdtConfig(config map[string]string) (map[string]string, error) {
	cfg := make(map[string]string, len(config))
	for k, v := range config {
		cfg[k] = strings.TrimSpace(v)
	}
	for _, k := range []string{"pid", "secretKey", "apiBase", "notifyUrl"} {
		if cfg[k] == "" {
			return nil, fmt.Errorf("epusdt config missing required key: %s", k)
		}
	}
	apiBase, err := normalizeEpusdtAPIBase(cfg["apiBase"])
	if err != nil {
		return nil, err
	}
	cfg["apiBase"] = apiBase

	cfg["token"] = strings.ToLower(cfg["token"])
	cfg["network"] = strings.ToLower(cfg["network"])
	if (cfg["token"] == "") != (cfg["network"] == "") {
		return nil, fmt.Errorf("epusdt config token and network must be set together")
	}

	currency, err := payment.NormalizePaymentCurrency(cfg["currency"])
	if err != nil {
		return nil, fmt.Errorf("epusdt config currency: %w", err)
	}
	cfg["currency"] = currency

	if cfg["cashierBase"] != "" {
		cashierBase, err := normalizeEpusdtCashierBase(cfg["cashierBase"])
		if err != nil {
			return nil, err
		}
		cfg["cashierBase"] = cashierBase
	}

	return cfg, nil
}

// normalizeEpusdtAPIBase accepts the gateway origin (optionally with a path
// prefix behind a reverse proxy) and strips any pasted endpoint path so the
// provider can append its own routes.
func normalizeEpusdtAPIBase(raw string) (string, error) {
	base := strings.TrimSpace(raw)
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("epusdt config apiBase must be an absolute http(s) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("epusdt config apiBase must use http or https")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.RawPath = ""
	path := strings.TrimRight(parsed.Path, "/")
	lower := strings.ToLower(path)
	for _, suffix := range []string{epusdtCreateTransactionPath, "/payments/gmpay/v1", "/payments/gmpay", "/payments"} {
		if strings.HasSuffix(lower, suffix) {
			path = path[:len(path)-len(suffix)]
			break
		}
	}
	parsed.Path = strings.TrimRight(path, "/")
	return strings.TrimRight(parsed.String(), "/"), nil
}

// normalizeEpusdtCashierBase validates the payer-facing cashier origin. Only
// scheme and host are kept: the cashier routes are fixed by the gateway.
func normalizeEpusdtCashierBase(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("epusdt config cashierBase must be an absolute http(s) URL")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

// rewriteEpusdtCashierURL swaps the origin of the gateway cashier link for the
// configured cashierBase, keeping path, query and fragment. Links that are not
// on the gateway's own cashier routes (e.g. a third-party hosted checkout) are
// returned untouched.
func rewriteEpusdtCashierURL(payURL, cashierBase string) string {
	if cashierBase == "" {
		return payURL
	}
	parsed, err := url.Parse(payURL)
	if err != nil || parsed.Host == "" {
		return payURL
	}
	if !strings.HasPrefix(parsed.Path, "/pay/") && !strings.HasPrefix(parsed.Path, "/cashier/") {
		return payURL
	}
	base, err := url.Parse(cashierBase)
	if err != nil || base.Host == "" {
		return payURL
	}
	parsed.Scheme = base.Scheme
	parsed.Host = base.Host
	return parsed.String()
}

func (e *Epusdt) Name() string        { return "Epusdt" }
func (e *Epusdt) ProviderKey() string { return payment.TypeEpusdt }
func (e *Epusdt) SupportedTypes() []payment.PaymentType {
	return []payment.PaymentType{payment.TypeUSDT}
}

func (e *Epusdt) apiBase() string {
	if e == nil {
		return ""
	}
	return e.config["apiBase"]
}

func (e *Epusdt) currency() string {
	if e == nil {
		return payment.DefaultPaymentCurrency
	}
	if currency := strings.TrimSpace(e.config["currency"]); currency != "" {
		return currency
	}
	return payment.DefaultPaymentCurrency
}

func (e *Epusdt) MerchantIdentityMetadata() map[string]string {
	if e == nil {
		return nil
	}
	pid := strings.TrimSpace(e.config["pid"])
	if pid == "" {
		return nil
	}
	return map[string]string{"pid": pid}
}

// resolveURLs returns (notifyURL, returnURL) preferring request values,
// falling back to instance config.
func (e *Epusdt) resolveURLs(req payment.CreatePaymentRequest) (string, string) {
	notifyURL := strings.TrimSpace(req.NotifyURL)
	if notifyURL == "" {
		notifyURL = e.config["notifyUrl"]
	}
	returnURL := strings.TrimSpace(req.ReturnURL)
	if returnURL == "" {
		returnURL = e.config["returnUrl"]
	}
	return notifyURL, returnURL
}

type epusdtEnvelope struct {
	StatusCode int             `json:"status_code"`
	Message    string          `json:"message"`
	Data       json.RawMessage `json:"data"`
	RequestID  string          `json:"request_id"`
}

type epusdtCreateTransactionData struct {
	TradeID        string  `json:"trade_id"`
	OrderID        string  `json:"order_id"`
	Amount         float64 `json:"amount"`
	Currency       string  `json:"currency"`
	ActualAmount   float64 `json:"actual_amount"`
	ReceiveAddress string  `json:"receive_address"`
	Token          string  `json:"token"`
	Status         int     `json:"status"`
	ExpirationTime int64   `json:"expiration_time"`
	PaymentURL     string  `json:"payment_url"`
}

// CreatePayment creates a GMPay transaction and returns the hosted cashier URL.
// The request is sent as application/x-www-form-urlencoded so the amount string
// participates in the signature exactly as formatted (JSON numbers would be
// re-canonicalized by the gateway before signing).
func (e *Epusdt) CreatePayment(ctx context.Context, req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	notifyURL, returnURL := e.resolveURLs(req)
	params := map[string]string{
		"pid":          e.config["pid"],
		"order_id":     strings.TrimSpace(req.OrderID),
		"currency":     strings.ToLower(e.currency()),
		"amount":       strings.TrimSpace(req.Amount),
		"notify_url":   notifyURL,
		"redirect_url": returnURL,
		"name":         strings.TrimSpace(req.Subject),
		"token":        e.config["token"],
		"network":      e.config["network"],
	}
	for k, v := range params {
		if v == "" {
			delete(params, k)
		}
	}
	params[epusdtSignatureField] = epusdtSignStrings(params, e.config["secretKey"])

	body, status, err := e.postForm(ctx, e.apiBase()+epusdtCreateTransactionPath, params)
	if err != nil {
		return nil, fmt.Errorf("epusdt create: %w", err)
	}
	var data epusdtCreateTransactionData
	if err := decodeEpusdtEnvelope(body, status, &data); err != nil {
		return nil, fmt.Errorf("epusdt create: %w", err)
	}
	tradeID := strings.TrimSpace(data.TradeID)
	if tradeID == "" {
		return nil, fmt.Errorf("epusdt create: response missing trade_id")
	}
	payURL := resolveEpusdtReturnedRef(e.apiBase(), data.PaymentURL)
	if payURL == "" {
		payURL = e.apiBase() + epusdtCheckoutCounterPath + url.PathEscape(tradeID)
	}
	payURL = rewriteEpusdtCashierURL(payURL, e.config["cashierBase"])
	expiresAt := time.Time{}
	if data.ExpirationTime > time.Now().Unix() {
		expiresAt = time.Unix(data.ExpirationTime, 0)
	}
	return &payment.CreatePaymentResponse{
		TradeNo:   tradeID,
		PayURL:    payURL,
		Currency:  e.currency(),
		ExpiresAt: expiresAt,
	}, nil
}

// resolveEpusdtReturnedRef absolutizes a payment_url that the gateway returned
// as a site-root-relative path (possible behind a reverse proxy without
// X-Forwarded-Proto). Absolute URLs are returned untouched.
func resolveEpusdtReturnedRef(apiBase, ref string) string {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return ""
	}
	if !strings.HasPrefix(trimmed, "/") {
		return trimmed
	}
	base, err := url.Parse(apiBase)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return trimmed
	}
	return base.ResolveReference(parsed).String()
}

type epusdtCheckStatusData struct {
	TradeID string `json:"trade_id"`
	Status  int    `json:"status"`
}

type epusdtCheckoutInfoData struct {
	TradeID        string  `json:"trade_id"`
	Amount         float64 `json:"amount"`
	ActualAmount   float64 `json:"actual_amount"`
	Token          string  `json:"token"`
	Currency       string  `json:"currency"`
	ReceiveAddress string  `json:"receive_address"`
	Network        string  `json:"network"`
	Status         int     `json:"status"`
}

// QueryOrder polls the gateway by trade_id. check-status only carries the
// status, so on a paid order the checkout info endpoint is read as well to
// recover the fiat amount the fulfillment amount check needs.
func (e *Epusdt) QueryOrder(ctx context.Context, tradeNo string) (*payment.QueryOrderResponse, error) {
	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" {
		return nil, fmt.Errorf("epusdt query: missing trade_id")
	}
	body, status, err := e.get(ctx, e.apiBase()+epusdtCheckStatusPath+url.PathEscape(tradeNo))
	if err != nil {
		return nil, fmt.Errorf("epusdt query: %w", err)
	}
	var statusData epusdtCheckStatusData
	if err := decodeEpusdtEnvelope(body, status, &statusData); err != nil {
		return nil, fmt.Errorf("epusdt query: %w", err)
	}

	result := &payment.QueryOrderResponse{
		TradeNo:  tradeNo,
		Status:   payment.ProviderStatusPending,
		Metadata: e.MerchantIdentityMetadata(),
	}
	if statusData.Status != epusdtOrderStatusPaid {
		return result, nil
	}

	infoBody, infoStatus, err := e.get(ctx, e.apiBase()+epusdtCheckoutInfoPath+url.PathEscape(tradeNo))
	if err != nil {
		return nil, fmt.Errorf("epusdt query info: %w", err)
	}
	var info epusdtCheckoutInfoData
	if err := decodeEpusdtEnvelope(infoBody, infoStatus, &info); err != nil {
		return nil, fmt.Errorf("epusdt query info: %w", err)
	}
	result.Status = payment.ProviderStatusPaid
	result.Amount = info.Amount
	if result.Metadata == nil {
		result.Metadata = map[string]string{}
	}
	if token := strings.TrimSpace(info.Token); token != "" {
		result.Metadata["token"] = token
	}
	if network := strings.TrimSpace(info.Network); network != "" {
		result.Metadata["network"] = network
	}
	if info.ActualAmount > 0 {
		result.Metadata["actual_amount"] = strconv.FormatFloat(info.ActualAmount, 'f', -1, 64)
	}
	return result, nil
}

// VerifyNotification verifies the GMPay JSON callback. The signature covers
// every non-empty field except "signature", with numbers canonicalized the way
// the gateway does (shortest float repr), joined as sorted k=v pairs and
// HMAC-SHA256'd with the merchant secret.
func (e *Epusdt) VerifyNotification(_ context.Context, rawBody string, _ map[string]string) (*payment.PaymentNotification, error) {
	var params map[string]any
	if err := json.Unmarshal([]byte(rawBody), &params); err != nil {
		return nil, fmt.Errorf("parse notify: %w", err)
	}
	signature, _ := params[epusdtSignatureField].(string)
	if strings.TrimSpace(signature) == "" {
		return nil, fmt.Errorf("missing signature")
	}
	expected, err := epusdtSignValues(params, e.config["secretKey"])
	if err != nil {
		return nil, fmt.Errorf("sign notify: %w", err)
	}
	if !hmac.Equal([]byte(expected), []byte(strings.ToLower(strings.TrimSpace(signature)))) {
		return nil, fmt.Errorf("invalid signature")
	}

	status := payment.ProviderStatusFailed
	if epusdtNumberValue(params["status"]) == epusdtOrderStatusPaid {
		status = payment.ProviderStatusSuccess
	}

	pid := epusdtStringValue(params["pid"])
	tradeID := epusdtStringValue(params["trade_id"])
	orderID := epusdtStringValue(params["order_id"])
	amount := epusdtNumberValue(params["amount"])
	if status == payment.ProviderStatusSuccess {
		switch {
		case pid == "":
			return nil, fmt.Errorf("missing pid")
		case tradeID == "":
			return nil, fmt.Errorf("missing trade_id")
		case orderID == "":
			return nil, fmt.Errorf("missing order_id")
		case amount <= 0 || math.IsNaN(amount) || math.IsInf(amount, 0):
			return nil, fmt.Errorf("invalid amount")
		}
	}

	metadata := map[string]string{}
	if pid != "" {
		metadata["pid"] = pid
	}
	for _, key := range []string{"token", "receive_address", "block_transaction_id"} {
		if v := epusdtStringValue(params[key]); v != "" {
			metadata[key] = v
		}
	}
	if actual := epusdtStringValue(params["actual_amount"]); actual != "" {
		metadata["actual_amount"] = actual
	}

	return &payment.PaymentNotification{
		TradeNo:  tradeID,
		OrderID:  orderID,
		Amount:   amount,
		Status:   status,
		RawData:  rawBody,
		Metadata: metadata,
	}, nil
}

// Refund is not supported: on-chain transfers are irreversible and the gateway
// exposes no refund API. Admins settle refunds manually.
func (e *Epusdt) Refund(_ context.Context, _ payment.RefundRequest) (*payment.RefundResponse, error) {
	return nil, fmt.Errorf("epusdt does not support refunds")
}

// decodeEpusdtEnvelope unwraps the {status_code,message,data} envelope and
// decodes data into out when the business status code is 200.
func decodeEpusdtEnvelope(body []byte, httpStatus int, out any) error {
	var env epusdtEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("non-JSON response (HTTP %d): %s", httpStatus, summarizeEasyPayResponse(body))
	}
	if env.StatusCode != epusdtStatusCodeOK {
		msg := strings.TrimSpace(env.Message)
		if msg == "" {
			msg = summarizeEasyPayResponse(body)
		}
		return fmt.Errorf("gateway error %d (HTTP %d): %s", env.StatusCode, httpStatus, msg)
	}
	if out == nil || len(env.Data) == 0 || string(env.Data) == "null" {
		return fmt.Errorf("gateway response missing data (HTTP %d)", httpStatus)
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("decode data: %w", err)
	}
	return nil
}

func (e *Epusdt) postForm(ctx context.Context, endpoint string, params map[string]string) ([]byte, int, error) {
	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return e.do(req)
}

func (e *Epusdt) get(ctx context.Context, endpoint string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	return e.do(req)
}

func (e *Epusdt) do(req *http.Request) ([]byte, int, error) {
	client := e.httpClient
	if client == nil {
		client = &http.Client{Timeout: epusdtHTTPTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxEpusdtResponseSize))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// epusdtSignStrings signs a string parameter map (outbound requests).
func epusdtSignStrings(params map[string]string, secretKey string) string {
	values := make(map[string]any, len(params))
	for k, v := range params {
		values[k] = v
	}
	signature, _ := epusdtSignValues(values, secretKey)
	return signature
}

// epusdtSignValues mirrors the gateway's canonicalization: drop "signature",
// nil and empty values; floats use strconv.FormatFloat(v, 'f', -1, 64); pairs
// are sorted as whole "k=v" strings and joined with "&".
func epusdtSignValues(params map[string]any, secretKey string) (string, error) {
	pairs := make([]string, 0, len(params))
	for k, v := range params {
		if k == epusdtSignatureField || v == nil {
			continue
		}
		var fv string
		switch t := v.(type) {
		case string:
			fv = t
		case float64:
			fv = strconv.FormatFloat(t, 'f', -1, 64)
		case float32:
			fv = strconv.FormatFloat(float64(t), 'f', -1, 64)
		case int:
			fv = strconv.Itoa(t)
		case int64:
			fv = strconv.FormatInt(t, 10)
		case json.Number:
			fv = t.String()
		default:
			return "", fmt.Errorf("unsupported signature value type %T for %q", v, k)
		}
		if fv == "" {
			continue
		}
		pairs = append(pairs, k+"="+fv)
	}
	sort.Strings(pairs)
	mac := hmac.New(sha256.New, []byte(secretKey))
	_, _ = mac.Write([]byte(strings.Join(pairs, "&")))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func epusdtStringValue(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	default:
		return ""
	}
}

func epusdtNumberValue(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0
		}
		return f
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return 0
		}
		return f
	default:
		return 0
	}
}
