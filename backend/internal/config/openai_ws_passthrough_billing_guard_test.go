//go:build unit

package config

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"
)

func TestWarnOpenAIWSPassthroughBillingGap(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		want    bool
	}{
		{"router off", false, false},
		{"router on", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			cfg := &Config{}
			cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = tc.enabled
			cfg.Gateway.OpenAIWS.IngressModeDefault = "passthrough"
			warnOpenAIWSPassthroughBillingGap(cfg, slog.New(slog.NewTextHandler(&buf, nil)))
			got := strings.Contains(buf.String(), "do not bill a turn interrupted")
			if got != tc.want {
				t.Fatalf("warning logged = %v, want %v; log: %q", got, tc.want, buf.String())
			}
			if tc.want && !strings.Contains(buf.String(), "ingress_mode_default=passthrough") {
				t.Fatalf("warning lacks ingress_mode_default: %q", buf.String())
			}
		})
	}
	warnOpenAIWSPassthroughBillingGap(nil, nil) // must not panic
}

func TestValidateCallsOpenAIWSPassthroughBillingGapWarning(t *testing.T) {
	src, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "warnOpenAIWSPassthroughBillingGap(c, nil)") {
		t.Fatal("Config.Validate must call warnOpenAIWSPassthroughBillingGap")
	}
}
