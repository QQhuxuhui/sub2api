// Package jsondupkey finds duplicate member names in JSON request bodies.
//
// The gateway reads request fields with gjson, which picks the first of two
// duplicate keys, while upstreams decoding with encoding/json or Python pick
// the last one. A body such as {"model":"cheap",...,"model":"expensive"} is
// then billed as one model and served as another. Find reports such bodies so
// the gateway can reject them before routing, billing and forwarding.
//
// The scanner is a single non-recursive pass with a depth limit. It is lenient
// where the gateway's lenient request path is lenient (raw control bytes in
// strings, literal spelling) and strict about structure.
package jsondupkey

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Result classifies a scanned body.
type Result int

const (
	// ResultOK: no duplicate key, or the body does not start with an object or
	// array and is not this package's concern.
	ResultOK Result = iota
	// ResultDuplicate: an object holds the same member name twice.
	ResultDuplicate
	// ResultTooDeep: nesting exceeds MaxDepth.
	ResultTooDeep
	// ResultInvalid: the body starts like JSON but is structurally malformed.
	ResultInvalid
	// ResultTooManyKeys: the objects open at one point of the scan hold more
	// than MaxHeldKeys member names, which no real request needs.
	ResultTooManyKeys
)

// MaxHeldKeys bounds the member names the scanner keeps at once (the names of
// every object still open), and with it the memory a wide object can make the
// scan hold.
const MaxHeldKeys = 100000

// MaxHeldKeyBytes bounds the total size of those names.
const MaxHeldKeyBytes = 16 << 20

// MaxDepth matches the nesting limit of encoding/json.
const MaxDepth = 10000

// MaxComparedKeyBytes is the longest raw member name (as written, escapes
// included) that is decoded and compared. Longer names cannot be any request
// field the gateway reads, and decoding them would cost memory proportional
// to client input.
const MaxComparedKeyBytes = 4096

// longKeyPathBytes is how much of an uncompared long name appears in a path.
const longKeyPathBytes = 64

// MaxPathBytes bounds the JSON Pointer returned for a duplicate key, so a
// client-chosen key name cannot inflate error responses or log lines.
const MaxPathBytes = 256

// Options adjusts which subtrees are treated as user content.
type Options struct {
	// SkipRootContent also skips the root "content" member (Seedance task
	// bodies carry user text and media there).
	SkipRootContent bool
}

// rootSkipKeys hold user content at the root object: messages, Responses
// input, Gemini contents and system prompts. Their subtrees are not checked.
var rootSkipKeys = map[string]struct{}{
	"messages":           {},
	"input":              {},
	"contents":           {},
	"system":             {},
	"systemInstruction":  {},
	"system_instruction": {},
	"instructions":       {},
	"prompt":             {},
}

// generateContentRequestSkipKeys hold user content inside the root
// generateContentRequest wrapper of Gemini countTokens.
var generateContentRequestSkipKeys = map[string]struct{}{
	"contents":           {},
	"systemInstruction":  {},
	"system_instruction": {},
}

// nestedSkipKeys hold caller-written schemas and examples below the root
// object (tool parameters, structured-output schemas, tool input examples).
var nestedSkipKeys = map[string]struct{}{
	"parameters":           {},
	"input_schema":         {},
	"parametersJsonSchema": {},
	"responseSchema":       {},
	"responseJsonSchema":   {},
	"response_schema":      {},
	"response_json_schema": {},
	"schema":               {},
	"input_examples":       {},
}

// LeadingByte returns the first byte after a UTF-8 BOM and leading white
// space, using the same notion of white space as bytes.TrimSpace (Unicode
// spaces included). ok is false when nothing but white space is present.
func LeadingByte(body []byte) (byte, bool) {
	rest := trimLeft(body)
	if len(rest) == 0 {
		return 0, false
	}
	return rest[0], true
}

