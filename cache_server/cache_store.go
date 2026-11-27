package main

import (
	"errors"
	"math"
	"sort"
	"sync"
	"time"
)

var (
	ErrOOM       = errors.New("OOM command not allowed when used memory > 'maxmemory'")
	ErrWrongType = errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
)

// entryOverhead is a rough per-key bookkeeping cost (map slot, expiry field,
// allocator rounding) so used-memory is an estimate, not an exact count.
const entryOverhead = 64

func entrySize(key string, value []byte) int64 {
	return int64(len(key)) + int64(len(value)) + entryOverhead
}

type entry struct {
	value     []byte
	expiresAt int64
}

func (e entry) expired(now int64) bool {
	return e.expiresAt > 0 && e.expiresAt <= now
}

type MemoryStore struct {
	mu    sync.RWMutex
	cfg   *Config
	data  map[string]entry               // string values (the only type with TTLs)
	lists map[string][]string            // LPUSH appends at the front, RPOP takes from the back
	sets  map[string]map[string]struct{} // SADD members
	zsets map[string]map[string]float64  // ZADD member -> score
	used  int64                          // approximate bytes held by string values
}

var _ Store = (*MemoryStore)(nil)

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		cfg:   serverConfig,
		data:  make(map[string]entry),
		lists: make(map[string][]string),
		sets:  make(map[string]map[string]struct{}),
		zsets: make(map[string]map[string]float64),
	}
}

func (s *MemoryStore) holdsString(key string, now int64) bool {
	e, ok := s.data[key]
	return ok && !e.expired(now)
}

type Store interface {
	Get(key string) ([]byte, bool)
	Set(key string, value []byte, ttlMs int64) error
	Delete(key string) (uint64, error)
	Exists(key string) (uint64, error)
	FlushDB() error
	LPush(key string, values ...string) (int64, error)
	LLEN(key string) (int64, error)
	RPop(key string) (string, bool)
	LRem(key string, count int64, value string) (int64, error)
	SAdd(key string, members ...string) (int64, error)
	ZAdd(key string, member string, score float64) (int64, error)
	ZRangeByScore(key string, min, max float64, minExcl, maxExcl bool) ([]string, error)
}

