package main

import (
	"bufio"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

type Store interface {
	Get(key string) ([]byte, bool)
	Set(key string, value []byte) (uint64, error)
	Delete(key string) (uint64, error)
	Exists(key string) (bool, error)
}

var _ Store = (*MemoryStore)(nil)

type StoreValue struct {
	data      map[string][]byte
	timestamp time.Time
}

type MemoryStore struct {
	mu    sync.RWMutex
	value StoreValue
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		mu:    sync.RWMutex{},
		value: StoreValue{data: make(map[string][]byte)},
	}
}

func (s *MemoryStore) Set(key string, value []byte) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value.data[key] = value
	return 0, nil
}

func (s *MemoryStore) Get(key string) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.value.data[key]
	return value, exists
}

func (s *MemoryStore) Delete(key string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.value.data[key]; !exists {
		return 0, nil
	}
	delete(s.value.data, key)
	return 1, nil
}

func (s *MemoryStore) Exists(key string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, exists := s.value.data[key]
	return exists, nil
}

// serve accepts connections on ln until ln is closed. Each connection gets
// its own goroutine so a slow client can never stall the others.
func serve(listener net.Listener, store Store) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go handleConn(conn, store)
	}
}

// handleConn runs the read-decode-dispatch-encode-flush loop for one client
// connection. All network I/O happens outside any store lock.
func handleConn(conn net.Conn, store Store) {
	defer conn.Close()
	defer func() {
		if p := recover(); p != nil {
			log.Printf("panic in connection handler: %v", p)
		}
	}()

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)

	for {
		args, err := readCommand(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			writeReply(writer, errReply("ERR Protocol error: "+err.Error()))
			writer.Flush()
			return
		}

		reply := dispatch(store, args)
		writeReply(writer, reply)
		writer.Flush()

		if reply.Kind == '+' && reply.Str == "OK" && strings.EqualFold(args[0], "QUIT") {
			return
		}
	}
}

func main() {
	addr := flag.String("addr", "localhost:6379", "listen address")
	flag.Parse()

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", *addr, err)
	}
	log.Printf("cache server listening on %s", *addr)
	log.Fatal(serve(listener, NewMemoryStore()))
}
