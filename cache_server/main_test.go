package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"
)

func newTestStore() *MemoryStore {
	return NewMemoryStore()
}

func setKeys(s *MemoryStore, keys map[string][]byte) {
	for k, v := range keys {
		s.Set(k, v, 0)
	}
}

// ---------------------------------------------------------------------------
// Store tests
// ---------------------------------------------------------------------------

func TestSet(t *testing.T) {
	oneMB := make([]byte, 1<<20)
	for i := range oneMB {
		oneMB[i] = 'A'
	}

	tests := []struct {
		name    string
		key     string
		value   []byte
		wantErr bool
	}{
		{"set_new_key", "foo", []byte("bar"), false},
		{"set_empty_key", "", []byte("val"), false},
		{"set_empty_value", "key", []byte(""), false},
		{"set_both_empty", "", []byte(""), false},
		{"set_overwrite_existing", "foo", []byte("new"), false},
		{"set_unicode_key", "키", []byte("값"), false},
		{"set_large_value", "big", oneMB, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore()

			if tc.name == "set_overwrite_existing" {
				s.Set("foo", []byte("old"), 0)
			}

			if err := s.Set(tc.key, tc.value, 0); (err != nil) != tc.wantErr {
				t.Fatalf("Set() error = %v, wantErr %v", err, tc.wantErr)
			}

			gotVal, ok := s.Get(tc.key)
			if !ok {
				t.Fatalf("Get(%q) returned exists=false after Set", tc.key)
			}
			if string(gotVal) != string(tc.value) {
				t.Errorf("Get(%q) = %q, want %q", tc.key, gotVal, tc.value)
			}
		})
	}
}

func TestSetTTL(t *testing.T) {
	s := newTestStore()
	if err := s.Set("k", []byte("v"), 60_000); err != nil {
		t.Fatalf("Set: %v", err)
	}
	s.mu.RLock()
	e, ok := s.data["k"]
	s.mu.RUnlock()
	if !ok {
		t.Fatal("key missing after Set with ttl")
	}
	if e.expiresAt == 0 {
		t.Error("Set with ttl did not record an expiry timestamp")
	}

	// a plain SET overwrites the key and clears the TTL
	if err := s.Set("k", []byte("v2"), 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	s.mu.RLock()
	e = s.data["k"]
	s.mu.RUnlock()
	if e.expiresAt != 0 {
		t.Errorf("plain Set kept expiresAt = %d, want 0", e.expiresAt)
	}
}

func TestGet(t *testing.T) {
	tests := []struct {
		name      string
		key       string
		setupKeys map[string][]byte
		wantValue []byte
		wantExist bool
	}{
		{"get_existing_key", "foo", map[string][]byte{"foo": []byte("bar")}, []byte("bar"), true},
		{"get_missing_key", "nope", map[string][]byte{}, nil, false},
		{"get_empty_value", "empty", map[string][]byte{"empty": []byte("")}, []byte(""), true},
		{"get_empty_key", "", map[string][]byte{"": []byte("found")}, []byte("found"), true},
		{"get_empty_key_missing", "", map[string][]byte{}, nil, false},
		{"get_after_delete", "temp", map[string][]byte{"temp": []byte("val")}, nil, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore()
			setKeys(s, tc.setupKeys)

			if tc.name == "get_after_delete" {
				s.Delete("temp")
			}

			gotVal, gotExist := s.Get(tc.key)
			if gotExist != tc.wantExist {
				t.Errorf("Get(%q) exists = %v, want %v", tc.key, gotExist, tc.wantExist)
			}
			if tc.wantExist && string(gotVal) != string(tc.wantValue) {
				t.Errorf("Get(%q) = %q, want %q", tc.key, gotVal, tc.wantValue)
			}
			if !tc.wantExist && gotVal != nil {
				t.Errorf("Get(%q) = %v, want nil for missing key", tc.key, gotVal)
			}
		})
	}
}

func TestDelete(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		setupKeys    map[string][]byte
		wantRet      uint64
		wantErr      bool
		verifyKey    string
		verifyExists bool
	}{
		{"delete_existing_key", "foo", map[string][]byte{"foo": []byte("bar")}, 1, false, "foo", false},
		{"delete_missing_key", "nope", map[string][]byte{}, 0, false, "nope", false},
		{"delete_empty_key", "", map[string][]byte{"": []byte("val")}, 1, false, "", false},
		{"delete_then_set_again", "x", map[string][]byte{"x": []byte("1")}, 1, false, "x", true},
		{"delete_nonexistent_empty_key", "", map[string][]byte{}, 0, false, "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore()
			setKeys(s, tc.setupKeys)

			ret, err := s.Delete(tc.key)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Delete() error = %v, wantErr %v", err, tc.wantErr)
			}
			if ret != tc.wantRet {
				t.Errorf("Delete() = %d, want %d", ret, tc.wantRet)
			}

			if tc.name == "delete_then_set_again" {
				s.Set("x", []byte("2"), 0)
			}

			_, exists := s.Get(tc.verifyKey)
			if exists != tc.verifyExists {
				t.Errorf("Get(%q) exists = %v after Delete, want %v", tc.verifyKey, exists, tc.verifyExists)
			}

			if tc.name == "delete_then_set_again" {
				val, _ := s.Get("x")
				if string(val) != "2" {
					t.Errorf("Get(%q) = %q, want %q after re-set", "x", val, "2")
				}
			}
		})
	}
}