func (s *MemoryStore) Set(key string, value []byte, ttlMs int64) error {
	now := time.Now().UnixMilli()
	e := entry{value: value}
	if ttlMs > 0 {
		if ttlMs > math.MaxInt64-now {
			e.expiresAt = math.MaxInt64
		} else {
			e.expiresAt = now + ttlMs
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	newSize := entrySize(key, value)
	oldSize := int64(0)
	if prev, ok := s.data[key]; ok {
		oldSize = entrySize(key, prev.value)
	}
	if max := s.cfg.MaxMemory(); max > 0 && s.used-oldSize+newSize > max {
		return ErrOOM
	}
	delete(s.lists, key)
	delete(s.sets, key)
	delete(s.zsets, key)
	s.used += newSize - oldSize
	s.data[key] = e
	return nil
}

func (store *MemoryStore) Get(key string) ([]byte, bool) {
	now := time.Now().UnixMilli()
	store.mu.RLock()
	defer store.mu.RUnlock()
	entry, ok := store.data[key]
	if !ok || entry.expired(now) {
		return nil, false
	}
	return entry.value, true
}

func (store *MemoryStore) Delete(key string) (uint64, error) {
	now := time.Now().UnixMilli()
	store.mu.Lock()
	defer store.mu.Unlock()
	var removed uint64
	if e, exists := store.data[key]; exists {
		if !e.expired(now) {
			removed = 1
		}
		store.used -= entrySize(key, e.value)
		delete(store.data, key)
	}
	if _, ok := store.lists[key]; ok {
		delete(store.lists, key)
		removed = 1
	}
	if _, ok := store.sets[key]; ok {
		delete(store.sets, key)
		removed = 1
	}
	if _, ok := store.zsets[key]; ok {
		delete(store.zsets, key)
		removed = 1
	}
	if removed > 1 {
		removed = 1 // DEL counts keys removed, not types
	}
	return removed, nil
}

func (store *MemoryStore) Exists(key string) (uint64, error) {
	now := time.Now().UnixMilli()
	store.mu.RLock()
	defer store.mu.RUnlock()
	if store.holdsString(key, now) {
		return 1, nil
	}
	if _, ok := store.lists[key]; ok {
		return 1, nil
	}
	if _, ok := store.sets[key]; ok {
		return 1, nil
	}
	if _, ok := store.zsets[key]; ok {
		return 1, nil
	}
	return 0, nil
}

func (store *MemoryStore) FlushDB() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	clear(store.data)
	clear(store.lists)
	clear(store.sets)
	clear(store.zsets)
	store.used = 0
	return nil
}

func (s *MemoryStore) LPush(key string, values ...string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.holdsString(key, time.Now().UnixMilli()) {
		return 0, ErrWrongType
	}
	l := s.lists[key]
	out := make([]string, 0, len(values)+len(l))
	for i := len(values) - 1; i >= 0; i-- {
		out = append(out, values[i])
	}
	out = append(out, l...)
	s.lists[key] = out
	return int64(len(out)), nil
}

func (s *MemoryStore) LLEN(key string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.holdsString(key, time.Now().UnixMilli()) {
		return 0, ErrWrongType
	}
	return int64(len(s.lists[key])), nil
}

func (s *MemoryStore) RPop(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.lists[key]
	if len(l) == 0 {
		return "", false
	}
	v := l[len(l)-1]
	if len(l) == 1 {
		delete(s.lists, key)
	} else {
		s.lists[key] = l[:len(l)-1]
	}
	return v, true
}

func (s *MemoryStore) LRem(key string, count int64, value string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.lists[key]
	if !ok {
		return 0, nil
	}
	var removed int64
	keep := make([]string, 0, len(l))
	if count < 0 {
		limit := -count
		for i := len(l) - 1; i >= 0; i-- {
			if l[i] == value && removed < limit {
				removed++
			} else {
				keep = append(keep, l[i])
			}
		}
		for i, j := 0, len(keep)-1; i < j; i, j = i+1, j-1 {
			keep[i], keep[j] = keep[j], keep[i]
		}
	} else {
		limit := count
		if limit == 0 {
			limit = int64(len(l))
		}
		for _, v := range l {
			if v == value && removed < limit {
				removed++
			} else {
				keep = append(keep, v)
			}
		}
	}
	if removed == 0 {
		return 0, nil
	}
	if len(keep) == 0 {
		delete(s.lists, key)
	} else {
		s.lists[key] = keep
	}
	return removed, nil
}

func (s *MemoryStore) SAdd(key string, members ...string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.holdsString(key, time.Now().UnixMilli()) {
		return 0, ErrWrongType
	}
	set := s.sets[key]
	if set == nil {
		set = make(map[string]struct{})
	}
	var added int64
	for _, m := range members {
		if _, dup := set[m]; !dup {
			set[m] = struct{}{}
			added++
		}
	}
	s.sets[key] = set
	return added, nil
}

func (s *MemoryStore) ZAdd(key string, member string, score float64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.holdsString(key, time.Now().UnixMilli()) {
		return 0, ErrWrongType
	}
	z := s.zsets[key]
	if z == nil {
		z = make(map[string]float64)
	}
	if _, exists := z[member]; exists {
		z[member] = score // updating an existing member doesn't count as added
		s.zsets[key] = z
		return 0, nil
	}
	z[member] = score
	s.zsets[key] = z
	return 1, nil
}

func (s *MemoryStore) ZRangeByScore(key string, min, max float64, minExcl, maxExcl bool) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	z := s.zsets[key]
	type scored struct {
		member string
		score  float64
	}
	matches := make([]scored, 0, len(z))
	for m, sc := range z {
		lo := sc >= min
		if minExcl {
			lo = sc > min
		}
		hi := sc <= max
		if maxExcl {
			hi = sc < max
		}
		if lo && hi {
			matches = append(matches, scored{m, sc})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score < matches[j].score
		}
		return matches[i].member < matches[j].member // lex tie-break, like Redis
	})
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = m.member
	}
	return out, nil
}
