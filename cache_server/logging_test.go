package main

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestSanitize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"short", "hello", "hello"},
		{"newline", "a\nb", "a b"},
		{"carriage_return", "a\rb", "a b"},
		{"crlf", "a\r\nb", "a  b"},
		{"exactly_64_bytes", strings.Repeat("x", 64), strings.Repeat("x", 64)},
		{"65_bytes", strings.Repeat("x", 65), strings.Repeat("x", 64) + "...(65B)"},
		// The cut is by byte, not by rune: 22×"世" is 66 bytes, so the 64-byte
		// prefix ends mid-rune and leaves a partial UTF-8 sequence.
		{"utf8_split", strings.Repeat("世", 22), strings.Repeat("世", 21) + "\xe4...(66B)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitize(tc.in); got != tc.want {
				t.Errorf("sanitize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestQuoteIfNeeded(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "plain", "plain"},
		{"space", "two words", "\"two words\""},
		{"tab", "a\tb", "\"a\\tb\""},
		{"newline_sanitized_then_quoted", "a\nb", "\"a b\""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := quoteIfNeeded(tc.in); got != tc.want {
				t.Errorf("quoteIfNeeded(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestArgsSummary(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"nil", nil, ""},
		{"set", []string{"SET", "foo", "bar"}, "SET foo bar"},
		{"quoted_arg", []string{"SET", "hello world"}, "SET \"hello world\""},
		{"exactly_eight", []string{"a", "b", "c", "d", "e", "f", "g", "h"}, "a b c d e f g h"},
		{"ten_elided", []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}, "a b c d e f g h ...(+2 args)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := argsSummary(tc.args); got != tc.want {
				t.Errorf("argsSummary(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

func TestReplySummary(t *testing.T) {
	tenInts := make([]Reply, 10)
	for i := range tenInts {
		tenInts[i] = intReply(int64(i))
	}
	tests := []struct {
		name string
		in   Reply
		want string
	}{
		{"simple_string", simpleString("OK"), "+OK"},
		{"error", errReply("ERR x"), "-ERR x"},
		{"integer", intReply(42), ":42"},
		{"negative_integer", intReply(-1), ":-1"},
		{"bulk", bulkReply([]byte("hello")), "$5 hello"},
		{"empty_bulk", bulkReply([]byte("")), "$0 "},
		{"null_bulk", nullReply(), "$-1 (nil)"},
		{"null_array", nullArrayReply(), "*-1 (nil)"},
		{"empty_array", arrayReply(nil), "*0 [...]"},
		{"empty_map", mapReply(nil), "%0 [...]"},
		{"nested_array", arrayReply([]Reply{arrayReply([]Reply{intReply(1)})}), "*1 [*1 [:1]]"},
		{"depth_cap", arrayReply([]Reply{arrayReply([]Reply{arrayReply([]Reply{intReply(1)})})}), "*1 [*1 [*1 [...]]]"},
		{"ten_ints_elided", arrayReply(tenInts), "*10 [:0, :1, :2, :3, :4, :5, :6, :7 ...(+2)]"},
		{"map", mapReply([]Reply{bulkReply([]byte("a")), intReply(1), bulkReply([]byte("b")), intReply(2)}), "%2 [$1 a, :1, $1 b, :2]"},
		{"unknown_kind", Reply{Kind: 'x'}, "?x"},
		{"zero_kind", Reply{}, "?\x00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := replySummary(tc.in); got != tc.want {
				t.Errorf("replySummary(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestLogCommSilentAndActive(t *testing.T) {
	old := commandLog
	t.Cleanup(func() { commandLog = old })

	// Nil logger: logComm must be a no-op.
	commandLog = nil
	logComm("[conn %d] --> %s", 1, "PING")

	var buf bytes.Buffer
	commandLog = log.New(&buf, "", 0)
	logComm("[conn %d] --> %s", 1, "PING")
	if got, want := buf.String(), "[conn 1] --> PING\n"; got != want {
		t.Errorf("log output = %q, want %q", got, want)
	}

	buf.Reset()
	commandLog = nil
	logComm("[conn %d] --> %s", 1, "PING")
	if buf.Len() != 0 {
		t.Errorf("log output with nil logger = %q, want empty", buf.String())
	}
}