// LeadingSpaceEnd scans body from offset from, which must be a value it
// returned earlier (0 at first), over a leading UTF-8 BOM and white space as
// LeadingByte counts them. It returns the offset where scanning stopped and
// whether more input is needed to decide: true when everything so far is BOM
// or white space (possibly ending in an incomplete rune), false when end is
// the offset of the first significant byte. Feeding it a growing buffer with
// the previous end keeps the total work linear.
func LeadingSpaceEnd(body []byte, from int) (end int, more bool) {
	i := from
	if i == 0 {
		switch {
		case bytes.HasPrefix(body, utf8BOM):
			i = len(utf8BOM)
		case len(body) < len(utf8BOM) && bytes.HasPrefix(utf8BOM, body):
			return 0, true
		}
	}
	for i < len(body) {
		r, size := utf8.DecodeRune(body[i:])
		if r == utf8.RuneError && size <= 1 && !utf8.FullRune(body[i:]) {
			return i, true
		}
		if !unicode.IsSpace(r) {
			return i, false
		}
		i += size
	}
	return i, true
}

var utf8BOM = []byte("\xef\xbb\xbf")

func trimLeft(body []byte) []byte {
	body = bytes.TrimPrefix(body, utf8BOM)
	return bytes.TrimLeftFunc(body, unicode.IsSpace)
}

// Find scans body with default options. See FindWithOptions.
func Find(body []byte) (result Result, path string, truncated bool, complete bool) {
	return FindWithOptions(body, Options{})
}

// FindWithOptions scans body for duplicate member names.
//
// path is the JSON Pointer of the first duplicate (at most MaxPathBytes, cut
// on a rune boundary and marked with "…" when truncated). complete reports
// whether the whole body is one structurally valid JSON value followed only
// by white space. A duplicate wins over later structural errors; nesting
// beyond MaxDepth stops the scan at once.
func FindWithOptions(body []byte, opts Options) (result Result, path string, truncated bool, complete bool) {
	data := trimLeft(body)
	if len(data) == 0 || (data[0] != '{' && data[0] != '[') {
		return ResultOK, "", false, false
	}
	s := scanner{data: data, opts: opts}
	return s.run()
}

type frame struct {
	object bool
	skip   bool
	// key is the member whose value is being scanned (objects); index is the
	// element position (arrays).
	key   string
	index int
	// keyInParent is the member name under which this container sits.
	keyInParent string
	// keys holds the names seen so far, as compared: folded at the root,
	// as decoded elsewhere. table replaces keys once it grows past
	// linearKeyLimit.
	keys  []string
	table map[string]struct{}
	count int
	bytes int
}

type scanner struct {
	data  []byte
	pos   int
	opts  Options
	stack []frame

	held      int
	heldBytes int
	tooMany   bool
	dupFound  bool
	dupPath   string
	dupTrunc  bool
}

type state int

const (
	stateValue state = iota
	stateKeyOrEnd
	stateKey
	stateColon
	stateAfterValue
	stateValueOrEnd
)

func (s *scanner) run() (Result, string, bool, bool) {
	st := stateValue
	for {
		s.skipWS()
		switch st {
		case stateValue, stateValueOrEnd:
			if s.pos >= len(s.data) {
				return s.invalid()
			}
			c := s.data[s.pos]
			if st == stateValueOrEnd && c == ']' {
				s.pos++
				s.pop()
				if len(s.stack) == 0 {
					return s.finish()
				}
				st = stateAfterValue
				continue
			}
			switch c {
			case '{', '[':
				if len(s.stack) >= MaxDepth {
					return ResultTooDeep, "", false, false
				}
				s.push(c == '{')
				s.pos++
				if c == '{' {
					st = stateKeyOrEnd
				} else {
					st = stateValueOrEnd
				}
				continue
			case '"':
				if _, ok := s.scanString(); !ok {
					return s.invalid()
				}
			default:
				if !s.scanLiteral() {
					return s.invalid()
				}
			}
			if len(s.stack) == 0 {
				return s.finish()
			}
			st = stateAfterValue
		case stateKeyOrEnd, stateKey:
			if s.pos >= len(s.data) {
				return s.invalid()
			}
			c := s.data[s.pos]
			if st == stateKeyOrEnd && c == '}' {
				s.pos++
				s.pop()
				if len(s.stack) == 0 {
					return s.finish()
				}
				st = stateAfterValue
				continue
			}
			if c != '"' {
				return s.invalid()
			}
			raw, ok := s.scanString()
			if !ok {
				return s.invalid()
			}
			s.addKey(raw)
			if s.tooMany {
				return ResultTooManyKeys, "", false, false
			}
			st = stateColon
		case stateColon:
			if s.pos >= len(s.data) || s.data[s.pos] != ':' {
				return s.invalid()
			}
			s.pos++
			st = stateValue
		case stateAfterValue:
			if s.pos >= len(s.data) {
				return s.invalid()
			}
			top := &s.stack[len(s.stack)-1]
			c := s.data[s.pos]
			switch {
			case c == ',':
				s.pos++
				if top.object {
					st = stateKey
				} else {
					top.index++
					st = stateValue
				}
			case c == '}' && top.object, c == ']' && !top.object:
				s.pos++
				s.pop()
				if len(s.stack) == 0 {
					return s.finish()
				}
				st = stateAfterValue
			default:
				return s.invalid()
			}
		}
	}
}

