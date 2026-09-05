package main

// NOTE: The Store interface declares Delete(key string) uint64 and Exists(key string) bool,
// but MemoryStore implements Delete(key string) (uint64, error) and Exists(key string) (bool, error).
// This is a signature mismatch — MemoryStore does NOT satisfy the Store interface as written.
// These tests exercise the concrete MemoryStore type directly.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// parseResponse decodes the JSON Response envelope from an httptest.ResponseRecorder.
func parseResponse(t *testing.T, w *httptest.ResponseRecorder) Response {
	t.Helper()
	var resp Response
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode JSON response: %v\nbody: %s", err, w.Body.String())
	}
	return resp
}

// parseHTTPResponse decodes the JSON Response envelope from an *http.Response.
func parseHTTPResponse(t *testing.T, resp *http.Response) Response {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	var r Response
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("failed to decode JSON response: %v\nbody: %s", err, string(body))
	}
	return r
}

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
		{"delete_existing_key", "foo", map[string][]byte{"foo": []byte("bar")}, 0, false, "foo", false},
		{"delete_missing_key", "nope", map[string][]byte{}, 0, false, "nope", false},
		{"delete_empty_key", "", map[string][]byte{"": []byte("val")}, 0, false, "", false},
		{"delete_then_set_again", "x", map[string][]byte{"x": []byte("1")}, 0, false, "x", true},
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
// Phase 2: HTTP handler tests
// ---------------------------------------------------------------------------

func TestSetValueHandler(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		key        string
		value      string
		wantCode   int
		wantStatus string
		wantJSON   string
		verifyKey  string
		verifyVal  string
	}{
		{"set_valid_post", http.MethodPost, "foo", "bar", 200, "ok", "SET_OK", "foo", "bar"},
		{"set_wrong_method_get", http.MethodGet, "", "", 405, "error", "METHOD_NOT_ALLOWED", "", ""},
		{"set_wrong_method_put", http.MethodPut, "", "", 405, "error", "METHOD_NOT_ALLOWED", "", ""},
		{"set_empty_key_value", http.MethodPost, "", "", 400, "error", "MISSING_KEY", "", ""},
		{"set_overwrite", http.MethodPost, "k", "v2", 200, "ok", "SET_OK", "k", "v2"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore()

			if tc.name == "set_overwrite" {
				s.Set("k", []byte("v1"))
			}

			var req *http.Request
			if tc.method == http.MethodPost {
				body := strings.NewReader("key=" + tc.key + "&value=" + tc.value)
				req = httptest.NewRequest(tc.method, "/cache/set", body)
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			} else {
				req = httptest.NewRequest(tc.method, "/cache/set", nil)
			}

			w := httptest.NewRecorder()
			s.setValueHandler(w, req)

			if w.Code != tc.wantCode {
				t.Errorf("status = %d, want %d", w.Code, tc.wantCode)
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			resp := parseResponse(t, w)
			if resp.Status != tc.wantStatus {
				t.Errorf("status field = %q, want %q", resp.Status, tc.wantStatus)
			}
			if resp.Code != tc.wantJSON {
				t.Errorf("code field = %q, want %q", resp.Code, tc.wantJSON)
			}

			if tc.verifyKey != "" {
				got, ok := s.Get(tc.verifyKey)
				if !ok {
					t.Errorf("store has no key %q after Set", tc.verifyKey)
				} else if string(got) != tc.verifyVal {
					t.Errorf("store[%q] = %q, want %q", tc.verifyKey, got, tc.verifyVal)
				}
			}
		})
	}
}

