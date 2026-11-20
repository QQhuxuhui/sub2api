package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type wsReadOutcome struct {
	msgType coderws.MessageType
	payload []byte
	err     error
}

// readOneFrameThroughGateway sends payload from a real client and reads it on
// the server side with ReadOpenAIWSClientMessage. It returns the server's read
// outcome and the client's next read error (the close frame, if any).
func readOneFrameThroughGateway(t *testing.T, ctx context.Context, payload string) (wsReadOutcome, error) {
	t.Helper()
	outcome := make(chan wsReadOutcome, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			outcome <- wsReadOutcome{err: err}
			return
		}
		defer func() { _ = conn.CloseNow() }()
		msgType, data, readErr := ReadOpenAIWSClientMessage(ctx, conn, 0, coderws.StatusPolicyViolation, "timeout")
		outcome <- wsReadOutcome{msgType: msgType, payload: data, err: readErr}
		if readErr == nil {
			_ = conn.Close(coderws.StatusNormalClosure, "done")
		}
	}))
	defer server.Close()

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = client.CloseNow() }()
	require.NoError(t, client.Write(dialCtx, coderws.MessageText, []byte(payload)))
	_, _, clientErr := client.Read(dialCtx)

	select {
	case got := <-outcome:
		return got, clientErr
	case <-time.After(5 * time.Second):
		t.Fatal("server read did not finish")
		return wsReadOutcome{}, nil
	}
}

func TestReadOpenAIWSClientMessage_RejectsDuplicateJSONKeys(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	ctx := logger.IntoContext(context.Background(), zap.New(core))
	ctx = WithOpenAIWSRequestIdentity(ctx, 148, 42, 7, true)

	longKey := strings.Repeat("键", 100) // 300 bytes, beyond the close-reason limit
	cases := []struct {
		name, payload, reason, path string
	}{
		{"response.create model", `{"type":"response.create","model":"gpt-4o-mini","model":"gpt-5.5-pro"}`, openAIWSDuplicateJSONKeyCloseReason, "/model"},
		{"session.update", `{"type":"session.update","session":{"model":"a","model":"b"}}`, openAIWSDuplicateJSONKeyCloseReason, "/session/model"},
		{"unicode leading space", " {\"type\":\"response.create\",\"model\":\"a\",\"model\":\"b\"}", openAIWSDuplicateJSONKeyCloseReason, "/model"},
		{"long multibyte key", `{"` + longKey + `":1,"` + longKey + `":2}`, openAIWSDuplicateJSONKeyCloseReason, ""},
		{"too deep", strings.Repeat("[", 10001) + strings.Repeat("]", 10001), openAIWSJSONTooDeepCloseReason, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := logs.Len()
			got, clientErr := readOneFrameThroughGateway(t, ctx, tc.payload)
			var serverClose *OpenAIWSClientCloseError
			require.ErrorAs(t, got.err, &serverClose)
			require.Equal(t, coderws.StatusPolicyViolation, serverClose.StatusCode())
			require.Nil(t, got.payload)

			var closeErr coderws.CloseError
			require.ErrorAs(t, clientErr, &closeErr, "the client receives a close frame")
			require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)
			require.Equal(t, tc.reason, closeErr.Reason)

			require.Equal(t, before+1, logs.Len())
			fields := logs.All()[before].ContextMap()
			require.Equal(t, "websocket", fields["transport"])
			require.Equal(t, int64(148), fields["user_id"])
			require.Equal(t, int64(42), fields["api_key_id"])
			require.Equal(t, int64(7), fields["group_id"])
			if tc.path != "" {
				require.Equal(t, tc.path, fields["json_path"])
			}
		})
	}
}

func TestReadOpenAIWSClientMessage_PassesCleanFrames(t *testing.T) {
	for _, payload := range []string{
		`{"type":"response.create","model":"gpt-5.4","input":[{"role":"user","role":"x"}]}`,
		`not json at all`,
	} {
		got, _ := readOneFrameThroughGateway(t, context.Background(), payload)
		require.NoError(t, got.err)
		require.Equal(t, payload, string(got.payload))
	}
}