func TestExpiry(t *testing.T) {
	s := newTestStore()
	now := time.Now().UnixMilli()
	s.data["expired"] = entry{value: []byte("v"), expiresAt: now - 100}
	s.data["alive"] = entry{value: []byte("v"), expiresAt: now + 60_000}

	if _, ok := s.Get("expired"); ok {
		t.Error("Get on expired key returned a value")
	}
	if n, _ := s.Delete("expired"); n != 0 {
		t.Errorf("Delete on expired key = %d, want 0", n)
	}
	if _, ok := s.Get("alive"); !ok {
		t.Error("Get on live TTL key returned no value")
	}

	// Setting over an expired key replaces it cleanly
	if err := s.Set("expired", []byte("fresh"), 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v, ok := s.Get("expired"); !ok || string(v) != "fresh" {
		t.Errorf("Get after Set on expired key = %q, %v", v, ok)
	}
}

func TestConcurrentAccess(t *testing.T) {
	t.Run("concurrent_unique_keys", func(t *testing.T) {
		s := newTestStore()
		const numGoroutines = 100
		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(idx int) {
				defer wg.Done()
				key := strings.Repeat("k", idx+1) // unique key per goroutine
				val := []byte(strings.Repeat("v", idx+1))

				s.Set(key, val, 0)
				got, ok := s.Get(key)
				if ok && string(got) != string(val) {
					t.Errorf("goroutine %d: Get(%q) = %q, want %q", idx, key, got, val)
				}
				s.Delete(key)
			}(i)
		}
		wg.Wait()
	})

	t.Run("concurrent_same_key", func(t *testing.T) {
		s := newTestStore()
		const numGoroutines = 50
		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(idx int) {
				defer wg.Done()
				s.Set("shared-key", []byte(strings.Repeat("v", idx+1)), 0)
			}(i)
		}
		wg.Wait()

		val, ok := s.Get("shared-key")
		if !ok {
			t.Fatal("Get(\"shared-key\") exists = false after concurrent writes")
		}
		if val == nil {
			t.Fatal("Get(\"shared-key\") returned nil value after concurrent writes")
		}
	})
}

// ---------------------------------------------------------------------------
// Encoder tests
// ---------------------------------------------------------------------------

