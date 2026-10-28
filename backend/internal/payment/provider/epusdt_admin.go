package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

// The gateway's admin API is the only place that knows about orders of every
// merchant sharing the same wallet. Auto-settlement reads it to make sure a
// short transfer cannot belong to another site's order; it needs a gateway
// admin login (adminUsername/adminPassword on the instance) and is disabled
// without one.

const (
	epusdtAdminLoginPath  = "/admin/api/v1/auth/login"
	epusdtAdminOrdersPath = "/admin/api/v1/orders"
	epusdtAdminPageSize   = 200
	epusdtAdminMaxPages   = 5
	// Gateway timestamps are rendered in its local zone without an offset;
	// the deployment runs on Asia/Shanghai.
	epusdtAdminTokenTTL = 30 * time.Minute
)

var epusdtGatewayZone = time.FixedZone("CST", 8*3600)

var (
	epusdtAdminTokensMu sync.Mutex
	epusdtAdminTokens   = map[string]epusdtAdminToken{}
)

type epusdtAdminToken struct {
	token   string
	expires time.Time
}

// AutoSettleConfigured reports whether a gateway admin login is configured.
func (e *Epusdt) AutoSettleConfigured() bool {
	return e != nil && e.config["adminUsername"] != "" && e.config["adminPassword"] != ""
}

// AutoSettleScope returns the instance's locked chain and the configured
// receiving addresses on it.
func (e *Epusdt) AutoSettleScope() (string, string, []string) {
	network := strings.ToLower(e.config["network"])
	var addresses []string
	for _, addr := range strings.FieldsFunc(e.config["receiveAddresses"], func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == ' '
	}) {
		addr = strings.TrimSpace(addr)
		if network != "tron" && (!strings.HasPrefix(strings.ToLower(addr), "0x") || len(addr) != 42) {
			continue
		}
		if network == "tron" && !strings.HasPrefix(addr, "T") {
			continue
		}
		addresses = append(addresses, addr)
	}
	return network, strings.ToUpper(e.config["token"]), addresses
}

// GatewayOrdersSince lists gateway orders (all merchants, including chain
// sub-orders) paying into address on network/token created at or after since.
func (e *Epusdt) GatewayOrdersSince(ctx context.Context, network, token, address string, since time.Time) ([]payment.GatewayOrder, error) {
	if !e.AutoSettleConfigured() {
		return nil, fmt.Errorf("epusdt admin login not configured")
	}
	var out []payment.GatewayOrder
	for page := 1; page <= epusdtAdminMaxPages; page++ {
		q := url.Values{}
		q.Set("network", strings.ToLower(network))
		q.Set("token", strings.ToUpper(token))
		q.Set("address", address)
		q.Set("page", strconv.Itoa(page))
		q.Set("page_size", strconv.Itoa(epusdtAdminPageSize))
		var data struct {
			List []struct {
				TradeID            string  `json:"trade_id"`
				OrderID            string  `json:"order_id"`
				ParentTradeID      string  `json:"parent_trade_id"`
				BlockTransactionID string  `json:"block_transaction_id"`
				ActualAmount       float64 `json:"actual_amount"`
				ReceiveAddress     string  `json:"receive_address"`
				Token              string  `json:"token"`
				Network            string  `json:"network"`
				Status             int     `json:"status"`
				CreatedAt          string  `json:"created_at"`
			} `json:"list"`
			Total int `json:"total"`
		}
		if err := e.adminGet(ctx, epusdtAdminOrdersPath+"?"+q.Encode(), &data); err != nil {
			return nil, err
		}
		reachedOlder := false
		for _, row := range data.List {
			created, err := time.ParseInLocation("2006-01-02 15:04:05", strings.TrimSpace(row.CreatedAt), epusdtGatewayZone)
			if err != nil {
				return nil, fmt.Errorf("epusdt admin orders: bad created_at %q", row.CreatedAt)
			}
			if created.Before(since) {
				reachedOlder = true
				continue
			}
			out = append(out, payment.GatewayOrder{
				TradeNo:        row.TradeID,
				OrderID:        row.OrderID,
				ParentTradeNo:  row.ParentTradeID,
				TxHash:         row.BlockTransactionID,
				ActualAmount:   decimal.NewFromFloat(row.ActualAmount),
				ReceiveAddress: row.ReceiveAddress,
				Token:          strings.ToUpper(row.Token),
				Network:        strings.ToLower(row.Network),
				Status:         epusdtGatewayStatus(row.Status),
				CreatedAt:      created,
			})
		}
		if reachedOlder || len(data.List) < epusdtAdminPageSize {
			return out, nil
		}
	}
	return nil, fmt.Errorf("epusdt admin orders: more than %d orders in the window", epusdtAdminPageSize*epusdtAdminMaxPages)
}

func epusdtGatewayStatus(status int) string {
	switch status {
	case 1:
		return payment.GatewayOrderWaiting
	case 2:
		return payment.GatewayOrderPaid
	case 3:
		return payment.GatewayOrderExpired
	default:
		return payment.GatewayOrderSelecting
	}
}

func (e *Epusdt) adminGet(ctx context.Context, path string, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		token, err := e.adminToken(ctx, attempt > 0)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.apiBase()+path, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		body, status, err := e.do(req)
		if err != nil {
			return fmt.Errorf("epusdt admin: %w", err)
		}
		if status == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		if err := decodeEpusdtEnvelope(body, status, out); err != nil {
			return fmt.Errorf("epusdt admin: %w", err)
		}
		return nil
	}
	return fmt.Errorf("epusdt admin: unauthorized")
}

func (e *Epusdt) adminToken(ctx context.Context, refresh bool) (string, error) {
	key := e.apiBase() + "\x00" + e.config["adminUsername"]
	epusdtAdminTokensMu.Lock()
	cached, ok := epusdtAdminTokens[key]
	epusdtAdminTokensMu.Unlock()
	if ok && !refresh && time.Now().Before(cached.expires) {
		return cached.token, nil
	}
	payload, _ := json.Marshal(map[string]string{
		"username": e.config["adminUsername"],
		"password": e.config["adminPassword"],
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.apiBase()+epusdtAdminLoginPath, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	body, status, err := e.do(req)
	if err != nil {
		return "", fmt.Errorf("epusdt admin login: %w", err)
	}
	var data struct {
		Token string `json:"token"`
	}
	if err := decodeEpusdtEnvelope(body, status, &data); err != nil {
		return "", fmt.Errorf("epusdt admin login: %w", err)
	}
	if strings.TrimSpace(data.Token) == "" {
		return "", fmt.Errorf("epusdt admin login: empty token")
	}
	epusdtAdminTokensMu.Lock()
	epusdtAdminTokens[key] = epusdtAdminToken{token: data.Token, expires: time.Now().Add(epusdtAdminTokenTTL)}
	epusdtAdminTokensMu.Unlock()
	return data.Token, nil
}
