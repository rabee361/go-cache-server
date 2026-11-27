package main

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
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

func TestExists(t *testing.T) {
	tests := []struct {
		name      string
		key       string
		setupKeys map[string][]byte
		expire    string
		want      uint64
	}{
		{"exists_existing", "foo", map[string][]byte{"foo": []byte("bar")}, "", 1},
		{"exists_missing", "nope", map[string][]byte{}, "", 0},
		{"exists_empty_key", "", map[string][]byte{"": []byte("v")}, "", 1},
		{"exists_expired", "old", map[string][]byte{"old": []byte("v")}, "old", 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore()
			setKeys(s, tc.setupKeys)
			if tc.expire != "" {
				now := time.Now().UnixMilli()
				s.mu.Lock()
				e := s.data[tc.expire]
				e.expiresAt = now - 100
				s.data[tc.expire] = e
				s.mu.Unlock()
			}

			got, err := s.Exists(tc.key)
			if err != nil {
				t.Fatalf("Exists() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("Exists(%q) = %d, want %d", tc.key, got, tc.want)
			}
		})
	}
}

func TestFlushDB(t *testing.T) {
	t.Run("clears_keys", func(t *testing.T) {
		s := newTestStore()
		s.Set("a", []byte("1"), 0)
		s.Set("b", []byte("2"), 0)
		if err := s.FlushDB(); err != nil {
			t.Fatalf("FlushDB() error = %v", err)
		}
		if len(s.data) != 0 {
			t.Errorf("len(data) after FlushDB = %d, want 0", len(s.data))
		}
		if n, _ := s.Exists("a"); n != 0 {
			t.Errorf("Exists(a) after FlushDB = %d, want 0", n)
		}
	})

	t.Run("clears_all_types_and_used", func(t *testing.T) {
		s := newTestStore()
		s.Set("str", []byte("v"), 0)
		s.LPush("list", "x")
		s.SAdd("set", "m")
		s.ZAdd("zset", "m", 1)
		if s.used == 0 {
			t.Fatal("used = 0 after seeding a string value, want > 0")
		}
		if err := s.FlushDB(); err != nil {
			t.Fatalf("FlushDB() error = %v", err)
		}
		if len(s.data) != 0 || len(s.lists) != 0 || len(s.sets) != 0 || len(s.zsets) != 0 {
			t.Errorf("maps after FlushDB = data:%d lists:%d sets:%d zsets:%d, want all 0",
				len(s.data), len(s.lists), len(s.sets), len(s.zsets))
		}
		if s.used != 0 {
			t.Errorf("used after FlushDB = %d, want 0", s.used)
		}
		for _, k := range []string{"str", "list", "set", "zset"} {
			if n, _ := s.Exists(k); n != 0 {
				t.Errorf("Exists(%s) after FlushDB = %d, want 0", k, n)
			}
		}
	})

	t.Run("empty_store", func(t *testing.T) {
		s := newTestStore()
		if err := s.FlushDB(); err != nil {
			t.Fatalf("FlushDB() on empty store error = %v", err)
		}
	})
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
// Celery-facing store tests (lists, sets, sorted sets)
// ---------------------------------------------------------------------------

func TestLPushRPop(t *testing.T) {
	s := newTestStore()

	n, err := s.LPush("q", "a")
	if err != nil || n != 1 {
		t.Fatalf("LPush(q, a) = %d, %v; want 1, nil", n, err)
	}
	// LPUSH q b c prepends each in order → list is [c b a]
	n, err = s.LPush("q", "b", "c")
	if err != nil || n != 3 {
		t.Fatalf("LPush(q, b, c) = %d, %v; want 3, nil", n, err)
	}
	for _, want := range []string{"a", "b", "c"} {
		v, ok := s.RPop("q")
		if !ok || v != want {
			t.Errorf("RPop(q) = %q, %v; want %q, true", v, ok, want)
		}
	}
	if v, ok := s.RPop("q"); ok {
		t.Errorf("RPop on drained list = %q, %v; want \"\", false", v, ok)
	}
	if _, ok := s.lists["q"]; ok {
		t.Errorf("empty list key was left behind in the map")
	}
}

func TestLRemDirections(t *testing.T) {
	build := func() *MemoryStore {
		s := newTestStore()
		s.LPush("l", "x") // [x]
		s.lists["l"] = []string{"x", "y", "x", "z", "x"}
		return s
	}

	t.Run("count_positive_removes_from_head", func(t *testing.T) {
		s := build()
		got, err := s.LRem("l", 2, "x")
		if err != nil || got != 2 {
			t.Fatalf("LRem = %d, %v; want 2, nil", got, err)
		}
		if want := []string{"y", "z", "x"}; !reflect.DeepEqual(s.lists["l"], want) {
			t.Errorf("list = %v, want %v", s.lists["l"], want)
		}
	})

	t.Run("count_negative_removes_from_tail", func(t *testing.T) {
		s := build()
		got, err := s.LRem("l", -2, "x")
		if err != nil || got != 2 {
			t.Fatalf("LRem = %d, %v; want 2, nil", got, err)
		}
		if want := []string{"x", "y", "z"}; !reflect.DeepEqual(s.lists["l"], want) {
			t.Errorf("list = %v, want %v", s.lists["l"], want)
		}
	})

	t.Run("count_zero_removes_all", func(t *testing.T) {
		s := build()
		got, err := s.LRem("l", 0, "x")
		if err != nil || got != 3 {
			t.Fatalf("LRem = %d, %v; want 3, nil", got, err)
		}
		if want := []string{"y", "z"}; !reflect.DeepEqual(s.lists["l"], want) {
			t.Errorf("list = %v, want %v", s.lists["l"], want)
		}
	})

	t.Run("missing_key_and_no_match", func(t *testing.T) {
		s := newTestStore()
		if got, _ := s.LRem("nope", 0, "x"); got != 0 {
			t.Errorf("LRem on missing key = %d, want 0", got)
		}
		s.LPush("l", "a")
		if got, _ := s.LRem("l", 0, "x"); got != 0 {
			t.Errorf("LRem with no match = %d, want 0", got)
		}
	})
}

func TestSAddDedup(t *testing.T) {
	s := newTestStore()
	if got, err := s.SAdd("revoked", "t1", "t2"); err != nil || got != 2 {
		t.Fatalf("SAdd = %d, %v; want 2, nil", got, err)
	}
	if got, err := s.SAdd("revoked", "t2", "t3"); err != nil || got != 1 {
		t.Fatalf("SAdd dup = %d, %v; want 1 (only t3 is new), nil", got, err)
	}
	if len(s.sets["revoked"]) != 3 {
		t.Errorf("set size = %d, want 3", len(s.sets["revoked"]))
	}
	// A call whose members are all already present adds nothing.
	if got, err := s.SAdd("revoked", "t1", "t2"); err != nil || got != 0 {
		t.Errorf("SAdd all dup = %d, %v; want 0, nil", got, err)
	}
	// Duplicates within a single call count once.
	if got, err := s.SAdd("dupcall", "a", "a"); err != nil || got != 1 {
		t.Errorf("SAdd same-call dup = %d, %v; want 1, nil", got, err)
	}
	if len(s.sets["dupcall"]) != 1 {
		t.Errorf("dupcall set size = %d, want 1", len(s.sets["dupcall"]))
	}
}

func TestZAddZRangeByScore(t *testing.T) {
	s := newTestStore()
	if got, err := s.ZAdd("unacked", "task-b", 200); err != nil || got != 1 {
		t.Fatalf("ZAdd b = %d, %v; want 1, nil", got, err)
	}
	if got, _ := s.ZAdd("unacked", "task-a", 100); got != 1 {
		t.Fatalf("ZAdd a = %d, want 1", got)
	}
	// Updating an existing member scores 0 added.
	if got, _ := s.ZAdd("unacked", "task-a", 150); got != 0 {
		t.Errorf("ZAdd update = %d, want 0", got)
	}

	got, err := s.ZRangeByScore("unacked", math.Inf(-1), math.Inf(1), false, false)
	if err != nil {
		t.Fatalf("ZRangeByScore: %v", err)
	}
	if want := []string{"task-a", "task-b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("full range = %v, want %v (score order)", got, want)
	}
	// Exclusive lower bound skips task-a at exactly 150.
	got, _ = s.ZRangeByScore("unacked", 150, math.Inf(1), true, false)
	if want := []string{"task-b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("exclusive min = %v, want %v", got, want)
	}
	// Nothing in range → empty (not nil) slice.
	got, _ = s.ZRangeByScore("unacked", 1000, 2000, false, false)
	if len(got) != 0 {
		t.Errorf("out-of-range = %v, want empty", got)
	}
	// Exclusive upper bound skips task-b at exactly 200.
	got, _ = s.ZRangeByScore("unacked", math.Inf(-1), 200, false, true)
	if want := []string{"task-a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("exclusive max = %v, want %v", got, want)
	}
	// Inclusive bounds include both endpoints.
	got, _ = s.ZRangeByScore("unacked", 150, 200, false, false)
	if want := []string{"task-a", "task-b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("inclusive 150..200 = %v, want %v", got, want)
	}

	// Equal scores fall back to lexicographic member order, like Redis.
	tie := newTestStore()
	tie.ZAdd("z", "b", 5)
	tie.ZAdd("z", "a", 5)
	got, _ = tie.ZRangeByScore("z", math.Inf(-1), math.Inf(1), false, false)
	if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("lex tie-break = %v, want %v", got, want)
	}
}