func TestGetValueHandler(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		key         string
		prePopulate map[string][]byte
		wantCode    int
		wantStatus  string
		wantCode2   string
		wantValue   string
		wantExists  bool
	}{
		{"get_existing_key", http.MethodGet, "foo", map[string][]byte{"foo": []byte("bar")}, 200, "ok", "GET_OK", "bar", true},
		{"get_missing_key", http.MethodGet, "nope", map[string][]byte{}, 404, "error", "NOT_FOUND", "", false},
		{"get_wrong_method", http.MethodPost, "", map[string][]byte{}, 405, "error", "METHOD_NOT_ALLOWED", "", false},
		{"get_empty_key", http.MethodGet, "", map[string][]byte{"": []byte("found")}, 400, "error", "MISSING_KEY", "", false},
		{"get_empty_key_missing", http.MethodGet, "", map[string][]byte{}, 400, "error", "MISSING_KEY", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore()
			populateStore(s, tc.prePopulate)

			url := "/cache/get?key=" + tc.key
			req := httptest.NewRequest(tc.method, url, nil)
			w := httptest.NewRecorder()

			s.getValueHandler(w, req)

			if w.Code != tc.wantCode {
				t.Errorf("status = %d, want %d", w.Code, tc.wantCode)
			}

			resp := parseResponse(t, w)
			if resp.Status != tc.wantStatus {
				t.Errorf("status field = %q, want %q", resp.Status, tc.wantStatus)
			}
			if resp.Code != tc.wantCode2 {
				t.Errorf("code field = %q, want %q", resp.Code, tc.wantCode2)
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			if tc.wantExists && resp.Data != nil {
				data, ok := resp.Data.(map[string]interface{})
				if !ok {
					t.Fatalf("data is not a map: %v", resp.Data)
				}
				if gotVal, ok := data["value"]; !ok || gotVal.(string) != tc.wantValue {
					t.Errorf("data.value = %v, want %q", gotVal, tc.wantValue)
				}
			}
		})
	}
}

func TestExistsValueHandler(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		key         string
		prePopulate map[string][]byte
		wantCode    int
		wantStatus  string
		wantExists  bool
	}{
		{"exists_present_key", http.MethodGet, "foo", map[string][]byte{"foo": []byte("bar")}, 200, "ok", true},
		{"exists_absent_key", http.MethodGet, "nope", map[string][]byte{}, 404, "error", false},
		{"exists_wrong_method", http.MethodPost, "", map[string][]byte{}, 405, "error", false},
		{"exists_empty_key_present", http.MethodGet, "", map[string][]byte{"": []byte("x")}, 400, "error", false},
		{"exists_after_delete", http.MethodGet, "tmp", map[string][]byte{"tmp": []byte("v")}, 404, "error", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore()
			populateStore(s, tc.prePopulate)

			if tc.name == "exists_after_delete" {
				s.Delete("tmp")
			}

			url := "/cache/exists?key=" + tc.key
			req := httptest.NewRequest(tc.method, url, nil)
			w := httptest.NewRecorder()

			s.existsValueHandler(w, req)

			if w.Code != tc.wantCode {
				t.Errorf("status = %d, want %d", w.Code, tc.wantCode)
			}

			resp := parseResponse(t, w)
			if resp.Status != tc.wantStatus {
				t.Errorf("status field = %q, want %q", resp.Status, tc.wantStatus)
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			if tc.wantCode == http.StatusOK && resp.Data != nil {
				data, ok := resp.Data.(map[string]interface{})
				if !ok {
					t.Fatalf("data is not a map: %v", resp.Data)
				}
				if gotExists, ok := data["exists"]; !ok || gotExists.(bool) != tc.wantExists {
					t.Errorf("data.exists = %v, want %v", gotExists, tc.wantExists)
				}
			}
		})
	}
}

