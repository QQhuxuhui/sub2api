package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
)

// webhookNotifySender 向任意 HTTP 端点 POST 标准 JSON 告警负载,
// 配置 secret 时附带 HMAC-SHA256 签名头供接收方验签。
type webhookNotifySender struct{}

type opsWebhookPayload struct {
	Kind        string           `json:"kind"`
	Severity    string           `json:"severity"`
	Title       string           `json:"title"`
	Description string           `json:"description"`
	Fields      []OpsNotifyField `json:"fields"`
	OccurredAt  string           `json:"occurred_at"`
}

// webhookSignature = hex(HMAC-SHA256(secret, timestamp + "." + body))
func webhookSignature(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func (webhookNotifySender) Send(ctx context.Context, ch *OpsNotifyChannelConfig, msg *OpsNotifyMessage) error {
	if ch == nil || msg == nil {
		return fmt.Errorf("nil channel or message")
	}
	timeout := time.Duration(ch.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	client, err := httpclient.GetClient(httpclient.Options{Timeout: timeout})
	if err != nil {
		return fmt.Errorf("get http client: %w", err)
	}

	occurred := msg.OccurredAt
	if occurred.IsZero() {
		occurred = time.Now()
	}
	fields := msg.Fields
	if fields == nil {
		fields = []OpsNotifyField{}
	}
	body, err := json.Marshal(opsWebhookPayload{
		Kind:        msg.Kind,
		Severity:    msg.Severity,
		Title:       msg.Title,
		Description: msg.Description,
		Fields:      fields,
		OccurredAt:  occurred.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ch.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(ch.Secret) != "" {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		req.Header.Set("X-Sub2API-Timestamp", ts)
		req.Header.Set("X-Sub2API-Signature", webhookSignature(ch.Secret, ts, body))
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook http status %d", resp.StatusCode)
	}
	return nil
}
