package service

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// intentRouteCapture keeps the user text of keyword misses from selected API
// keys, for reviewing whether a client is steering around the keyword rules.
// Hits are already kept in intent_route_hits; misses are the other half.
//
// It is switched by a plain file so no restart is needed: <dir>/api_keys holds
// API key IDs (comma or newline separated) and is re-read every 30 seconds; a
// missing or empty file captures nothing. Records go to <dir>/misses-YYYYMMDD.jsonl
// through a buffered writer, dropped rather than ever slowing a request, and
// writing stops once the directory holds maxBytes.
type intentRouteCapture struct {
	dir      string
	maxBytes int64

	keys     atomic.Pointer[map[int64]struct{}]
	loadedAt atomic.Int64
	loading  atomic.Bool

	queue   chan []byte
	used    atomic.Int64
	dropped atomic.Int64
	once    sync.Once
	now     func() time.Time
}

const (
	intentCaptureKeysTTL      = 30 * time.Second
	intentCaptureQueue        = 4096
	intentCaptureTextRunes    = 8000
	intentCaptureFirstRunes   = 2000
	intentCaptureSystemRunes  = 300
	intentCaptureDefaultLimit = 2 << 30
)

func newIntentRouteCapture(dir string, maxBytes int64) *intentRouteCapture {
	if maxBytes <= 0 {
		maxBytes = intentCaptureDefaultLimit
	}
	c := &intentRouteCapture{dir: dir, maxBytes: maxBytes, queue: make(chan []byte, intentCaptureQueue), now: time.Now}
	empty := map[int64]struct{}{}
	c.keys.Store(&empty)
	return c
}

// newIntentRouteCaptureFromEnv places the capture under the data directory the
// container mounts; INTENT_ROUTE_CAPTURE_MAX_MB caps its size.
func newIntentRouteCaptureFromEnv() *intentRouteCapture {
	dir := strings.TrimSpace(os.Getenv("INTENT_ROUTE_CAPTURE_DIR"))
	if dir == "" {
		dir = filepath.Join("data", "intent_capture")
	}
	var maxBytes int64
	if mb, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("INTENT_ROUTE_CAPTURE_MAX_MB")), 10, 64); err == nil && mb > 0 {
		maxBytes = mb << 20
	}
	return newIntentRouteCapture(dir, maxBytes)
}

// watching reports whether misses of apiKeyID are captured. It never blocks:
// a stale key list is refreshed in the background.
func (c *intentRouteCapture) watching(apiKeyID int64) bool {
	if c == nil || apiKeyID <= 0 {
		return false
	}
	if c.now().UnixNano()-c.loadedAt.Load() > int64(intentCaptureKeysTTL) && c.loading.CompareAndSwap(false, true) {
		go func() {
			defer c.loading.Store(false)
			c.reloadKeys()
		}()
	}
	_, ok := (*c.keys.Load())[apiKeyID]
	return ok
}

func (c *intentRouteCapture) reloadKeys() {
	keys := map[int64]struct{}{}
	if raw, err := os.ReadFile(filepath.Join(c.dir, "api_keys")); err == nil {
		for _, field := range strings.FieldsFunc(string(raw), func(r rune) bool {
			return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t'
		}) {
			if id, err := strconv.ParseInt(field, 10, 64); err == nil && id > 0 {
				keys[id] = struct{}{}
			}
		}
	}
	c.keys.Store(&keys)
	c.loadedAt.Store(c.now().UnixNano())
	if len(keys) > 0 {
		c.once.Do(c.start)
	}
}

// IntentRouteMiss is one captured keyword miss.
type IntentRouteMiss struct {
	Time        time.Time `json:"time"`
	RequestID   string    `json:"request_id"`
	GroupID     int64     `json:"group_id"`
	UserID      int64     `json:"user_id"`
	APIKeyID    int64     `json:"api_key_id"`
	Endpoint    string    `json:"endpoint"`
	Model       string    `json:"model"`
	BodyBytes   int       `json:"body_bytes"`
	Turns       int       `json:"turns"`
	UserImages  int       `json:"user_images"`
	SystemChars int       `json:"system_chars"`
	SystemHead  string    `json:"system_head,omitempty"`
	// Checked are the texts the keyword rules were matched against, newest
	// first, after injected client blocks were removed.
	Checked []string `json:"checked"`
	// RawLastUser is the newest user turn before that removal, so text hidden
	// inside injected-looking blocks stays visible.
	RawLastUser string `json:"raw_last_user"`
	FirstUser   string `json:"first_user,omitempty"`
}

