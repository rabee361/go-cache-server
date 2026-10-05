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
)

// newTestStore returns a fresh MemoryStore with an initialized map.
func newTestStore() *MemoryStore {
	return &MemoryStore{
		mu:    sync.RWMutex{},
		value: StoreValue{data: make(map[string][]byte)},
	}
}

// populateStore seeds a MemoryStore with the given key-value pairs.
func populateStore(s *MemoryStore, keys map[string][]byte) {
	for k, v := range keys {
		s.Set(k, v)
	}
}

// ---------------------------------------------------------------------------
// Phase 1: Unit tests for MemoryStore methods
// ---------------------------------------------------------------------------

func TestSet(t *testing.T) {
	oneMB := make([]byte, 1<<20)
	for i := range oneMB {
		oneMB[i] = 'A'
	}

	tests := []struct {
		name      string
		key       string
		value     []byte
		wantBytes uint64
		wantErr   bool
	}{
		{"set_new_key", "foo", []byte("bar"), 0, false},
		{"set_empty_key", "", []byte("val"), 0, false},
		{"set_empty_value", "key", []byte(""), 0, false},
		{"set_both_empty", "", []byte(""), 0, false},
		{"set_overwrite_existing", "foo", []byte("new"), 0, false},
		{"set_unicode_key", "키", []byte("값"), 0, false},
		{"set_large_value", "big", oneMB, 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore()

			// Pre-populate for overwrite test
			if tc.name == "set_overwrite_existing" {
				s.Set("foo", []byte("old"))
			}

			got, err := s.Set(tc.key, tc.value)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Set() error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.wantBytes {
				t.Errorf("Set() returned %d, want %d", got, tc.wantBytes)
			}

			// Verify the value was stored correctly
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
			populateStore(s, tc.setupKeys)

			// Special setup: delete for get_after_delete test
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
			populateStore(s, tc.setupKeys)

			ret, err := s.Delete(tc.key)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Delete() error = %v, wantErr %v", err, tc.wantErr)
			}
			if ret != tc.wantRet {
				t.Errorf("Delete() = %d, want %d", ret, tc.wantRet)
			}

			// Special verification: set again after delete
			if tc.name == "delete_then_set_again" {
				s.Set("x", []byte("2"))
			}

			_, exists := s.Get(tc.verifyKey)
			if exists != tc.verifyExists {
				t.Errorf("Get(%q) exists = %v after Delete, want %v", tc.verifyKey, exists, tc.verifyExists)
			}

			// For delete_then_set_again, verify the new value
			if tc.name == "delete_then_set_again" {
				val, _ := s.Get("x")
				if string(val) != "2" {
					t.Errorf("Get(%q) = %q, want %q after re-set", "x", val, "2")
				}
			}
		})
	}
}

