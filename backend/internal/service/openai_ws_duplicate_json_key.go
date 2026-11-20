package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/jsondupkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"

	coderws "github.com/coder/websocket"
	"go.uber.org/zap"
)

// Short, fixed close reasons: coder/websocket refuses reasons over 123 bytes,
// so the duplicate key path goes to the log only.
const (
	openAIWSDuplicateJSONKeyCloseReason = "duplicate JSON key in request"
	openAIWSJSONTooDeepCloseReason      = "JSON nesting too deep"
)

type openAIWSRequestIdentityKey struct{}

type openAIWSRequestIdentity struct {
	userID   int64
	apiKeyID int64
	groupID  int64
	hasGroup bool
}

// WithOpenAIWSRequestIdentity stores the authenticated caller on the request
// context, so rejections of WebSocket frames can be logged with user and key.
// groupID is ignored when hasGroup is false.
func WithOpenAIWSRequestIdentity(ctx context.Context, userID, apiKeyID, groupID int64, hasGroup bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIWSRequestIdentityKey{}, openAIWSRequestIdentity{
		userID:   userID,
		apiKeyID: apiKeyID,
		groupID:  groupID,
		hasGroup: hasGroup,
	})
}

// openAIWSClientFrameDuplicateKeyReason checks one raw client frame before any
// compatibility rewrite or event dispatch. It returns the close reason when
// the frame must be rejected, or "" to let it through.
func openAIWSClientFrameDuplicateKeyReason(ctx context.Context, msgType coderws.MessageType, payload []byte) string {
	if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
		return ""
	}
	result, path, truncated, _ := jsondupkey.Find(payload)
	var reason, closeReason string
	switch result {
	case jsondupkey.ResultDuplicate:
		reason, closeReason = "duplicate", openAIWSDuplicateJSONKeyCloseReason
	case jsondupkey.ResultTooDeep:
		reason, closeReason = "too_deep", openAIWSJSONTooDeepCloseReason
	default:
		return ""
	}
	fields := []zap.Field{
		zap.String("transport", "websocket"),
		zap.String("reason", reason),
	}
	if path != "" {
		fields = append(fields, zap.String("json_path", path))
		if truncated {
			fields = append(fields, zap.Bool("json_path_truncated", true))
		}
	}
	if ctx != nil {
		if identity, ok := ctx.Value(openAIWSRequestIdentityKey{}).(openAIWSRequestIdentity); ok {
			fields = append(fields,
				zap.Int64("user_id", identity.userID),
				zap.Int64("api_key_id", identity.apiKeyID),
			)
			if identity.hasGroup {
				fields = append(fields, zap.Int64("group_id", identity.groupID))
			}
		}
	}
	logger.FromContext(ctx).Warn("gateway.duplicate_json_key_rejected", fields...)
	return closeReason
}
