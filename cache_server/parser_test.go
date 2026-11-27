package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

// ---------------------------------------------------------------------------
// Encoder tests
// ---------------------------------------------------------------------------

func TestWriteReply(t *testing.T) {
	tests := []struct {
		name             string
		protocol_version int
		in               Reply
		want             string
	}{
		{"simple_string", 2, simpleString("OK"), "+OK\r\n"},
		{"error", 2, errReply("ERR x"), "-ERR x\r\n"},
		{"integer", 2, intReply(42), ":42\r\n"},
		{"negative_integer", 2, intReply(-1), ":-1\r\n"},
		{"bulk", 2, bulkReply([]byte("hello")), "$5\r\nhello\r\n"},
		{"null", 2, nullReply(), "$-1\r\n"},
		{"empty_bulk", 2, bulkReply([]byte("")), "$0\r\n\r\n"},
		{"error_crlf_sanitized", 2, errReply("bad\r\nmsg"), "-bad  msg\r\n"},
		{"array", 2, arrayReply([]Reply{simpleString("OK"), intReply(1)}), "*2\r\n+OK\r\n:1\r\n"},
		{"empty_array", 2, arrayReply([]Reply{}), "*0\r\n"},
		{"null_resp3", 3, nullReply(), "_\r\n"},
		{"array_null_resp3", 3, arrayReply([]Reply{nullReply()}), "*1\r\n_\r\n"},
		{"map_resp3", 3, mapReply([]Reply{bulkReply([]byte("k")), intReply(3)}), "%1\r\n$1\r\nk\r\n:3\r\n"},
		{"map_resp2_flat", 2, mapReply([]Reply{bulkReply([]byte("k")), intReply(3)}), "*2\r\n$1\r\nk\r\n:3\r\n"},
		{"map_null_resp3", 3, mapReply([]Reply{bulkReply([]byte("k")), nullReply()}), "%1\r\n$1\r\nk\r\n_\r\n"},
		{"null_array", 2, nullArrayReply(), "*-1\r\n"},
		{"null_array_resp3", 3, nullArrayReply(), "_\r\n"},
		{"array_null_bulk_resp2", 2, arrayReply([]Reply{nullReply()}), "*1\r\n$-1\r\n"},
		{"array_mixed_nulls_resp3", 3, arrayReply([]Reply{nullReply(), bulkReply([]byte("x"))}), "*2\r\n_\r\n$1\r\nx\r\n"},
		{"nested_null_array_resp3", 3, arrayReply([]Reply{nullArrayReply()}), "*1\r\n_\r\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			protocol_version := tc.protocol_version
			if protocol_version == 0 {
				protocol_version = 2
			}
			var buf bytes.Buffer
			w := bufio.NewWriter(&buf)
			if err := writeReply(w, protocol_version, tc.in); err != nil {
				t.Fatalf("writeReply: %v", err)
			}
			if err := w.Flush(); err != nil {
				t.Fatalf("flush: %v", err)
			}
			if got := buf.String(); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWriteReplyDoesNotFlush(t *testing.T) {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	if err := writeReply(w, 2, simpleString("OK")); err != nil {
		t.Fatalf("writeReply: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("writeReply wrote to the underlying buffer without Flush: %q", buf.String())
	}
}

func TestWriteReplyUnknownKind(t *testing.T) {
	for _, kind := range []byte{'x', 0} {
		var buf bytes.Buffer
		w := bufio.NewWriter(&buf)
		err := writeReply(w, 2, Reply{Kind: kind})
		if err == nil || !strings.Contains(err.Error(), "unknown reply kind") {
			t.Errorf("writeReply(kind %q) err = %v, want error containing %q", kind, err, "unknown reply kind")
		}
	}
}

// ---------------------------------------------------------------------------
// Decoder tests
// ---------------------------------------------------------------------------

func TestReadCommand(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		want       []string
		wantEOF    bool
		wantErrMsg string
	}{
		{"array_set", "*3\r\n$3\r\nSET\r\n$3\r\nfoo\r\n$3\r\nbar\r\n", []string{"SET", "foo", "bar"}, false, ""},
		{"array_get", "*2\r\n$3\r\nGET\r\n$1\r\nk\r\n", []string{"GET", "k"}, false, ""},
		{"inline", "SET foo bar\r\n", []string{"SET", "foo", "bar"}, false, ""},
		{"inline_extra_spaces", "  GET   foo  \r\n", []string{"GET", "foo"}, false, ""},
		{"empty_inline_line", "\r\n", []string{}, false, ""},
		{"empty_value", "*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$0\r\n\r\n", []string{"SET", "k", ""}, false, ""},
		{"value_with_crlf", "*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$7\r\na\r\nb\r\nc\r\n", []string{"SET", "k", "a\r\nb\r\nc"}, false, ""},
		{"array_zero_len", "*0\r\n", nil, false, "invalid multibulk length"},
		{"oversized_bulk", "*3\r\n$3\r\nSET\r\n$3\r\nfoo\r\n$2097153\r\n", nil, false, "invalid bulk length"},
		{"missing_terminator", "*1\r\n$4\r\nPINGxx\r\n", nil, false, "terminator"},
		{"truncated_bulk", "*1\r\n$4\r\nPI", nil, false, "unexpected EOF"},
		{"array_negative_len", "*-1\r\n", nil, false, "invalid multibulk length"},
		{"array_junk_len", "*abc\r\n", nil, false, "invalid multibulk length"},
		{"too_many_args", "*1025\r\n", nil, false, "too many arguments"},
		{"bulk_len_one_over_max", "*2\r\n$3\r\nGET\r\n$1048577\r\n", nil, false, "invalid bulk length"},
		{"negative_bulk_len", "*1\r\n$-1\r\n", nil, false, "invalid bulk length"},
		{"bulk_header_not_dollar", "*1\r\nGET\r\n", nil, false, "expected '$'"},
		{"truncated_array_header", "*2\r", nil, false, "truncated array header"},
		{"truncated_bulk_header", "*1\r\n$4", nil, false, "truncated bulk header"},
		{"inline_tabs", "SET\tfoo\tbar\r\n", []string{"SET", "foo", "bar"}, false, ""},
		{"inline_lf_only", "GET k\n", []string{"GET", "k"}, false, ""},
		{"inline_spaces_only", "   \r\n", []string{}, false, ""},
		{"truncated_inline", "PING", nil, false, "truncated"},
		{"eof", "", nil, true, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := bufio.NewReader(strings.NewReader(tc.input))
			got, err := readCommand(r)
			if tc.wantEOF {
				if !errors.Is(err, io.EOF) {
					t.Fatalf("got err %v, want io.EOF", err)
				}
				return
			}
			if tc.wantErrMsg != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrMsg) {
					t.Fatalf("got err %v, want error containing %q", err, tc.wantErrMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("readCommand: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestReadCommandFragmented(t *testing.T) {
	input := "*3\r\n$3\r\nSET\r\n$3\r\nfoo\r\n$3\r\nbar\r\n"
	r := bufio.NewReader(iotest.OneByteReader(strings.NewReader(input)))
	got, err := readCommand(r)
	if err != nil {
		t.Fatalf("readCommand: %v", err)
	}
	want := []string{"SET", "foo", "bar"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestReadCommandTwoCommands(t *testing.T) {
	input := "*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r\n*2\r\n$3\r\nGET\r\n$1\r\nk\r\n"
	r := bufio.NewReader(strings.NewReader(input))
	for i := 0; i < 2; i++ {
		got, err := readCommand(r)
		if err != nil {
			t.Fatalf("command %d: %v", i, err)
		}
		if i == 0 && !reflect.DeepEqual(got, []string{"SET", "k", "v"}) {
			t.Errorf("command %d: got %#v", i, got)
		}
		if i == 1 && !reflect.DeepEqual(got, []string{"GET", "k"}) {
			t.Errorf("command %d: got %#v", i, got)
		}
	}
	if _, err := readCommand(r); !errors.Is(err, io.EOF) {
		t.Errorf("after both commands: got %v, want io.EOF", err)
	}
}

func TestReadCommandLimits(t *testing.T) {
	t.Run("max_1024_args_ok", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("*1024\r\n")
		for i := 0; i < 1024; i++ {
			b.WriteString("$1\r\na\r\n")
		}
		r := bufio.NewReader(strings.NewReader(b.String()))
		got, err := readCommand(r)
		if err != nil {
			t.Fatalf("readCommand at exactly maxArgs: %v", err)
		}
		if len(got) != 1024 {
			t.Errorf("got %d args, want 1024", len(got))
		}
	})

	t.Run("max_bulk_1mb_ok", func(t *testing.T) {
		payload := strings.Repeat("x", maxBulkLen)
		input := "*1\r\n$1048576\r\n" + payload + "\r\n"
		r := bufio.NewReader(strings.NewReader(input))
		got, err := readCommand(r)
		if err != nil {
			t.Fatalf("readCommand at exactly maxBulkLen: %v", err)
		}
		if len(got) != 1 || len(got[0]) != maxBulkLen {
			t.Errorf("got %d args (first len %d), want 1 arg of length %d", len(got), len(got[0]), maxBulkLen)
		}
	})
}
