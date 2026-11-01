package service

import "bytes"

// JSONBodyMayContainToken 是必要条件过滤：返回 false 时保证 body 的任何 JSON 字符串
// 解码后都不含 token，调用方可以跳过昂贵的结构解析；返回 true 不代表确实包含。
//
// token 只能是 ASCII。JSON 允许把任意字符写成 \uXXXX，所以原文不含 token 时还要看
// 转义：只有解码后落在 token 字符集里的转义才可能拼出 token。把「出现任何 \u」都
// 当成可能包含的做法会被中文之类的非 ASCII 转义废掉。异常转义一律放行，交给原逻辑。
func JSONBodyMayContainToken(body []byte, token string) bool {
	if bytes.Contains(body, []byte(token)) {
		return true
	}
	return jsonBodyMayEscapeTokenChar(body, token)
}

// jsonBodyMayHaveKey 判断 body 是否可能有名为 key 的字段：字段名原文一定带引号，
// 带转义的字段名由转义检查兜底。比 JSONBodyMayContainToken 更严，正文里提到 key
// 这个词不算（字符串里的引号必须转义，写不成 "key" 的形式）。
func jsonBodyMayHaveKey(body []byte, key string) bool {
	literal := make([]byte, 0, len(key)+2)
	literal = append(literal, '"')
	literal = append(literal, key...)
	literal = append(literal, '"')
	return bytes.Contains(body, literal) || jsonBodyMayEscapeTokenChar(body, key)
}

// jsonBodyMayEscapeTokenChar 判断 body 里是否存在解码后落在 token 字符集的转义，
// 或无法判定的异常转义。
func jsonBodyMayEscapeTokenChar(body []byte, token string) bool {
	var chars [128]bool
	for i := 0; i < len(token); i++ {
		if token[i] < 0x80 {
			chars[token[i]] = true
		}
	}
	for i := 0; ; {
		offset := bytes.IndexByte(body[i:], '\\')
		if offset < 0 {
			return false
		}
		i += offset
		if i+1 >= len(body) {
			return true
		}
		escape := body[i+1]
		if escape == 'u' {
			if i+6 > len(body) {
				return true
			}
			value, ok := parseJSONHex4(body[i+2 : i+6])
			if !ok || (value < 0x80 && chars[value]) {
				return true
			}
			i += 6
			continue
		}
		decoded, ok := jsonShortEscapeValue(escape)
		if !ok || chars[decoded] {
			return true
		}
		i += 2
	}
}

func parseJSONHex4(digits []byte) (uint16, bool) {
	var value uint16
	for _, digit := range digits {
		value <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			value |= uint16(digit - '0')
		case digit >= 'a' && digit <= 'f':
			value |= uint16(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			value |= uint16(digit-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func jsonShortEscapeValue(escape byte) (byte, bool) {
	switch escape {
	case '"', '\\', '/':
		return escape, true
	case 'b':
		return '\b', true
	case 'f':
		return '\f', true
	case 'n':
		return '\n', true
	case 'r':
		return '\r', true
	case 't':
		return '\t', true
	}
	return 0, false
}

// jsonBodyRootIsObjectOrNull 判断跳过 JSON 空白后的根值是否为对象或 null，
// 也就是解码到 map[string]any 不会因类型报错的两种根值。
func jsonBodyRootIsObjectOrNull(body []byte) bool {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	return len(trimmed) > 0 && (trimmed[0] == '{' || bytes.HasPrefix(trimmed, []byte("null")))
}
