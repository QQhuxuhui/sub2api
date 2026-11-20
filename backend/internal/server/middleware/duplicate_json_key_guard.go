package middleware

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/jsondupkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// WithDuplicateJSONKeyGuard rejects gateway request bodies whose JSON holds a
// duplicate member name before next runs, then calls next (which calls
// c.Next itself).
//
// The gateway reads fields with gjson (first duplicate wins) while upstreams
// decode with encoding/json or Python (last duplicate wins). A body such as
// {"model":"cheap",...,"model":"expensive"} would be billed as the first model
// and served as the second. See internal/pkg/jsondupkey for what is compared
// and which user-content subtrees are skipped.
//
// The downstream always receives the original transfer bytes and headers.
// routeBodyLimits maps route templates (c.FullPath()) to body limits that the
// route applies only after this guard, so the read here honours them.
func WithDuplicateJSONKeyGuard(next gin.HandlerFunc, routeBodyLimits map[string]int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request == nil {
			next(c)
			return
		}
		switch c.Request.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
		case http.MethodGet:
			attachOpenAIWSRequestIdentity(c)
			next(c)
			return
		default:
			next(c)
			return
		}
		if c.Request.Body == nil || c.Request.Body == http.NoBody {
			next(c)
			return
		}
		limit, limited := routeBodyLimits[c.FullPath()]
		if preread, ok := c.Request.Body.(*httputil.PrereadBody); ok {
			body := preread.Bytes()
			if limited && int64(len(body)) > limit {
				next(c) // the route's own limit answers 413
				return
			}
			if duplicateJSONKeyGuardRejects(c, body) {
				return
			}
			next(c)
			return
		}
		if limited {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		}
		body, ok := duplicateJSONKeyGuardReadBody(c)
		if !ok {
			next(c)
			return
		}
		if duplicateJSONKeyGuardRejects(c, body) {
			return
		}
		next(c)
	}
}

// duplicateJSONKeyGuardReadBody returns the view of the body to scan, and
// restores c.Request.Body so the downstream reads the original bytes. ok is
// false when there is nothing to scan (non-JSON body, empty body or a read
// error, which the downstream will see again).
func duplicateJSONKeyGuardReadBody(c *gin.Context) ([]byte, bool) {
	req := c.Request
	encoding := strings.ToLower(strings.TrimSpace(req.Header.Get("Content-Encoding")))
	if encoding == "" || encoding == "identity" {
		return duplicateJSONKeyGuardReadPlain(c)
	}

	rawReq := req.Clone(req.Context())
	rawReq.Header.Del("Content-Encoding")
	rawReq.Body = req.Body
	raw, err := httputil.ReadRequestBodyWithPrealloc(rawReq)
	if err != nil {
		req.Body = errorReadCloser{err: err}
		return nil, false
	}
	req.Body = io.NopCloser(bytes.NewReader(raw))

	decodeReq := req.Clone(req.Context())
	decodeReq.Body = io.NopCloser(bytes.NewReader(raw))
	decoded, err := httputil.ReadRequestBodyWithPrealloc(decodeReq)
	if err != nil {
		// Endpoints that bind JSON without decoding read the raw bytes.
		return raw, true
	}
	return decoded, true
}

// duplicateJSONKeyGuardReadPlain reads only up to the first significant byte.
// A body that does not start like JSON (multipart, audio) is handed back
// unread; a JSON body is read whole and handed back as a PrereadBody.
func duplicateJSONKeyGuardReadPlain(c *gin.Context) ([]byte, bool) {
	req := c.Request
	original := req.Body
	prefix := make([]byte, 0, 512)
	chunk := make([]byte, 512)
	for {
		n, err := original.Read(chunk)
		prefix = append(prefix, chunk[:n]...)
		if !jsondupkey.IsLeadingSpacePrefix(prefix) {
			break
		}
		if err == io.EOF {
			req.Body = replayReadCloser(prefix, nil, original)
			return prefix, true
		}
		if err != nil {
			req.Body = errorReadCloser{err: err}
			return nil, false
		}
	}
	if lead, _ := jsondupkey.LeadingByte(prefix); lead != '{' && lead != '[' {
		req.Body = replayReadCloser(prefix, original, original)
		return prefix, true
	}

	// Read the rest with the gateway's chunked reader: it assembles the exact
	// size once instead of doubling a buffer.
	body, err := httputil.ReadRequestBodyWithPrealloc(&http.Request{
		Header:        http.Header{},
		Body:          io.NopCloser(io.MultiReader(bytes.NewReader(prefix), original)),
		ContentLength: req.ContentLength,
	})
	if err != nil {
		req.Body = errorReadCloser{err: err}
		return nil, false
	}
	req.Body = httputil.NewPrereadBody(body)
	return body, true
}