func (s *scanner) skipWS() {
	for s.pos < len(s.data) {
		switch s.data[s.pos] {
		case ' ', '\t', '\n', '\r':
			s.pos++
		default:
			return
		}
	}
}

// finish runs after the root value closed: only white space may follow.
func (s *scanner) finish() (Result, string, bool, bool) {
	complete := len(bytes.TrimRightFunc(s.data[s.pos:], unicode.IsSpace)) == 0
	if s.dupFound {
		return ResultDuplicate, s.dupPath, s.dupTrunc, complete
	}
	if !complete {
		return ResultInvalid, "", false, false
	}
	return ResultOK, "", false, true
}

func (s *scanner) invalid() (Result, string, bool, bool) {
	if s.dupFound {
		return ResultDuplicate, s.dupPath, s.dupTrunc, false
	}
	return ResultInvalid, "", false, false
}

// scanString scans a string starting at the opening quote and returns its raw
// content without quotes. Raw control bytes are accepted.
func (s *scanner) scanString() ([]byte, bool) {
	start := s.pos + 1
	i := start
	for {
		j := bytes.IndexByte(s.data[i:], '"')
		if j < 0 {
			return nil, false
		}
		end := i + j
		backslashes := 0
		for k := end - 1; k >= start && s.data[k] == '\\'; k-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			s.pos = end + 1
			return s.data[start:end], true
		}
		i = end + 1
	}
}

func (s *scanner) scanLiteral() bool {
	start := s.pos
	for s.pos < len(s.data) {
		c := s.data[s.pos]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '+' || c == '-' || c == '.' {
			s.pos++
			continue
		}
		break
	}
	return s.pos > start
}

func (s *scanner) push(object bool) {
	f := frame{object: object}
	if n := len(s.stack); n > 0 {
		parent := &s.stack[n-1]
		f.skip = parent.skip
		if parent.object {
			f.keyInParent = parent.key
			if !f.skip {
				f.skip = s.skipChild(n-1, parent.key)
			}
		}
	}
	s.stack = append(s.stack, f)
}

// skipChild decides whether the value under key of the object at stack
// position idx is user content or a caller-written schema.
func (s *scanner) skipChild(idx int, key string) bool {
	if idx == 0 {
		if _, ok := rootSkipKeys[key]; ok {
			return true
		}
		return s.opts.SkipRootContent && key == "content"
	}
	if idx == 1 && s.stack[0].object && s.stack[1].keyInParent == "generateContentRequest" {
		if _, ok := generateContentRequestSkipKeys[key]; ok {
			return true
		}
	}
	_, ok := nestedSkipKeys[key]
	return ok
}

func (s *scanner) pop() {
	n := len(s.stack) - 1
	s.held -= s.stack[n].count
	s.heldBytes -= s.stack[n].bytes
	// Clear the slot so the backing array does not keep the closed object's
	// names alive.
	s.stack[n] = frame{}
	s.stack = s.stack[:n]
}

const linearKeyLimit = 16

// addKey records a member name of the current object and checks it against
// the names seen so far. Only the first duplicate is recorded.
func (s *scanner) addKey(raw []byte) {
	top := &s.stack[len(s.stack)-1]
	if top.skip || s.dupFound {
		// Skipped subtrees are scanned for structure only, and after the first
		// duplicate no further names are compared.
		top.key = ""
		return
	}
	if len(raw) > MaxComparedKeyBytes {
		// No request field the gateway bills or routes on has a name this long
		// (even fully \u-escaped), so such names are not compared. Skipping them
		// also bounds the memory spent decoding client-chosen names.
		top.key = string(raw[:longKeyPathBytes]) + "…"
		return
	}
	key := decodeKey(raw)
	top.key = key
	root := len(s.stack) == 1
	if s.seen(top, key, root) {
		s.recordDuplicate(key)
		return
	}
	s.remember(top, key, root)
}