func TestExists(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		setupKeys  map[string][]byte
		wantExists bool
		wantErr    bool
	}{
		{"exists_present_key", "foo", map[string][]byte{"foo": []byte("bar")}, true, false},
		{"exists_absent_key", "nope", map[string][]byte{}, false, false},
		{"exists_empty_key_present", "", map[string][]byte{"": []byte("x")}, true, false},
		{"exists_after_delete", "tmp", map[string][]byte{"tmp": []byte("v")}, false, false},
		{"exists_after_overwrite", "k", map[string][]byte{"k": []byte("old")}, true, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore()
			populateStore(s, tc.setupKeys)

			// Special setup
			if tc.name == "exists_after_delete" {
				s.Delete("tmp")
			}
			if tc.name == "exists_after_overwrite" {
				s.Set("k", []byte("new"))
			}

			got, err := s.Exists(tc.key)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Exists() error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.wantExists {
				t.Errorf("Exists(%q) = %v, want %v", tc.key, got, tc.wantExists)
			}
		})
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

				s.Set(key, val)
				got, ok := s.Get(key)
				if ok && string(got) != string(val) {
					t.Errorf("goroutine %d: Get(%q) = %q, want %q", idx, key, got, val)
				}
				s.Exists(key)
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
				s.Set("shared-key", []byte(strings.Repeat("v", idx+1)))
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
		name string
		in   Reply
		want string
	}{
		{"simple_string", simpleString("OK"), "+OK\r\n"},
		{"error", errReply("ERR x"), "-ERR x\r\n"},
		{"integer", intReply(42), ":42\r\n"},
		{"negative_integer", intReply(-1), ":-1\r\n"},
		{"bulk", bulkReply([]byte("hello")), "$5\r\nhello\r\n"},
		{"null", nullReply(), "$-1\r\n"},
		{"empty_bulk", bulkReply([]byte("")), "$0\r\n\r\n"},
		{"error_crlf_sanitized", errReply("bad\r\nmsg"), "-bad  msg\r\n"},
		{"array", Reply{Kind: '*', Arr: []Reply{simpleString("OK"), intReply(1)}}, "*2\r\n+OK\r\n:1\r\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := bufio.NewWriter(&buf)
			if err := writeReply(w, tc.in); err != nil {
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
	if err := writeReply(w, simpleString("OK")); err != nil {
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
		{"inline_ping", "PING\r\n", []string{"PING"}, false, ""},
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
	input := "*1\r\n$4\r\nPING\r\n*1\r\n$4\r\nPING\r\n"
	r := bufio.NewReader(strings.NewReader(input))
	for i := 0; i < 2; i++ {
		got, err := readCommand(r)
		if err != nil {
			t.Fatalf("command %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, []string{"PING"}) {
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
		{"unknown", []string{"FOO"}, errReply("ERR unknown command 'FOO'")},
		{"get_missing", []string{"get", "k"}, nullReply()},
		{"set_lowercase", []string{"SeT", "k", "v"}, simpleString("OK")},
		{"ping", []string{"PING"}, simpleString("PONG")},
		{"ping_echo", []string{"PING", "hi"}, bulkReply([]byte("hi"))},
		{"quit", []string{"QUIT"}, simpleString("OK")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := dispatch(newTestStore(), tc.args)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Handler tests (store state)
// ---------------------------------------------------------------------------

func TestCommandHandlers(t *testing.T) {
	t.Run("set_then_get", func(t *testing.T) {
		s := newTestStore()
		if got := dispatch(s, []string{"SET", "foo", "bar"}); !reflect.DeepEqual(got, simpleString("OK")) {
			t.Fatalf("SET reply = %+v", got)
		}
		if got := dispatch(s, []string{"GET", "foo"}); !reflect.DeepEqual(got, bulkReply([]byte("bar"))) {
			t.Fatalf("GET reply = %+v", got)
		}
	})

	t.Run("get_missing", func(t *testing.T) {
		s := newTestStore()
		if got := dispatch(s, []string{"GET", "nope"}); !reflect.DeepEqual(got, nullReply()) {
			t.Errorf("GET reply = %+v, want null", got)
		}
	})

	t.Run("del_counts", func(t *testing.T) {
		s := newTestStore()
		s.Set("a", []byte("1"))
		s.Set("b", []byte("2"))
		if got := dispatch(s, []string{"DEL", "a", "b", "c"}); !reflect.DeepEqual(got, intReply(2)) {
			t.Errorf("DEL reply = %+v, want :2", got)
		}
		if got := dispatch(s, []string{"DEL", "a"}); !reflect.DeepEqual(got, intReply(0)) {
			t.Errorf("second DEL reply = %+v, want :0", got)
		}
	})

	t.Run("exists_counts", func(t *testing.T) {
		s := newTestStore()
		s.Set("a", []byte("1"))
		s.Set("b", []byte("2"))
		if got := dispatch(s, []string{"EXISTS", "a", "c", "b"}); !reflect.DeepEqual(got, intReply(2)) {
			t.Errorf("EXISTS reply = %+v, want :2", got)
		}
	})

	t.Run("ready_cleanup", func(t *testing.T) {
		s := newTestStore()
		if got := dispatch(s, []string{"READY"}); !reflect.DeepEqual(got, simpleString("READY")) {
			t.Fatalf("READY reply = %+v", got)
		}
		s.mu.RLock()
		defer s.mu.RUnlock()
		for k := range s.value.data {
			if strings.HasPrefix(k, "_health_check_") {
				t.Errorf("orphaned health key %q left in store", k)
			}
		}
	})
}
