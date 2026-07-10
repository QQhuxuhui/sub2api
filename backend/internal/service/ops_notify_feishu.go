package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
)

// feishuNotifySender 通过飞书自定义机器人 webhook 发送 interactive 卡片消息。
type feishuNotifySender struct{}

// feishuSign 按飞书加签规范生成签名:
// key = timestamp + "\n" + secret,对空串做 HMAC-SHA256 后 Base64。
func feishuSign(secret string, timestamp int64) (string, error) {
	stringToSign := strconv.FormatInt(timestamp, 10) + "\n" + secret
	h := hmac.New(sha256.New, []byte(stringToSign))
	if _, err := h.Write([]byte{}); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}

func feishuCardTemplate(msg *OpsNotifyMessage) string {
	if msg == nil {
		return "blue"
	}
	switch msg.Kind {
	case OpsNotifyKindAlertResolved:
		return "green"
	case OpsNotifyKindCriticalError:
		return "red"
	case OpsNotifyKindTest:
		return "blue"
	}
	switch strings.ToUpper(strings.TrimSpace(msg.Severity)) {
	case "P0":
		return "red"
	case "P1":
		return "orange"
	default:
		return "yellow"
	}
}

func buildFeishuPayload(msg *OpsNotifyMessage, secret string, now time.Time) (map[string]any, error) {
	lines := make([]string, 0, len(msg.Fields)+2)
	if strings.TrimSpace(msg.Description) != "" {
		lines = append(lines, msg.Description)
	}
	for _, f := range msg.Fields {
		lines = append(lines, fmt.Sprintf("**%s**: %s", f.Label, f.Value))
	}
	occurred := msg.OccurredAt
	if occurred.IsZero() {
		occurred = now
	}
	lines = append(lines, fmt.Sprintf("**时间**: %s", occurred.UTC().Format(time.RFC3339)))

	payload := map[string]any{
		"msg_type": "interactive",
		"card": map[string]any{
			"config": map[string]any{"wide_screen_mode": true},
			"header": map[string]any{
				"template": feishuCardTemplate(msg),
				"title":    map[string]any{"tag": "plain_text", "content": msg.Title},
			},
			"elements": []any{
				map[string]any{
					"tag":  "div",
					"text": map[string]any{"tag": "lark_md", "content": strings.Join(lines, "\n")},
				},
			},
		},
	}
	if strings.TrimSpace(secret) != "" {
		ts := now.Unix()
		sign, err := feishuSign(secret, ts)
		if err != nil {
			return nil, err
		}
		payload["timestamp"] = strconv.FormatInt(ts, 10)
		payload["sign"] = sign
	}
	return payload, nil
}

type feishuBotResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func (feishuNotifySender) Send(ctx context.Context, ch *OpsNotifyChannelConfig, msg *OpsNotifyMessage) error {
	if ch == nil || msg == nil {
		return fmt.Errorf("nil channel or message")
	}
	timeout := time.Duration(ch.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	// 不启用 ValidateResolvedIP:通道 URL 由管理员配置,且内网 webhook 网桥是合法场景。
	client, err := httpclient.GetClient(httpclient.Options{Timeout: timeout})
	if err != nil {
		return fmt.Errorf("get http client: %w", err)
	}

	payload, err := buildFeishuPayload(msg, ch.Secret, time.Now())
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ch.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("feishu webhook http status %d", resp.StatusCode)
	}
	var parsed feishuBotResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&parsed); err != nil {
		return fmt.Errorf("decode feishu response: %w", err)
	}
	if parsed.Code != 0 {
		return fmt.Errorf("feishu webhook error code=%d msg=%s", parsed.Code, parsed.Msg)
	}
	return nil
}