func TestDeleteValueHandler(t *testing.T) {
	tests := []struct {
		name         string
		method       string
		key          string
		prePopulate  map[string][]byte
		wantCode     int
		wantStatus   string
		wantJSONCode string
		verifyKey    string
		verifyExists bool
	}{
		{"delete_existing_key", http.MethodDelete, "foo", map[string][]byte{"foo": []byte("bar")}, 200, "ok", "DELETE_OK", "foo", false},
		{"delete_missing_key", http.MethodDelete, "nope", map[string][]byte{}, 200, "ok", "DELETE_OK", "nope", false},
		{"delete_wrong_method", http.MethodPost, "", map[string][]byte{}, 405, "error", "METHOD_NOT_ALLOWED", "", true},
		{"delete_empty_key", http.MethodDelete, "", map[string][]byte{"": []byte("val")}, 400, "error", "MISSING_KEY", "", false},
		{"delete_then_verify_absent", http.MethodDelete, "x", map[string][]byte{"x": []byte("data")}, 200, "ok", "DELETE_OK", "x", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore()
			populateStore(s, tc.prePopulate)

			url := "/cache/delete?key=" + tc.key
			req := httptest.NewRequest(tc.method, url, nil)
			w := httptest.NewRecorder()

			s.deleteValueHandler(w, req)

			if w.Code != tc.wantCode {
				t.Errorf("status = %d, want %d", w.Code, tc.wantCode)
			}

			resp := parseResponse(t, w)
			if resp.Status != tc.wantStatus {
				t.Errorf("status field = %q, want %q", resp.Status, tc.wantStatus)
			}
			if resp.Code != tc.wantJSONCode {
				t.Errorf("code field = %q, want %q", resp.Code, tc.wantJSONCode)
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			if tc.verifyKey != "" {
				_, exists := s.Get(tc.verifyKey)
				if exists != tc.verifyExists {
					t.Errorf("Get(%q) exists = %v after Delete, want %v", tc.verifyKey, exists, tc.verifyExists)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Health check handler tests
// ---------------------------------------------------------------------------

func TestHealthHandler(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		wantCode   int
		wantStatus string
		wantJSON   string
	}{
		{"health_valid_get", http.MethodGet, 200, "ok", "HEALTH_OK"},
		{"health_wrong_method_post", http.MethodPost, 405, "error", "METHOD_NOT_ALLOWED"},
		{"health_wrong_method_put", http.MethodPut, 405, "error", "METHOD_NOT_ALLOWED"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore()

			req := httptest.NewRequest(tc.method, "/health", nil)
			w := httptest.NewRecorder()
			s.healthHandler(w, req)

			if w.Code != tc.wantCode {
				t.Errorf("status = %d, want %d", w.Code, tc.wantCode)
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			resp := parseResponse(t, w)
			if resp.Status != tc.wantStatus {
				t.Errorf("status field = %q, want %q", resp.Status, tc.wantStatus)
			}
			if resp.Code != tc.wantJSON {
				t.Errorf("code field = %q, want %q", resp.Code, tc.wantJSON)
			}
		})
	}
}

func TestReadyHandler(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		wantCode   int
		wantStatus string
		wantJSON   string
	}{
		{"ready_store_healthy", http.MethodGet, 200, "ok", "READY"},
		{"ready_wrong_method_post", http.MethodPost, 405, "error", "METHOD_NOT_ALLOWED"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore()

			req := httptest.NewRequest(tc.method, "/health/ready", nil)
			w := httptest.NewRecorder()
			s.readyHandler(w, req)

			if w.Code != tc.wantCode {
				t.Errorf("status = %d, want %d", w.Code, tc.wantCode)
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			resp := parseResponse(t, w)
			if resp.Status != tc.wantStatus {
				t.Errorf("status field = %q, want %q", resp.Status, tc.wantStatus)
			}
			if resp.Code != tc.wantJSON {
				t.Errorf("code field = %q, want %q", resp.Code, tc.wantJSON)
			}
		})
	}

	t.Run("ready_cleanup", func(t *testing.T) {
		s := newTestStore()
		req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
		w := httptest.NewRecorder()
		s.readyHandler(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
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

func TestMissingQueryParameter(t *testing.T) {
	tests := []struct {
		name       string
		handler    string
		method     string
		wantCode   int
		wantStatus string
		wantJSON   string
	}{
		{"set_missing_key_param", "set", http.MethodPost, 400, "error", "MISSING_KEY"},
		{"get_missing_key_param", "get", http.MethodGet, 400, "error", "MISSING_KEY"},
		{"exists_missing_key_param", "exists", http.MethodGet, 400, "error", "MISSING_KEY"},
		{"delete_missing_key_param", "delete", http.MethodDelete, 400, "error", "MISSING_KEY"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore()

			var url string
			switch tc.handler {
			case "set":
				url = "/cache/set"
			case "get":
				url = "/cache/get"
			case "exists":
				url = "/cache/exists"
			case "delete":
				url = "/cache/delete"
			}

			var req *http.Request
			if tc.method == http.MethodPost {
				req = httptest.NewRequest(tc.method, url, nil)
			} else {
				req = httptest.NewRequest(tc.method, url, nil)
			}

			w := httptest.NewRecorder()

			switch tc.handler {
			case "set":
				s.setValueHandler(w, req)
			case "get":
				s.getValueHandler(w, req)
			case "exists":
				s.existsValueHandler(w, req)
			case "delete":
				s.deleteValueHandler(w, req)
			}

			if w.Code != tc.wantCode {
				t.Errorf("status = %d, want %d", w.Code, tc.wantCode)
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			resp := parseResponse(t, w)
			if resp.Status != tc.wantStatus {
				t.Errorf("status field = %q, want %q", resp.Status, tc.wantStatus)
			}
			if resp.Code != tc.wantJSON {
				t.Errorf("code field = %q, want %q", resp.Code, tc.wantJSON)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Phase 3: Integration test
// ---------------------------------------------------------------------------

func TestIntegrationEndToEnd(t *testing.T) {
	store := newTestStore()
	mux := http.NewServeMux()
	mux.HandleFunc("/cache/get", store.getValueHandler)
	mux.HandleFunc("/cache/set", store.setValueHandler)
	mux.HandleFunc("/cache/delete", store.deleteValueHandler)
	mux.HandleFunc("/cache/exists", store.existsValueHandler)
	mux.HandleFunc("/health", store.healthHandler)
	mux.HandleFunc("/health/ready", store.readyHandler)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	postSet := func(key, value string) (*http.Response, error) {
		body := strings.NewReader("key=" + key + "&value=" + value)
		return http.Post(ts.URL+"/cache/set", "application/x-www-form-urlencoded", body)
	}

	t.Run("e2e_set_and_get", func(t *testing.T) {
		resp, err := postSet("hello", "world")
		if err != nil {
			t.Fatalf("POST /cache/set failed: %v", err)
		}
		r := parseHTTPResponse(t, resp)
		if resp.StatusCode != 200 {
			t.Errorf("POST /cache/set status = %d, want 200", resp.StatusCode)
		}
		if r.Code != "SET_OK" {
			t.Errorf("POST /cache/set code = %q, want SET_OK", r.Code)
		}

		val, ok := store.Get("hello")
		if !ok {
			t.Fatal("store.Get(\"hello\") exists = false after POST")
		}
		if string(val) != "world" {
			t.Errorf("store.Get(\"hello\") = %q, want %q", val, "world")
		}
	})

	t.Run("e2e_get_missing", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/cache/get?key=nonexistent")
		if err != nil {
			t.Fatalf("GET /cache/get failed: %v", err)
		}
		r := parseHTTPResponse(t, resp)
		if resp.StatusCode != 404 {
			t.Errorf("GET /cache/get status = %d, want 404", resp.StatusCode)
		}
		if r.Code != "NOT_FOUND" {
			t.Errorf("GET /cache/get code = %q, want NOT_FOUND", r.Code)
		}
	})

	t.Run("e2e_exists_present", func(t *testing.T) {
		postSet("k", "v")

		resp, err := http.Get(ts.URL + "/cache/exists?key=k")
		if err != nil {
			t.Fatalf("GET /cache/exists failed: %v", err)
		}
		r := parseHTTPResponse(t, resp)
		if resp.StatusCode != 200 {
			t.Errorf("GET /cache/exists status = %d, want 200", resp.StatusCode)
		}
		if r.Code != "EXISTS_OK" {
			t.Errorf("GET /cache/exists code = %q, want EXISTS_OK", r.Code)
		}
		data, ok := r.Data.(map[string]interface{})
		if !ok {
			t.Fatalf("data is not a map: %v", r.Data)
		}
		if exists, ok := data["exists"]; !ok || exists.(bool) != true {
			t.Errorf("data.exists = %v, want true", exists)
		}
	})

	t.Run("e2e_exists_absent", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/cache/exists?key=absent-key")
		if err != nil {
			t.Fatalf("GET /cache/exists failed: %v", err)
		}
		r := parseHTTPResponse(t, resp)
		if resp.StatusCode != 404 {
			t.Errorf("GET /cache/exists status = %d, want 404", resp.StatusCode)
		}
		if r.Code != "NOT_FOUND" {
			t.Errorf("GET /cache/exists code = %q, want NOT_FOUND", r.Code)
		}
	})

	t.Run("e2e_delete_and_verify", func(t *testing.T) {
		postSet("del-me", "data")

		exists, _ := store.Exists("del-me")
		if !exists {
			t.Fatal("key \"del-me\" should exist after SET")
		}

		req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/cache/delete?key=del-me", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("DELETE /cache/delete failed: %v", err)
		}
		r := parseHTTPResponse(t, resp)
		if resp.StatusCode != 200 {
			t.Errorf("DELETE /cache/delete status = %d, want 200", resp.StatusCode)
		}
		if r.Code != "DELETE_OK" {
			t.Errorf("DELETE /cache/delete code = %q, want DELETE_OK", r.Code)
		}

		exists, _ = store.Exists("del-me")
		if exists {
			t.Error("key \"del-me\" should be absent after DELETE")
		}
	})

	t.Run("e2e_set_wrong_method", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/cache/set")
		if err != nil {
			t.Fatalf("GET /cache/set failed: %v", err)
		}
		r := parseHTTPResponse(t, resp)
		if resp.StatusCode != 405 {
			t.Errorf("GET /cache/set status = %d, want 405", resp.StatusCode)
		}
		if r.Code != "METHOD_NOT_ALLOWED" {
			t.Errorf("GET /cache/set code = %q, want METHOD_NOT_ALLOWED", r.Code)
		}
	})

	t.Run("e2e_overwrite", func(t *testing.T) {
		postSet("k", "old")
		postSet("k", "new")

		val, ok := store.Get("k")
		if !ok {
			t.Fatal("store.Get(\"k\") exists = false after overwrite")
		}
		if string(val) != "new" {
			t.Errorf("store.Get(\"k\") = %q, want %q", val, "new")
		}
	})

	t.Run("e2e_health", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/health")
		if err != nil {
			t.Fatalf("GET /health failed: %v", err)
		}
		r := parseHTTPResponse(t, resp)
		if resp.StatusCode != 200 {
			t.Errorf("GET /health status = %d, want 200", resp.StatusCode)
		}
		if r.Code != "HEALTH_OK" {
			t.Errorf("GET /health code = %q, want HEALTH_OK", r.Code)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("GET /health Content-Type = %q, want application/json", ct)
		}
	})

	t.Run("e2e_ready", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/health/ready")
		if err != nil {
			t.Fatalf("GET /health/ready failed: %v", err)
		}
		r := parseHTTPResponse(t, resp)
		if resp.StatusCode != 200 {
			t.Errorf("GET /health/ready status = %d, want 200", resp.StatusCode)
		}
		if r.Code != "READY" {
			t.Errorf("GET /health/ready code = %q, want READY", r.Code)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("GET /health/ready Content-Type = %q, want application/json", ct)
		}
	})
}