// duplicateJSONKeyGuardRejects decides on the scanned body and writes the
// rejection. It returns true when the request was rejected.
func duplicateJSONKeyGuardRejects(c *gin.Context, body []byte) bool {
	route := c.FullPath()
	lead, ok := jsondupkey.LeadingByte(body)
	if !ok {
		return false
	}
	if lead != '{' && lead != '[' {
		if duplicateJSONKeyGuardResponsesRoutes[route] {
			rejectDuplicateJSONKey(c, "non_json_prefix", "", false)
			return true
		}
		return false
	}
	result, path, truncated, complete := jsondupkey.FindWithOptions(body, jsondupkey.Options{
		SkipRootContent: duplicateJSONKeyGuardSeedanceRoutes[route],
	})
	if result == jsondupkey.ResultTooDeep {
		rejectDuplicateJSONKey(c, "too_deep", "", false)
		return true
	}
	if !complete && duplicateJSONKeyGuardMultipartRoutes[route] && isMultipartFormRequest(c.Request) {
		// A form may carry a preamble that starts like JSON; the multipart
		// parser skips it. Only a body that is not one complete JSON value is
		// left to the form parser, since some endpoints try JSON first.
		return false
	}
	switch result {
	case jsondupkey.ResultDuplicate:
		rejectDuplicateJSONKey(c, "duplicate", path, truncated)
		return true
	case jsondupkey.ResultInvalid:
		rejectDuplicateJSONKey(c, "invalid_json", "", false)
		return true
	}
	return false
}

func isMultipartFormRequest(req *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	return err == nil && strings.EqualFold(mediaType, "multipart/form-data")
}

func rejectDuplicateJSONKey(c *gin.Context, reason, jsonPath string, truncated bool) {
	service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalPolicyDenied)

	fields := []zap.Field{
		zap.String("method", c.Request.Method),
		zap.String("path", c.Request.URL.Path),
		zap.String("reason", reason),
	}
	if jsonPath != "" {
		fields = append(fields, zap.String("json_path", jsonPath))
		if truncated {
			fields = append(fields, zap.Bool("json_path_truncated", true))
		}
	}
	if apiKey, ok := GetAPIKeyFromContext(c); ok && apiKey != nil {
		fields = append(fields, zap.Int64("user_id", apiKey.UserID), zap.Int64("api_key_id", apiKey.ID))
		if apiKey.GroupID != nil {
			fields = append(fields, zap.Int64("group_id", *apiKey.GroupID))
		}
	}
	logger.FromContext(c.Request.Context()).Warn("gateway.duplicate_json_key_rejected", fields...)

	var message, code string
	switch reason {
	case "duplicate":
		message, code = "Request body contains a duplicate JSON key: "+jsonPath, "duplicate_json_key"
	case "too_deep":
		message, code = "Request body JSON nesting is too deep", "json_nesting_too_deep"
	default:
		message, code = "Failed to parse request body", "invalid_json"
	}
	writeDuplicateJSONKeyError(c, message, code)
	c.Abort()
}

func writeDuplicateJSONKeyError(c *gin.Context, message, code string) {
	route := c.FullPath()
	switch {
	case strings.HasPrefix(route, "/v1beta") || strings.HasPrefix(route, "/antigravity/v1beta"):
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"code":    http.StatusBadRequest,
			"message": message,
			"status":  "INVALID_ARGUMENT",
		}})
	case duplicateJSONKeyGuardAnthropicRoutes[route]:
		c.JSON(http.StatusBadRequest, gin.H{
			"type":  "error",
			"error": gin.H{"type": "invalid_request_error", "message": message},
		})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"message": message,
			"type":    "invalid_request_error",
			"param":   nil,
			"code":    code,
		}})
	}
}

