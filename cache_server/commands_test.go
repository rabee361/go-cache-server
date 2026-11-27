package main

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

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
		{"unknown", []string{"BOGUSCMD"}, errReply("ERR unknown command 'BOGUSCMD'")},
		{"ping", []string{"PING"}, simpleString("PONG")},
		{"ping_message", []string{"PING", "hello"}, bulkReply([]byte("hello"))},
		{"ping_too_many", []string{"PING", "a", "b"}, errReply("ERR wrong number of arguments for 'PING' command")},
		{"exists_arity", []string{"EXISTS"}, errReply("ERR wrong number of arguments for 'EXISTS' command")},
		{"exists_missing", []string{"EXISTS", "k"}, intReply(0)},
		{"flushdb_ok", []string{"FLUSHDB"}, simpleString("OK")},
		{"flushdb_arity", []string{"FLUSHDB", "x"}, errReply("ERR wrong number of arguments for 'FLUSHDB' command")},
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
		{"hello_auth_incomplete", []string{"HELLO", "3", "AUTH"}, errReply("Syntax error in HELLO option 'AUTH'")},
		{"hello_auth_missing_password", []string{"HELLO", "3", "AUTH", "default"}, errReply("Syntax error in HELLO option 'AUTH'")},
		{"hello_auth_unknown_user", []string{"HELLO", "3", "AUTH", "nosuch", "secret"}, errReply("WRONGPASS invalid username-password pair or user is disabled.")},
		{"hello_setname", []string{"HELLO", "3", "SETNAME", "me"}, errReply("Syntax error in HELLO option 'SETNAME'")},
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

	// complete AUTH against the default (nopass) user succeeds and switches
	if got := dispatch(s, state, []string{"HELLO", "3", "AUTH", "default", "secret"}); !reflect.DeepEqual(got, helloReply(state)) {
		t.Errorf("HELLO 3 AUTH reply = %+v, want %+v", got, helloReply(state))
	}
	if state.protocol_version != 3 {
		t.Errorf("protocol_version after HELLO 3 AUTH = %d, want 3", state.protocol_version)
	}

	// unknown username fails without changing the protocol
	if got := dispatch(s, state, []string{"HELLO", "2", "AUTH", "nosuch", "secret"}); got.Kind != '-' {
		t.Errorf("HELLO AUTH nosuch reply = %+v, want an error", got)
	}
	if state.protocol_version != 3 {
		t.Errorf("protocol_version after rejected HELLO = %d, want 3 (unchanged)", state.protocol_version)
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

	t.Run("exists_counts", func(t *testing.T) {
		s := newTestStore()
		s.Set("a", []byte("1"), 0)
		s.Set("b", []byte("2"), 0)
		if got := dispatch(s, newClientState(), []string{"EXISTS", "a", "b", "c"}); !reflect.DeepEqual(got, intReply(2)) {
			t.Errorf("EXISTS reply = %+v, want :2", got)
		}
		if got := dispatch(s, newClientState(), []string{"EXISTS", "a"}); !reflect.DeepEqual(got, intReply(1)) {
			t.Errorf("second EXISTS reply = %+v, want :1", got)
		}
	})

	t.Run("flushdb_clears_store", func(t *testing.T) {
		s := newTestStore()
		s.Set("a", []byte("1"), 0)
		s.Set("b", []byte("2"), 0)
		if got := dispatch(s, newClientState(), []string{"FLUSHDB"}); !reflect.DeepEqual(got, simpleString("OK")) {
			t.Fatalf("FLUSHDB reply = %+v, want +OK", got)
		}
		if got := dispatch(s, newClientState(), []string{"EXISTS", "a", "b"}); !reflect.DeepEqual(got, intReply(0)) {
			t.Errorf("EXISTS after FLUSHDB = %+v, want :0", got)
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

// ---------------------------------------------------------------------------
// Command tests: Celery commands + CONFIG
// ---------------------------------------------------------------------------

func TestDispatchCeleryCommands(t *testing.T) {
	s := newTestStore()
	st := newClientState()

	if got := dispatch(s, st, []string{"LPUSH", "celery", "task1"}); !reflect.DeepEqual(got, intReply(1)) {
		t.Errorf("LPUSH = %+v, want :1", got)
	}
	want := arrayReply([]Reply{bulkReply([]byte("celery")), bulkReply([]byte("task1"))})
	if got := dispatch(s, st, []string{"BRPOP", "celery", "1"}); !reflect.DeepEqual(got, want) {
		t.Errorf("BRPOP = %+v, want [celery task1]", got)
	}
	if got := dispatch(s, st, []string{"BRPOP", "celery", "0.01"}); !reflect.DeepEqual(got, nullArrayReply()) {
		t.Errorf("BRPOP on empty = %+v, want null array", got)
	}
	if got := dispatch(s, st, []string{"SADD", "celery.revoked", "tid"}); !reflect.DeepEqual(got, intReply(1)) {
		t.Errorf("SADD = %+v, want :1", got)
	}
	if got := dispatch(s, st, []string{"ZADD", "celery.unacked", "100", "tid"}); !reflect.DeepEqual(got, intReply(1)) {
		t.Errorf("ZADD = %+v, want :1", got)
	}
	if got := dispatch(s, st, []string{"ZRANGEBYSCORE", "celery.unacked", "-inf", "+inf"}); !reflect.DeepEqual(got, arrayReply([]Reply{bulkReply([]byte("tid"))})) {
		t.Errorf("ZRANGEBYSCORE = %+v, want [tid]", got)
	}

	// Error paths.
	for _, tc := range []struct {
		name string
		args []string
		want Reply
	}{
		{"lpush_arity", []string{"LPUSH", "q"}, errReply("ERR wrong number of arguments for 'LPUSH' command")},
		{"brpop_arity", []string{"BRPOP", "1"}, errReply("ERR wrong number of arguments for 'BRPOP' command")},
		{"brpop_bad_timeout", []string{"BRPOP", "q", "abc"}, errReply("ERR timeout is not a float or out of range")},
		{"brpop_negative_timeout", []string{"BRPOP", "q", "-1"}, errReply("ERR timeout is not a float or out of range")},
		{"lrem_bad_count", []string{"LREM", "q", "x", "1"}, errReply("ERR value is not an integer or out of range")},
		{"zadd_odd_pairs", []string{"ZADD", "z", "1", "m", "2"}, errReply("ERR syntax error")},
		{"zadd_bad_float", []string{"ZADD", "z", "score", "m"}, errReply("ERR value is not a valid float")},
		{"zrange_bad_min", []string{"ZRANGEBYSCORE", "z", "junk", "+inf"}, errReply("ERR min or max is not a float")},
		{"zrange_bad_max", []string{"ZRANGEBYSCORE", "z", "-inf", "junk"}, errReply("ERR min or max is not a float")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dispatch(newTestStore(), newClientState(), tc.args); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}

	t.Run("lrem_via_dispatch", func(t *testing.T) {
		s := newTestStore()
		dispatch(s, st, []string{"LPUSH", "l", "a"})
		dispatch(s, st, []string{"LPUSH", "l", "b"})
		if got := dispatch(s, st, []string{"LREM", "l", "0", "a"}); !reflect.DeepEqual(got, intReply(1)) {
			t.Errorf("LREM = %+v, want :1", got)
		}
	})

	t.Run("wrongtype_via_dispatch", func(t *testing.T) {
		s := newTestStore()
		dispatch(s, st, []string{"SET", "str", "v"})
		if got := dispatch(s, st, []string{"LPUSH", "str", "x"}); !reflect.DeepEqual(got, errReply(ErrWrongType.Error())) {
			t.Errorf("LPUSH on string = %+v, want WRONGTYPE error", got)
		}
	})

	t.Run("oom_via_dispatch", func(t *testing.T) {
		defer func(old int64) { serverConfig.SetMaxMemory(old) }(serverConfig.MaxMemory())
		serverConfig.SetMaxMemory(100)
		got := dispatch(newTestStore(), st, []string{"SET", "k", strings.Repeat("x", 200)})
		if !reflect.DeepEqual(got, errReply(ErrOOM.Error())) {
			t.Errorf("SET over budget = %+v, want %q", got, ErrOOM.Error())
		}
	})
}

func TestParseMemSize(t *testing.T) {
	tests := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"0", 0, true},
		{"1024", 1024, true},
		{"10b", 10, true},
		{"1k", 1024, true},
		{"1kb", 1024, true},
		{"2m", 2 * 1024 * 1024, true},
		{"2mb", 2 * 1024 * 1024, true},
		{"1g", 1024 * 1024 * 1024, true},
		{"1gb", 1024 * 1024 * 1024, true},
		{"1GB", 1024 * 1024 * 1024, true},
		{"100 mb", 100 * 1024 * 1024, true},
		{"-5", 0, false},
		{"abc", 0, false},
		{"mb", 0, false},
		{"", 0, false},
		{"99999999999999gb", 0, false},
	}
	for _, tc := range tests {
		got, err := parseMemSize(tc.in)
		if tc.ok && (err != nil || got != tc.want) {
			t.Errorf("parseMemSize(%q) = %d, %v; want %d, nil", tc.in, got, err, tc.want)
		}
		if !tc.ok && err == nil {
			t.Errorf("parseMemSize(%q) = %d, nil; want error", tc.in, got)
		}
	}
}

func TestParseScoreBound(t *testing.T) {
	tests := []struct {
		in       string
		want     float64
		wantExcl bool
		ok       bool
	}{
		{"-inf", math.Inf(-1), false, true},
		{"+inf", math.Inf(1), false, true},
		{"inf", math.Inf(1), false, true},
		{"100", 100, false, true},
		{"-3.5", -3.5, false, true},
		{"(100", 100, true, true},
		{"(-inf", math.Inf(-1), true, true},
		{"junk", 0, false, false},
		{"(", 0, false, false},
	}
	for _, tc := range tests {
		got, excl, err := parseScoreBound(tc.in)
		if tc.ok && (err != nil || got != tc.want || excl != tc.wantExcl) {
			t.Errorf("parseScoreBound(%q) = %v, %v, %v; want %v, %v, nil", tc.in, got, excl, err, tc.want, tc.wantExcl)
		}
		if !tc.ok && err == nil {
			t.Errorf("parseScoreBound(%q) = %v, want error", tc.in, got)
		}
	}
}

func TestConfigCommands(t *testing.T) {
	defer func(mm int64, mc int) {
		serverConfig.SetMaxMemory(mm)
		serverConfig.SetMaxClients(mc)
	}(serverConfig.MaxMemory(), serverConfig.MaxClients())

	s := newTestStore()
	st := newClientState()

	// CONFIG SET maxmemory with unit suffix.
	if got := dispatch(s, st, []string{"CONFIG", "SET", "maxmemory", "100mb"}); !reflect.DeepEqual(got, simpleString("OK")) {
		t.Fatalf("CONFIG SET maxmemory = %+v, want +OK", got)
	}
	if mm := serverConfig.MaxMemory(); mm != 100*1024*1024 {
		t.Errorf("MaxMemory = %d, want %d", mm, 100*1024*1024)
	}

	// CONFIG GET exact name → flat [name, value] array.
	want := arrayReply([]Reply{bulkReply([]byte("maxmemory")), bulkReply([]byte("104857600"))})
	if got := dispatch(s, st, []string{"CONFIG", "GET", "maxmemory"}); !reflect.DeepEqual(got, want) {
		t.Errorf("CONFIG GET maxmemory = %+v, want %+v", got, want)
	}

	// CONFIG GET glob returns both params (maxmemory, maxclients).
	got := dispatch(s, st, []string{"CONFIG", "GET", "max*"})
	if len(got.Arr) != 4 {
		t.Errorf("CONFIG GET max* returned %d elements, want 4 (two name/value pairs)", len(got.Arr))
	}

	// CONFIG GET miss → empty array.
	if got := dispatch(s, st, []string{"CONFIG", "GET", "nosuchparam"}); !reflect.DeepEqual(got, arrayReply(nil)) {
		t.Errorf("CONFIG GET miss = %+v, want empty array", got)
	}

	// RESP3 connections get a map, like real Redis (redis-py 8 defaults to
	// RESP3 and its CONFIG GET callback requires the map shape).
	st3 := newClientState()
	dispatch(s, st3, []string{"HELLO", "3"})
	wantMap := mapReply([]Reply{bulkReply([]byte("maxmemory")), bulkReply([]byte("104857600"))})
	if got := dispatch(s, st3, []string{"CONFIG", "GET", "maxmemory"}); !reflect.DeepEqual(got, wantMap) {
		t.Errorf("CONFIG GET in RESP3 = %+v, want map %+v", got, wantMap)
	}

	// CONFIG SET maxclients takes effect at runtime.
	if got := dispatch(s, st, []string{"CONFIG", "SET", "maxclients", "5"}); !reflect.DeepEqual(got, simpleString("OK")) {
		t.Fatalf("CONFIG SET maxclients = %+v, want +OK", got)
	}
	if mc := serverConfig.MaxClients(); mc != 5 {
		t.Errorf("MaxClients = %d, want 5", mc)
	}

	// All-or-nothing: bad second pair leaves the first unapplied.
	got = dispatch(s, st, []string{"CONFIG", "SET", "maxclients", "7", "maxmemory", "garbage"})
	if !reflect.DeepEqual(got, errReply("ERR argument couldn't be parsed into an integer")) {
		t.Errorf("mixed SET = %+v, want parse error", got)
	}
	if mc := serverConfig.MaxClients(); mc != 5 {
		t.Errorf("MaxClients after failed SET = %d, want 5 (unchanged)", mc)
	}

	// Error paths.
	for _, tc := range []struct {
		name string
		args []string
		want Reply
	}{
		{"bare_config", []string{"CONFIG"}, errReply("ERR wrong number of arguments for 'CONFIG' command")},
		{"get_arity", []string{"CONFIG", "GET"}, errReply("ERR wrong number of arguments for 'config|get' command")},
		{"set_arity", []string{"CONFIG", "SET", "maxmemory"}, errReply("ERR wrong number of arguments for 'config|set' command")},
		{"set_odd_args", []string{"CONFIG", "SET", "maxmemory", "1", "maxclients"}, errReply("ERR wrong number of arguments for 'config|set' command")},
		{"unknown_param", []string{"CONFIG", "SET", "maxmemory-policy", "allkeys-lru"}, errReply("ERR Unsupported CONFIG parameter: maxmemory-policy")},
		{"maxclients_zero", []string{"CONFIG", "SET", "maxclients", "0"}, errReply("ERR argument couldn't be parsed into an integer")},
		{"unknown_sub", []string{"CONFIG", "REWRITE"}, errReply("ERR Unknown subcommand or wrong number of arguments for 'REWRITE'. Try CONFIG HELP.")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dispatch(newTestStore(), newClientState(), tc.args); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
