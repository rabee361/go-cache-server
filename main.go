package main

import (
	"net/http"
	"fmt"
	"sync"
)

type Store interface {
	Get(key string) ([]byte, bool)
	Set(key string, value []byte) uint64
	// Delete(key string) uint64
	// Exists(key string) bool
}

type MemoryStore struct {
    mu   sync.RWMutex
    data map[string][]byte
}

func (s *MemoryStore) Set(key string, value []byte) (uint64, error) {
	s.mu.Lock()
	s.data[key] = value
	s.mu.Unlock()
	return 0, nil
}

func (s *MemoryStore) Get(key string) ([]byte, bool) {
	return s.data[key], true
}

// func Delete(key string) (uint64, error) {
// }

// func Exists(key string) (bool, error) {
// }

func startServer() {
	err := http.ListenAndServe("localhost:8080", nil)
	if err != nil {
		println("Failed to start server")
		return
	}
	fmt.Println("Server started on localhost:8080")
}

func (s *MemoryStore) setValueHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}
	key := r.FormValue("key")
	value := r.FormValue("value")
	_, err := s.Set(key, []byte(value))
	if err != nil {
		http.Error(w, "Failed to set value", http.StatusInternalServerError)
		return
	}
	fmt.Println("Value set")
}

func (s *MemoryStore) getValueHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}
	key := r.FormValue("key")
	fmt.Println("key is ", key)
	value, _ := s.Get(key)
	fmt.Println(string(value))
}

func main() {
	store := &MemoryStore{
		mu:   sync.RWMutex{},
		data: make(map[string][]byte),
	}
	http.HandleFunc("/cache/get", store.getValueHandler)
	http.HandleFunc("/cache/set", store.setValueHandler)
	startServer()
}