func TestWriteReply(t *testing.T) {
	tests := []struct {
		name  string
		protocol_version int
		in    Reply
		want  string
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

// ---------------------------------------------------------------------------
// Dispatcher tests
// ---------------------------------------------------------------------------

func TestDispatch(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want Reply
	}{
		{"empty", []string{}, errReply("ERR empty command")},
		{"set_too_few", []string{"set"}, errReply("ERR wrong number of arguments for 'SET' command")},
		{"get_too_few", []string{"GET"}, errReply("ERR wrong number of arguments for 'GET' command")},
		{"get_too_many", []string{"GET", "a", "b"}, errReply("ERR wrong number of arguments for 'GET' command")},
		{"del_too_few", []string{"DEL"}, errReply("ERR wrong number of arguments for 'DEL' command")},
		{"client_too_few", []string{"CLIENT"}, errReply("ERR wrong number of arguments for 'CLIENT' command")},
		{"unknown", []string{"PING"}, errReply("ERR unknown command 'PING'")},
		{"get_missing", []string{"get", "k"}, nullReply()},
		{"set_lowercase", []string{"SeT", "k", "v"}, simpleString("OK")},
		{"set_ex", []string{"SET", "k", "v", "EX", "100"}, simpleString("OK")},
		{"set_px", []string{"SET", "k", "v", "PX", "1000"}, simpleString("OK")},
		{"set_ex_px_conflict", []string{"SET", "k", "v", "EX", "1", "PX", "1"}, errReply("ERR syntax error")},
		{"set_missing_ex_value", []string{"SET", "k", "v", "EX"}, errReply("ERR syntax error")},
		{"set_bad_ex", []string{"SET", "k", "v", "EX", "0"}, errReply("ERR invalid expire time in 'set' command")},
		{"set_syntax", []string{"SET", "k", "v", "BOGUS"}, errReply("ERR syntax error")},
		{"client_setinfo", []string{"CLIENT", "SETINFO", "LIB-NAME", "redis-py"}, simpleString("OK")},
		{"client_setinfo_arity", []string{"CLIENT", "SETINFO", "x"}, errReply("ERR wrong number of arguments for 'client|setinfo' command")},
		{"client_unknown_sub", []string{"CLIENT", "FOO"}, errReply("ERR Unknown subcommand or wrong number of arguments for 'FOO'. Try CLIENT HELP.")},
		{"hello_bad_proto", []string{"HELLO", "4"}, errReply("NOPROTO unsupported protocol version")},
		{"hello_proto_junk", []string{"HELLO", "abc"}, errReply("NOPROTO unsupported protocol version")},
		{"hello_auth", []string{"HELLO", "3", "AUTH", "default", "secret"}, errReply("ERR AUTH <password> called without any password configured for the default user. Are you sure your configuration is correct?")},
		{"hello_setname", []string{"HELLO", "3", "SETNAME", "me"}, errReply("ERR unsupported HELLO option 'SETNAME'")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := dispatch(newTestStore(), newClientState(), tc.args)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestHelloDispatch(t *testing.T) {
	s := newTestStore()
	state := newClientState()

	// HELLO with no args replies in the current protocol and does not switch it
	if got := dispatch(s, state, []string{"HELLO"}); !reflect.DeepEqual(got, helloReply(state)) {
		t.Errorf("HELLO reply = %+v, want %+v", got, helloReply(state))
	}
	if state.protocol_version != 2 {
		t.Errorf("protocol_version after bare HELLO = %d, want 2", state.protocol_version)
	}

	// HELLO 3 switches the connection to RESP3 and replies with a map
	if got := dispatch(s, state, []string{"HELLO", "3"}); !reflect.DeepEqual(got, helloReply(state)) {
		t.Errorf("HELLO 3 reply = %+v, want %+v", got, helloReply(state))
	}
	if state.protocol_version != 3 {
		t.Errorf("protocol_version after HELLO 3 = %d, want 3", state.protocol_version)
	}

	// HELLO 2 switches back to RESP2
	if got := dispatch(s, state, []string{"HELLO", "2"}); !reflect.DeepEqual(got, helloReply(state)) {
		t.Errorf("HELLO 2 reply = %+v, want %+v", got, helloReply(state))
	}
	if state.protocol_version != 2 {
		t.Errorf("protocol_version after HELLO 2 = %d, want 2", state.protocol_version)
	}

	// rejected HELLO must not change the protocol
	if got := dispatch(s, state, []string{"HELLO", "3", "AUTH", "default", "secret"}); got.Kind != '-' {
		t.Errorf("HELLO AUTH reply = %+v, want an error", got)
	}
	if state.protocol_version != 2 {
		t.Errorf("protocol_version after rejected HELLO = %d, want 2 (unchanged)", state.protocol_version)
	}
}

// ---------------------------------------------------------------------------
// Handler tests (store state)
// ---------------------------------------------------------------------------

func TestCommandHandlers(t *testing.T) {
	t.Run("set_then_get", func(t *testing.T) {
		s := newTestStore()
		state := newClientState()
		if got := dispatch(s, state, []string{"SET", "foo", "bar"}); !reflect.DeepEqual(got, simpleString("OK")) {
			t.Fatalf("SET reply = %+v", got)
		}
		if got := dispatch(s, state, []string{"GET", "foo"}); !reflect.DeepEqual(got, bulkReply([]byte("bar"))) {
			t.Fatalf("GET reply = %+v", got)
		}
	})

	t.Run("get_missing", func(t *testing.T) {
		s := newTestStore()
		if got := dispatch(s, newClientState(), []string{"GET", "nope"}); !reflect.DeepEqual(got, nullReply()) {
			t.Errorf("GET reply = %+v, want null", got)
		}
	})

	t.Run("del_counts", func(t *testing.T) {
		s := newTestStore()
		s.Set("a", []byte("1"), 0)
		s.Set("b", []byte("2"), 0)
		if got := dispatch(s, newClientState(), []string{"DEL", "a", "b", "c"}); !reflect.DeepEqual(got, intReply(2)) {
			t.Errorf("DEL reply = %+v, want :2", got)
		}
		if got := dispatch(s, newClientState(), []string{"DEL", "a"}); !reflect.DeepEqual(got, intReply(0)) {
			t.Errorf("second DEL reply = %+v, want :0", got)
		}
	})

	t.Run("set_with_expiry", func(t *testing.T) {
		s := newTestStore()
		if got := dispatch(s, newClientState(), []string{"SET", "k", "v", "EX", "100"}); !reflect.DeepEqual(got, simpleString("OK")) {
			t.Fatalf("SET EX reply = %+v", got)
		}
		if got := dispatch(s, newClientState(), []string{"GET", "k"}); !reflect.DeepEqual(got, bulkReply([]byte("v"))) {
			t.Errorf("GET reply = %+v", got)
		}
		s.mu.RLock()
		expiresAt := s.data["k"].expiresAt
		s.mu.RUnlock()
		if expiresAt <= time.Now().UnixMilli() {
			t.Errorf("expiresAt = %d, want a future timestamp", expiresAt)
		}
	})

	t.Run("client_setinfo_ok", func(t *testing.T) {
		s := newTestStore()
		for _, args := range [][]string{
			{"CLIENT", "SETINFO", "LIB-NAME", "redis-py"},
			{"CLIENT", "SETINFO", "LIB-VER", "5.2.1"},
		} {
			if got := dispatch(s, newClientState(), args); !reflect.DeepEqual(got, simpleString("OK")) {
				t.Errorf("%v reply = %+v, want OK", args, got)
			}
		}
	})
}