// seen reports whether key repeats a name of the object. At the root, names
// equal under case folding count as repeats, since struct binding upstream
// matches them; the root table therefore holds folded names only.
func (s *scanner) seen(top *frame, key string, root bool) bool {
	if top.table != nil {
		if root {
			key = foldKey(key)
		}
		_, ok := top.table[key]
		return ok
	}
	for _, k := range top.keys {
		if k == key || (root && strings.EqualFold(k, key)) {
			return true
		}
	}
	return false
}

func (s *scanner) remember(top *frame, key string, root bool) {
	top.count++
	top.bytes += len(key)
	s.held++
	s.heldBytes += len(key)
	if s.held > MaxHeldKeys || s.heldBytes > MaxHeldKeyBytes {
		s.tooMany = true
		return
	}
	if top.table == nil {
		top.keys = append(top.keys, key)
		if len(top.keys) <= linearKeyLimit {
			return
		}
		top.table = make(map[string]struct{}, len(top.keys)*2)
		for _, k := range top.keys {
			if root {
				k = foldKey(k)
			}
			top.table[k] = struct{}{}
		}
		top.keys = nil
		return
	}
	if root {
		key = foldKey(key)
	}
	top.table[key] = struct{}{}
}

// foldKey maps every rune to the smallest rune of its simple case-folding
// orbit, so two strings are strings.EqualFold exactly when their fold keys
// are equal.
func foldKey(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if ascii {
		// The smallest rune in an ASCII letter's orbit is its upper case
		// (K/k/U+212A and S/s/U+017F included).
		return strings.ToUpper(s)
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		min := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < min {
				min = f
			}
		}
		_, _ = b.WriteRune(min)
	}
	return b.String()
}

// decodeKey returns the member name as a decoder would see it. Raw control
// bytes are first escaped the way httputil.NormalizeLenientJSONRequestBody
// does, then JSON escapes are decoded.
func decodeKey(raw []byte) string {
	needsWork := false
	for _, c := range raw {
		if c == '\\' || c < 0x20 || c == 0x7f {
			needsWork = true
			break
		}
	}
	if !needsWork {
		return string(raw)
	}
	normalized := make([]byte, 0, len(raw)+8)
	normalized = append(normalized, '"')
	for _, c := range raw {
		if c < 0x20 || c == 0x7f {
			const hex = "0123456789abcdef"
			normalized = append(normalized, '\\', 'u', '0', '0', hex[c>>4], hex[c&0x0f])
			continue
		}
		normalized = append(normalized, c)
	}
	normalized = append(normalized, '"')
	var decoded string
	if err := json.Unmarshal(normalized, &decoded); err == nil {
		return decoded
	}
	// Not decodable even after normalization (a bad escape): the lenient
	// request path rejects such a body anyway; keep the raw bytes, not the
	// expanded form.
	return string(raw)
}

func (s *scanner) recordDuplicate(key string) {
	var b strings.Builder
	for i := range s.stack[:len(s.stack)-1] {
		f := &s.stack[i]
		_ = b.WriteByte('/')
		if f.object {
			_, _ = b.WriteString(escapePointer(f.key))
		} else {
			_, _ = b.WriteString(strconv.Itoa(f.index))
		}
		if b.Len() > MaxPathBytes {
			break
		}
	}
	_ = b.WriteByte('/')
	_, _ = b.WriteString(escapePointer(key))
	s.dupFound = true
	s.dupPath, s.dupTrunc = truncatePath(b.String())
}

func escapePointer(segment string) string {
	if !strings.ContainsAny(segment, "~/") {
		return segment
	}
	segment = strings.ReplaceAll(segment, "~", "~0")
	return strings.ReplaceAll(segment, "/", "~1")
}

func truncatePath(path string) (string, bool) {
	if len(path) <= MaxPathBytes {
		return path, false
	}
	const marker = "…"
	cut := MaxPathBytes - len(marker)
	for cut > 0 && !utf8.RuneStart(path[cut]) {
		cut--
	}
	return path[:cut] + marker, true
}