// attachOpenAIWSRequestIdentity lets rejections of WebSocket frames, which
// are read far from the gin context, log the authenticated caller.
func attachOpenAIWSRequestIdentity(c *gin.Context) {
	apiKey, ok := GetAPIKeyFromContext(c)
	if !ok || apiKey == nil {
		return
	}
	var groupID int64
	if apiKey.GroupID != nil {
		groupID = *apiKey.GroupID
	}
	c.Request = c.Request.WithContext(service.WithOpenAIWSRequestIdentity(
		c.Request.Context(), apiKey.UserID, apiKey.ID, groupID, apiKey.GroupID != nil))
}

// duplicateJSONKeyGuardResponsesRoutes read JSON whatever the media type, and
// the compact path rebuilds the body with gjson before validating it, so a
// body that does not start with JSON is rejected outright.
var duplicateJSONKeyGuardResponsesRoutes = map[string]bool{
	"/v1/responses":                         true,
	"/v1/responses/*subpath":                true,
	"/responses":                            true,
	"/responses/*subpath":                   true,
	"/backend-api/codex/responses":          true,
	"/backend-api/codex/responses/*subpath": true,
}

// duplicateJSONKeyGuardMultipartRoutes parse multipart/form-data bodies with a
// form parser (images, Grok media, Live, voice).
var duplicateJSONKeyGuardMultipartRoutes = map[string]bool{
	"/v1/images/edits":                  true,
	"/images/edits":                     true,
	"/v1/images/edits/async":            true,
	"/images/edits/async":               true,
	"/v1/images/generations":            true,
	"/images/generations":               true,
	"/v1/images/generations/async":      true,
	"/images/generations/async":         true,
	"/v1/videos":                        true,
	"/videos":                           true,
	"/v1/videos/generations":            true,
	"/videos/generations":               true,
	"/v1/videos/edits":                  true,
	"/videos/edits":                     true,
	"/v1/videos/extensions":             true,
	"/videos/extensions":                true,
	"/v1/live":                          true,
	"/backend-api/codex/realtime/calls": true,
	"/v1/stt":                           true,
	"/stt":                              true,
	"/v1/custom-voices":                 true,
	"/custom-voices":                    true,
	"/v1/custom-voices/:voice_id":       true,
	"/custom-voices/:voice_id":          true,
}

// duplicateJSONKeyGuardSeedanceRoutes carry user content in the root
// "content" member.
var duplicateJSONKeyGuardSeedanceRoutes = map[string]bool{
	"/api/v3/contents/generations/tasks": true,
	"/v3/contents/generations/tasks":     true,
	"/v1/contents/generations/tasks":     true,
	"/contents/generations/tasks":        true,
}

// duplicateJSONKeyGuardAnthropicRoutes answer in the Anthropic error shape.
var duplicateJSONKeyGuardAnthropicRoutes = map[string]bool{
	"/v1/messages":                          true,
	"/v1/messages/count_tokens":             true,
	"/messages/count_tokens":                true,
	"/antigravity/v1/messages":              true,
	"/antigravity/v1/messages/count_tokens": true,
	"/v1/systemone":                         true,
}

// errorReadCloser hands a body read error to the downstream unchanged.
type errorReadCloser struct{ err error }

func (e errorReadCloser) Read([]byte) (int, error) { return 0, e.err }
func (e errorReadCloser) Close() error             { return nil }

type replayBody struct {
	io.Reader
	closer io.Closer
}

func (r replayBody) Close() error { return r.closer.Close() }

// replayReadCloser replays prefix followed by rest (nil when the body ended),
// closing closer on Close.
func replayReadCloser(prefix []byte, rest io.Reader, closer io.Closer) io.ReadCloser {
	reader := io.Reader(bytes.NewReader(prefix))
	if rest != nil {
		reader = io.MultiReader(reader, rest)
	}
	return replayBody{Reader: reader, closer: closer}
}
