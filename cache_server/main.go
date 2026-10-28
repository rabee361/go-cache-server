package main

import (
	"bufio"
	"errors"
	"flag"
	"io"
	"log"
	"math"
	"net"
	"sync"
	"time"
)

type Store interface {
	Get(key string) ([]byte, bool)
	Set(key string, value []byte, ttlMs int64) error
	Delete(key string) (uint64, error)
}

var _ Store = (*MemoryStore)(nil)

type entry struct {
	value     []byte
	expiresAt int64
}

func (e entry) expired(now int64) bool {
	return e.expiresAt > 0 && e.expiresAt <= now
}

type MemoryStore struct {
	mu   sync.RWMutex
	data map[string]entry
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{data: make(map[string]entry)}
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
	e, exists := store.data[key]
	if !exists || e.expired(now) {
		return 0, nil
	}
	delete(store.data, key)
	return 1, nil
}

func serve(listener net.Listener, store *MemoryStore) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go handleConn(conn, store)
	}
}

func handleConn(conn net.Conn, store *MemoryStore) {
	defer conn.Close()
	defer func() {
		if p := recover(); p != nil {
			log.Printf("panic in connection handler: %v", p)
		}
	}()

	state := newClientState()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)

	for {
		args, err := readCommand(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			writer.Flush()
			return
		}

		reply := dispatch(store, state, args)
		writeReply(writer, state.protocol_version, reply)
		writer.Flush()
	}
}

func main() {
	addr := flag.String("addr", "localhost:6379", "listen address")
	flag.Parse()

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", *addr, err)
	}
	log.Printf("RESP cache server listening on %s", *addr)
	log.Fatal(serve(listener, NewMemoryStore()))
}
