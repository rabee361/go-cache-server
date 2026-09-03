package main

import (
	"fmt"
	"net/http"
	"sync"
	"time"
	"log"
)

type Store interface {
	Get(key string) ([]byte, bool)
	Set(key string, value []byte) (uint64, error)
	Delete(key string) uint64
	Exists(key string) bool
}

type StoreValue struct {
	data      map[string][]byte
	timestamp time.Time
}

type MemoryStore struct {
	mu    sync.RWMutex
	value StoreValue
}

func (s *MemoryStore) Set(key string, value []byte) (uint64, error) {
	s.mu.Lock()
	s.value.data[key] = value
	s.mu.Unlock()
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
	delete(s.value.data, key)
	return 0, nil
}

func (s *MemoryStore) Exists(key string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, exists := s.value.data[key]
	return exists, nil
}

func startServer() {
	err := http.ListenAndServe("localhost:8080", nil)
	log.Println("Server started on localhost:8080")
	if err != nil {
		log.Println("Failed to start server")
		return
	}
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
	log.Println("Value set")
}

func (s *MemoryStore) getValueHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}
	key := r.FormValue("key")
	log.Println("key =", key)
	value, _ := s.Get(key)
	log.Println("value =", string(value))
}

func (s *MemoryStore) existsValueHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}
	key := r.FormValue("key")
	exists, _ := s.Exists(key)
	if exists {
		fmt.Fprintf(w, "Key exists")
	} else {
		fmt.Fprintf(w, "Key does not exist")
	}
}

func (s *MemoryStore) deleteValueHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}
	key := r.FormValue("key")
	log.Println("key =", key)
	value, _ := s.Delete(key)
	log.Println("value =", string(value))
}

func main() {
	store := &MemoryStore{
		mu:    sync.RWMutex{},
		value: StoreValue{make(map[string][]byte), time.Now().Add(60 * time.Second)},
	}
	// http.HandleFunc("/health", store.getHealth)
	http.HandleFunc("/cache/get", store.getValueHandler)
	http.HandleFunc("/cache/set", store.setValueHandler)
	http.HandleFunc("/cache/delete", store.deleteValueHandler)
	http.HandleFunc("/cache/exists", store.existsValueHandler)
	startServer()
}
