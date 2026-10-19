package firstbyte

import (
	"context"
	"testing"
	"time"
)

func TestTrackerSkipsSSEKeepaliveAndRecordsFirstContent(t *testing.T) {
	start := time.Now().Add(-500 * time.Millisecond)
	tr := New(start)

	tr.MarkWrite(nil)
	tr.MarkWrite([]byte(": keepalive\n\n"))
	tr.MarkWrite([]byte(": ping\n\n"))
	tr.MarkWrite([]byte(":\n\n"))
	if _, ok := tr.ClientFirstByteMs(); ok {
		t.Fatalf("keepalive comments must not count as first byte")
	}
	if got := tr.SkippedKeepalives(); got != 3 {
		t.Fatalf("expected 3 skipped keepalives, got %d", got)
	}

	tr.MarkWrite([]byte("event: message_start\ndata: {}\n\n"))
	ms, ok := tr.ClientFirstByteMs()
	if !ok {
		t.Fatalf("expected first byte to be recorded")
	}
	if ms < 500 || ms > 5000 {
		t.Fatalf("unexpected first byte latency %dms", ms)
	}

	// 后续写出不改变首字节时刻。
	first, _ := tr.FirstByteAt()
	time.Sleep(2 * time.Millisecond)
	tr.MarkWrite([]byte("data: more\n\n"))
	again, _ := tr.FirstByteAt()
	if !first.Equal(again) {
		t.Fatalf("first byte timestamp must be immutable")
	}
}

func TestTrackerNonStreamBodyCountsImmediately(t *testing.T) {
	tr := New(time.Now())
	tr.MarkWrite([]byte(`{"id":"msg_1"}`))
	if _, ok := tr.ClientFirstByteMs(); !ok {
		t.Fatalf("json body must count as first byte")
	}
}

func TestIsSSECommentOnly(t *testing.T) {
	cases := map[string]bool{
		": keepalive\n\n":                  true,
		": ping\r\n\r\n":                   true,
		"\n\n":                             false,
		"":                                 false,
		"data: {}\n\n":                     false,
		": comment\ndata: {}\n\n":          false,
		"event: ping\ndata: {\"x\":1}\n\n": false,
	}
	for in, want := range cases {
		if got := isSSECommentOnly([]byte(in)); got != want {
			t.Errorf("isSSECommentOnly(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestContextRoundTrip(t *testing.T) {
	if FromContext(context.Background()) != nil {
		t.Fatalf("expected nil tracker on empty context")
	}
	if ClientFirstByteMsFromContext(context.Background()) != nil {
		t.Fatalf("expected nil ms on empty context")
	}
	tr := New(time.Now())
	ctx := WithTracker(context.Background(), tr)
	if FromContext(ctx) != tr {
		t.Fatalf("tracker not round-tripped through context")
	}
	if ClientFirstByteMsFromContext(ctx) != nil {
		t.Fatalf("expected nil before first byte")
	}
	tr.MarkNow()
	if ClientFirstByteMsFromContext(ctx) == nil {
		t.Fatalf("expected ms after first byte")
	}
	var nilTracker *Tracker
	nilTracker.MarkWrite([]byte("x"))
	if _, ok := nilTracker.ClientFirstByteMs(); ok {
		t.Fatalf("nil tracker must be a no-op")
	}
}