func (c *intentRouteCapture) add(in IntentRouteInput, view intentRequestView, checked []string) {
	miss := IntentRouteMiss{
		Time:        c.now(),
		GroupID:     in.GroupID,
		UserID:      in.UserID,
		APIKeyID:    in.APIKeyID,
		Endpoint:    in.Endpoint,
		Model:       gjson.GetBytes(view.body, "model").String(),
		BodyBytes:   len(view.body),
		Turns:       len(view.turns()),
		RawLastUser: truncateRunes(view.rawLastUserText(), intentCaptureTextRunes),
		FirstUser:   truncateRunes(view.firstUserText(), intentCaptureFirstRunes),
	}
	if id := strings.TrimSpace(in.ClientRequestID); id != "" {
		miss.RequestID = "client:" + id
	}
	system := view.systemText()
	miss.SystemChars = utf8.RuneCountInString(system)
	miss.SystemHead = truncateRunes(system, intentCaptureSystemRunes)
	miss.UserImages = view.lastUserImages()
	for _, text := range checked {
		miss.Checked = append(miss.Checked, truncateRunes(text, intentCaptureTextRunes))
	}
	line, err := json.Marshal(miss)
	if err != nil {
		return
	}
	select {
	case c.queue <- append(line, '\n'):
	default:
		c.dropped.Add(1)
	}
}

func (c *intentRouteCapture) start() {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		slog.Warn("intent_route.capture_dir_failed", "dir", c.dir, "error", err)
	}
	if entries, err := os.ReadDir(c.dir); err == nil {
		var used int64
		for _, entry := range entries {
			if info, err := entry.Info(); err == nil && strings.HasPrefix(entry.Name(), "misses-") {
				used += info.Size()
			}
		}
		c.used.Store(used)
	}
	go c.run()
}

func (c *intentRouteCapture) run() {
	var (
		file *os.File
		w    *bufio.Writer
		day  string
	)
	flush := time.NewTicker(2 * time.Second)
	defer flush.Stop()
	for {
		select {
		case line := <-c.queue:
			if c.used.Load()+int64(len(line)) > c.maxBytes {
				c.dropped.Add(1)
				continue
			}
			if today := c.now().Format("20060102"); today != day || file == nil {
				if w != nil {
					_ = w.Flush()
					_ = file.Close()
				}
				f, err := os.OpenFile(filepath.Join(c.dir, "misses-"+today+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
				if err != nil {
					slog.Warn("intent_route.capture_open_failed", "error", err)
					file, w = nil, nil
					continue
				}
				file, w, day = f, bufio.NewWriterSize(f, 256<<10), today
			}
			if n, err := w.Write(line); err == nil {
				c.used.Add(int64(n))
			}
		case <-flush.C:
			if w != nil {
				_ = w.Flush()
			}
		}
	}
}

// rawLastUserText is lastUserText without removing injected client blocks.
func (v intentRequestView) rawLastUserText() string {
	turns := v.turns()
	for i := len(turns) - 1; i >= 0; i-- {
		if !isIntentUserRole(turns[i].Get("role").String()) {
			continue
		}
		if text := intentTurnText(turns[i]); text != "" {
			return text
		}
	}
	if r := gjson.GetBytes(v.body, "input"); r.Type == gjson.String {
		return r.String()
	}
	return ""
}

// lastUserImages counts image blocks in the newest user turn that has content.
func (v intentRequestView) lastUserImages() int {
	turns := v.turns()
	for i := len(turns) - 1; i >= 0; i-- {
		if !isIntentUserRole(turns[i].Get("role").String()) {
			continue
		}
		content := turns[i].Get("content")
		if !content.IsArray() {
			if content.Exists() {
				return 0
			}
			continue
		}
		images := 0
		for _, block := range content.Array() {
			switch block.Get("type").String() {
			case "input_image", "image_url", "image":
				images++
			}
		}
		return images
	}
	return 0
}
