package service

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// openAIResponsesRejectedExtraFields lists non-OpenAI top-level request fields
// that some clients send (enable_thinking is a Qwen/DashScope switch) and that
// OpenAI-compatible upstreams reject as unsupported. When the upstream names
// one of them, the field is dropped and the request retried once. Names must be
// plain keys: they are used as gjson/sjson paths as is.
var openAIResponsesRejectedExtraFields = []string{"enable_thinking"}

// Only the lead-in is case-insensitive; the parameter name that follows keeps
// its case so it can be compared exactly. "is" counts as a separator only as a
// standalone word, so "parameter isenable_thinking" reads as "isenable_thinking".
var openAIResponsesRejectedExtraFieldPrefixPattern = regexp.MustCompile(`(?i)(?:unknown|unsupported)[ _-]+parameter(?:\s*[:=]\s*|\s+is\s+|\s+)`)

// normalizeOpenAIResponsesRejectedExtraFieldRetryBody decides rejections of the
// fields in openAIResponsesRejectedExtraFields. It reads the raw error body,
// keeping case, and runs before any other rejected-field rule.
//
// decided=false hands the response back to the existing rules. decided=true
// ends the decision: retryBody is the body to retry with, or nil to keep the
// request unchanged, so no other rule rewrites a request whose error names, or
// points its param at, one of these fields.
func normalizeOpenAIResponsesRejectedExtraFieldRetryBody(body, responseBody []byte) (retryBody []byte, reason string, decided bool, err error) {
	message, params := openAIResponsesRejectedExtraFieldError(responseBody)
	named := openAIResponsesRejectedExtraFieldNamed(message)
	if named == "" {
		for _, param := range params {
			if isOpenAIResponsesRejectedExtraField(param) {
				// The structured param points at the field but the message does not
				// name it: a value restriction or a conflicting error. Change nothing.
				return nil, "", true, nil
			}
		}
		return nil, "", false, nil
	}
	for _, param := range params {
		if param != named {
			return nil, "", true, nil
		}
	}
	if !gjson.GetBytes(body, named).Exists() {
		return nil, "", true, nil
	}
	retryBody, err = sjson.DeleteBytes(body, named)
	if err != nil {
		return nil, "", true, fmt.Errorf("delete rejected %s: %w", named, err)
	}
	return retryBody, named + " parameter rejection", true, nil
}

// openAIResponsesRejectedExtraFieldError returns the error message to match and
// every error.param found, unwrapping at most one JSON object that an upstream
// put inside the message. Serialized JSON arrays, and wrappers without a string
// message, yield no message, so echoed input is never read as the rejection.
func openAIResponsesRejectedExtraFieldError(responseBody []byte) (string, []string) {
	outer := gjson.ParseBytes(responseBody)
	params := appendOpenAIResponsesRejectedExtraFieldParam(nil, outer.Get("error.param"))
	message, ok := openAIResponsesRejectedExtraFieldMessage(outer)
	if !ok {
		return "", params
	}
	trimmed := strings.TrimSpace(message)
	if strings.HasPrefix(trimmed, "[") && gjson.Valid(trimmed) {
		return "", params
	}
	if !strings.HasPrefix(trimmed, "{") || !gjson.Valid(trimmed) {
		return message, params
	}
	inner := gjson.Parse(trimmed)
	params = appendOpenAIResponsesRejectedExtraFieldParam(params, inner.Get("error.param"))
	innerMessage, ok := openAIResponsesRejectedExtraFieldMessage(inner)
	if !ok {
		return "", params
	}
	innerTrimmed := strings.TrimSpace(innerMessage)
	if (strings.HasPrefix(innerTrimmed, "[") || strings.HasPrefix(innerTrimmed, "{")) && gjson.Valid(innerTrimmed) {
		return "", params
	}
	return innerMessage, params
}

func openAIResponsesRejectedExtraFieldMessage(obj gjson.Result) (string, bool) {
	for _, path := range []string{"error.message", "detail", "message"} {
		if value, ok := openAIResponsesRejectedExtraFieldString(obj.Get(path)); ok {
			return value, true
		}
	}
	return "", false
}

func appendOpenAIResponsesRejectedExtraFieldParam(params []string, value gjson.Result) []string {
	if param, ok := openAIResponsesRejectedExtraFieldString(value); ok {
		return append(params, strings.TrimSpace(param))
	}
	return params
}

func openAIResponsesRejectedExtraFieldString(value gjson.Result) (string, bool) {
	if value.Type != gjson.String || strings.TrimSpace(value.Str) == "" {
		return "", false
	}
	return value.Str, true
}

// openAIResponsesRejectedExtraFieldNamed returns the listed field the message
// names as unsupported or unknown, or "" when it names none of them. The name
// is read whole and compared exactly, so "enable_thinking.mode" or
// "ENABLE_THINKING" never match "enable_thinking".
func openAIResponsesRejectedExtraFieldNamed(message string) string {
	name := openAIResponsesRejectedParamName(message)
	if isOpenAIResponsesRejectedExtraField(name) {
		return name
	}
	return ""
}

func openAIResponsesRejectedParamName(message string) string {
	loc := openAIResponsesRejectedExtraFieldPrefixPattern.FindStringIndex(message)
	if loc == nil {
		return ""
	}
	rest := message[loc[1]:]
	if rest == "" {
		return ""
	}
	switch quote := rest[0]; quote {
	case '\'', '"', '`':
		end := strings.IndexByte(rest[1:], quote)
		if end < 0 {
			return ""
		}
		return rest[1 : 1+end]
	}
	if end := strings.IndexAny(rest, " \t\r\n,;:)"); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimRight(rest, ".")
}

func isOpenAIResponsesRejectedExtraField(name string) bool {
	if name == "" {
		return false
	}
	for _, field := range openAIResponsesRejectedExtraFields {
		if name == field {
			return true
		}
	}
	return false
}