func TestWrongTypeGuards(t *testing.T) {
	s := newTestStore()
	if err := s.Set("str", []byte("v"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LPush("str", "x"); !errors.Is(err, ErrWrongType) {
		t.Errorf("LPush on string key: err = %v, want ErrWrongType", err)
	}
	if _, err := s.SAdd("str", "x"); !errors.Is(err, ErrWrongType) {
		t.Errorf("SAdd on string key: err = %v, want ErrWrongType", err)
	}
	if _, err := s.ZAdd("str", "x", 1); !errors.Is(err, ErrWrongType) {
		t.Errorf("ZAdd on string key: err = %v, want ErrWrongType", err)
	}

	// The other direction: SET overwrites a list (Redis behavior).
	s.LPush("l", "item")
	if err := s.Set("l", []byte("now a string"), 0); err != nil {
		t.Fatalf("Set over list key: %v", err)
	}
	if _, ok := s.RPop("l"); ok {
		t.Errorf("list survived SET overwrite")
	}
	if v, ok := s.Get("l"); !ok || string(v) != "now a string" {
		t.Errorf("Get after overwrite = %q, %v", v, ok)
	}
}

func TestDeleteExistsAcrossTypes(t *testing.T) {
	s := newTestStore()
	s.LPush("list", "x")
	s.SAdd("set", "m")
	s.ZAdd("zset", "m", 1)
	s.Set("str", []byte("v"), 0)

	for _, k := range []string{"list", "set", "zset", "str"} {
		if n, _ := s.Exists(k); n != 1 {
			t.Errorf("Exists(%s) = %d, want 1", k, n)
		}
		if n, _ := s.Delete(k); n != 1 {
			t.Errorf("Delete(%s) = %d, want 1", k, n)
		}
		if n, _ := s.Exists(k); n != 0 {
			t.Errorf("Exists(%s) after delete = %d, want 0", k, n)
		}
	}
}

func TestMaxMemoryOOM(t *testing.T) {
	defer func(old int64) { serverConfig.SetMaxMemory(old) }(serverConfig.MaxMemory())
	// Budget for exactly one entry: len(k)+len(v)+overhead = 1+100+64 = 165.
	serverConfig.SetMaxMemory(200)
	s := newTestStore()

	val := make([]byte, 100)
	if err := s.Set("k", val, 0); err != nil {
		t.Fatalf("first Set within budget: %v", err)
	}
	err := s.Set("k2", val, 0)
	if !errors.Is(err, ErrOOM) {
		t.Fatalf("second Set over budget: err = %v, want ErrOOM", err)
	}
	// Overwrite within budget is fine (old size released first).
	if err := s.Set("k", make([]byte, 50), 0); err != nil {
		t.Errorf("shrinking overwrite: %v", err)
	}
	// Delete frees budget for new writes.
	s.Delete("k")
	if err := s.Set("k2", val, 0); err != nil {
		t.Errorf("Set after Delete freed budget: %v", err)
	}
	// Unlimited again.
	serverConfig.SetMaxMemory(0)
	if err := s.Set("k3", make([]byte, 10000), 0); err != nil {
		t.Errorf("Set with maxmemory=0: %v", err)
	}
}

func TestLLEN(t *testing.T) {
	s := newTestStore()
	if n, err := s.LLEN("q"); err != nil || n != 0 {
		t.Fatalf("LLEN(missing) = %d, %v; want 0, nil", n, err)
	}
	if _, err := s.LPush("q", "a"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.LLEN("q"); err != nil || n != 1 {
		t.Errorf("LLEN(q) = %d, %v; want 1, nil", n, err)
	}
	if _, err := s.LPush("q", "b", "c"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.LLEN("q"); err != nil || n != 3 {
		t.Errorf("LLEN(q) = %d, %v; want 3, nil", n, err)
	}
	if err := s.Set("str", []byte("v"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LLEN("str"); !errors.Is(err, ErrWrongType) {
		t.Errorf("LLEN on string key: err = %v, want ErrWrongType", err)
	}
	for i := 0; i < 3; i++ {
		if _, ok := s.RPop("q"); !ok {
			t.Fatalf("RPop %d on non-empty list returned false", i)
		}
	}
	if n, err := s.LLEN("q"); err != nil || n != 0 {
		t.Errorf("LLEN after drain = %d, %v; want 0, nil", n, err)
	}
}

func TestRPopOnMissingKey(t *testing.T) {
	s := newTestStore()
	if v, ok := s.RPop("nope"); ok || v != "" {
		t.Errorf("RPop(missing) = %q, %v; want \"\", false", v, ok)
	}
}

func TestUsedMemoryAccounting(t *testing.T) {
	s := newTestStore()
	if s.used != 0 {
		t.Fatalf("fresh store used = %d, want 0", s.used)
	}
	// len("k") + len("hello") + entryOverhead = 1+5+64.
	if err := s.Set("k", []byte("hello"), 0); err != nil {
		t.Fatal(err)
	}
	if s.used != 70 {
		t.Errorf("used after 5-byte Set = %d, want 70", s.used)
	}
	if err := s.Set("k", []byte("bye"), 0); err != nil {
		t.Fatal(err)
	}
	if s.used != 68 {
		t.Errorf("used after overwrite = %d, want 68 (1+3+64)", s.used)
	}
	if _, err := s.Delete("k"); err != nil {
		t.Fatal(err)
	}
	if s.used != 0 {
		t.Errorf("used after Delete = %d, want 0", s.used)
	}

	// Documented gap: used-memory accounting covers string values only, so
	// list/set/zset elements are not counted against maxmemory.
	s.LPush("l", "x")
	s.SAdd("set", "m")
	s.ZAdd("zset", "m", 1)
	if s.used != 0 {
		t.Errorf("used after list/set/zset writes = %d, want 0 (accounting gap)", s.used)
	}
}
