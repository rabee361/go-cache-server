package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

type Response struct {
	Status  string      `json:"status"`
	Code    string      `json:"code"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
	Ts      int64       `json:"ts"`
}

func writeJSON(w http.ResponseWriter, status int, code string, message string, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	resp := Response{
		Status:  httpStatusText(status),
		Code:    code,
		Message: message,
		Data:    data,
		Ts:      time.Now().Unix(),
	}
	b, _ := json.Marshal(resp)
	w.Write(b)
}

func httpStatusText(code int) string {
	if code >= 200 && code < 300 {
		return "ok"
	}
	return "error"
}

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
		writeJSON(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", nil)
		return
	}
	key := r.FormValue("key")
	if key == "" {
		writeJSON(w, http.StatusBadRequest, "MISSING_KEY", "missing key parameter", nil)
		return
	}
	value := r.FormValue("value")
	if _, err := s.Set(key, []byte(value)); err != nil {
		writeJSON(w, http.StatusInternalServerError, "SET_FAILED", "set operation failed: "+err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, "SET_OK", "", map[string]string{"key": key})
}

func (s *MemoryStore) getValueHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", nil)
		return
	}
	key := r.FormValue("key")
	if key == "" {
		writeJSON(w, http.StatusBadRequest, "MISSING_KEY", "missing key parameter", nil)
		return
	}
	value, exists := s.Get(key)
	if !exists {
		writeJSON(w, http.StatusNotFound, "NOT_FOUND", "key not found", map[string]string{"key": key})
		return
	}
	writeJSON(w, http.StatusOK, "GET_OK", "", map[string]string{"key": key, "value": string(value)})
}

func (s *MemoryStore) existsValueHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", nil)
		return
	}
	key := r.FormValue("key")
	if key == "" {
		writeJSON(w, http.StatusBadRequest, "MISSING_KEY", "missing key parameter", nil)
		return
	}
	exists, _ := s.Exists(key)
	if !exists {
		writeJSON(w, http.StatusNotFound, "NOT_FOUND", "key not found", map[string]interface{}{"key": key, "exists": false})
		return
	}
	writeJSON(w, http.StatusOK, "EXISTS_OK", "", map[string]interface{}{"key": key, "exists": true})
}

func (s *MemoryStore) deleteValueHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", nil)
		return
	}
	key := r.FormValue("key")
	if key == "" {
		writeJSON(w, http.StatusBadRequest, "MISSING_KEY", "missing key parameter", nil)
		return
	}
	if _, err := s.Delete(key); err != nil {
		writeJSON(w, http.StatusInternalServerError, "DELETE_FAILED", "delete operation failed: "+err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, "DELETE_OK", "", map[string]string{"key": key})
}

func (s *MemoryStore) healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", nil)
		return
	}
	writeJSON(w, http.StatusOK, "HEALTH_OK", "", map[string]bool{"alive": true})
}

func (s *MemoryStore) readyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", nil)
		return
	}

	key := fmt.Sprintf("_health_check_%d", time.Now().UnixNano())
	testValue := []byte("ok")

	if _, err := s.Set(key, testValue); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, "NOT_READY", "set failed: "+err.Error(), nil)
		return
	}
	defer s.Delete(key)

	got, ok := s.Get(key)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, "NOT_READY", "get failed: key not found after set", nil)
		return
	}

	if string(got) != string(testValue) {
		writeJSON(w, http.StatusServiceUnavailable, "NOT_READY", "get returned wrong value", nil)
		return
	}

	if _, err := s.Delete(key); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, "NOT_READY", "delete failed: "+err.Error(), nil)
		return
	}

	writeJSON(w, http.StatusOK, "READY", "", map[string]bool{"ready": true})
}

func main() {
	store := &MemoryStore{
		mu:    sync.RWMutex{},
		value: StoreValue{make(map[string][]byte), time.Now().Add(60 * time.Second)},
	}
	http.HandleFunc("/health", store.healthHandler)
	http.HandleFunc("/health/ready", store.readyHandler)
	http.HandleFunc("/cache/get", store.getValueHandler)
	http.HandleFunc("/cache/set", store.setValueHandler)
	http.HandleFunc("/cache/delete", store.deleteValueHandler)
	http.HandleFunc("/cache/exists", store.existsValueHandler)
	startServer()
}
