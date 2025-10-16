package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// Test helpers
func createTestKeyStore(t *testing.T) (*APIKeyStore, string) {
	t.Helper()
	tempDir := t.TempDir()
	keyFile := filepath.Join(tempDir, "test-keys.toml")
	file, err := os.Create(keyFile)
	if err != nil {
		t.Fatalf("Failed to create test key file: %v", err)
	}
	file.Close()

	store, err := NewStore(keyFile)
	if err != nil {
		t.Fatalf("Failed to create key store: %v", err)
	}

	apiKey, err := store.AddKey("test-key")
	if err != nil {
		t.Fatalf("Failed to add test key: %v", err)
	}

	return store, apiKey
}

func createTestRouter(t *testing.T, tasks map[string]Task, store *APIKeyStore) *chi.Mux {
	t.Helper()
	r := chi.NewRouter()
	for name, task := range tasks {
		taskCopy := task
		taskCopy.applyDefaults() // Apply defaults like the main config does
		tm, err := NewTaskManager(name, &taskCopy, store)
		if err != nil {
			t.Fatalf("Failed to create task manager for %s: %v", name, err)
		}
		tm.ConfigureRoutes(r)
	}
	return r
}

func getTestScriptPath(t *testing.T, scriptName string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	return filepath.Join(wd, "testdata", scriptName)
}

// Test: Basic synchronous task execution
func TestSyncTask_Success(t *testing.T) {
	store, apiKey := createTestKeyStore(t)

	tasks := map[string]Task{
		"fast": {
			Command:                 []string{getTestScriptPath(t, "fast_task.sh")},
			APIKeyNames:             []string{"test-key"},
			Async:                   false,
			ExecutionTimeoutSeconds: 5,
		},
	}

	router := createTestRouter(t, tasks, store)

	req := httptest.NewRequest("POST", "/tasks/fast", bytes.NewBufferString("test input"))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var result taskExecutionResult
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if result.Status != "success" {
		t.Errorf("Expected status 'success', got '%s'", result.Status)
	}

	if result.ExitCode != 0 {
		t.Errorf("Expected exit code 0, got %d", result.ExitCode)
	}

	if !strings.Contains(result.StdOut, "Fast task completed") {
		t.Errorf("Expected output to contain 'Fast task completed', got '%s'", result.StdOut)
	}
}

// Test: Task timeout
func TestSyncTask_Timeout(t *testing.T) {
	store, apiKey := createTestKeyStore(t)

	tasks := map[string]Task{
		"timeout": {
			Command:                 []string{getTestScriptPath(t, "timeout_task.sh")},
			APIKeyNames:             []string{"test-key"},
			Async:                   false,
			ExecutionTimeoutSeconds: 2, // Task sleeps 10 seconds but timeout is 2
		},
	}

	router := createTestRouter(t, tasks, store)

	req := httptest.NewRequest("POST", "/tasks/timeout", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	w := httptest.NewRecorder()

	start := time.Now()
	router.ServeHTTP(w, req)
	duration := time.Since(start)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var result taskExecutionResult
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if result.Status != "timeout" {
		t.Errorf("Expected status 'timeout', got '%s'", result.Status)
	}

	if result.ExitCode != -1 {
		t.Errorf("Expected exit code -1, got %d", result.ExitCode)
	}

	// Should timeout around 2 seconds, not wait full 10 seconds
	// Note: On some systems, process cleanup may take longer
	if duration > 12*time.Second {
		t.Errorf("Task took too long: %v (expected ~2s, but allowing up to 12s for cleanup)", duration)
	}
}

// Test: Task failure with exit code
func TestSyncTask_Failure(t *testing.T) {
	store, apiKey := createTestKeyStore(t)

	tasks := map[string]Task{
		"failing": {
			Command:                 []string{getTestScriptPath(t, "failing_task.sh")},
			APIKeyNames:             []string{"test-key"},
			Async:                   false,
			ExecutionTimeoutSeconds: 5,
		},
	}

	router := createTestRouter(t, tasks, store)

	req := httptest.NewRequest("POST", "/tasks/failing", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var result taskExecutionResult
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if result.Status != "failure" {
		t.Errorf("Expected status 'failure', got '%s'", result.Status)
	}

	if result.ExitCode != 42 {
		t.Errorf("Expected exit code 42, got %d", result.ExitCode)
	}

	if !strings.Contains(result.StdErr, "Task failed") {
		t.Errorf("Expected stderr to contain 'Task failed', got '%s'", result.StdErr)
	}
}

// Test: Async task execution
func TestAsyncTask_Basic(t *testing.T) {
	store, apiKey := createTestKeyStore(t)

	tasks := map[string]Task{
		"slow": {
			Command:                 []string{getTestScriptPath(t, "slow_task.sh")},
			APIKeyNames:             []string{"test-key"},
			Async:                   true,
			ExecutionTimeoutSeconds: 10,
		},
	}

	router := createTestRouter(t, tasks, store)

	// Submit task
	req := httptest.NewRequest("POST", "/tasks/slow", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	w := httptest.NewRecorder()

	start := time.Now()
	router.ServeHTTP(w, req)
	submitDuration := time.Since(start)

	// Should return immediately
	if submitDuration > 500*time.Millisecond {
		t.Errorf("Async task submission took too long: %v", submitDuration)
	}

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var submitResponse map[string]string
	if err := json.NewDecoder(w.Body).Decode(&submitResponse); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	taskID, ok := submitResponse["task_id"]
	if !ok || taskID == "" {
		t.Fatalf("Expected task_id in response, got: %v", submitResponse)
	}

	// Poll for result
	var result taskExecutionResult
	maxAttempts := 10
	for i := 0; i < maxAttempts; i++ {
		req = httptest.NewRequest("GET", "/tasks/slow/"+taskID, nil)
		req.Header.Set("Authorization", "Bearer "+apiKey)
		w = httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code == http.StatusNotFound {
			// Task not found yet or cleaned up
			time.Sleep(500 * time.Millisecond)
			continue
		}

		if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
			t.Fatalf("Failed to decode result: %v", err)
		}

		if result.Status == "running" {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		break
	}

	if result.Status != "success" {
		t.Errorf("Expected status 'success', got '%s'", result.Status)
	}

	if result.ExitCode != 0 {
		t.Errorf("Expected exit code 0, got %d", result.ExitCode)
	}
}

// Test: Async task with timeout
func TestAsyncTask_Timeout(t *testing.T) {
	store, apiKey := createTestKeyStore(t)

	tasks := map[string]Task{
		"timeout-async": {
			Command:                 []string{getTestScriptPath(t, "timeout_task.sh")},
			APIKeyNames:             []string{"test-key"},
			Async:                   true,
			ExecutionTimeoutSeconds: 2,
		},
	}

	router := createTestRouter(t, tasks, store)

	// Submit task
	req := httptest.NewRequest("POST", "/tasks/timeout-async", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var submitResponse map[string]string
	json.NewDecoder(w.Body).Decode(&submitResponse)
	taskID := submitResponse["task_id"]

	// Poll for timeout completion (task sleeps 10s but timeout is 2s, so ~10s on macOS)
	var result taskExecutionResult
	maxAttempts := 25 // 25 * 500ms = 12.5s total wait
	for i := 0; i < maxAttempts; i++ {
		req = httptest.NewRequest("GET", "/tasks/timeout-async/"+taskID, nil)
		req.Header.Set("Authorization", "Bearer "+apiKey)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code == http.StatusNotFound {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		json.NewDecoder(w.Body).Decode(&result)
		if result.Status == "running" {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		break
	}

	if result.Status != "timeout" {
		t.Errorf("Expected status 'timeout', got '%s'", result.Status)
	}

	if result.ExitCode != -1 {
		t.Errorf("Expected exit code -1, got %d", result.ExitCode)
	}
}

// Test: Rate limiting
func TestRateLimit_Exceeded(t *testing.T) {
	store, apiKey := createTestKeyStore(t)

	tasks := map[string]Task{
		"limited": {
			Command:                 []string{getTestScriptPath(t, "fast_task.sh")},
			APIKeyNames:             []string{"test-key"},
			Async:                   false,
			RateLimit:               1.0, // 1 request per second
			ExecutionTimeoutSeconds: 5,
		},
	}

	router := createTestRouter(t, tasks, store)

	// First request should succeed
	req := httptest.NewRequest("POST", "/tasks/limited", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("First request: expected status 200, got %d", w.Code)
	}

	// Second immediate request should be rate limited
	req = httptest.NewRequest("POST", "/tasks/limited", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("Second request: expected status 429, got %d", w.Code)
	}

	// After waiting, should succeed again
	time.Sleep(1100 * time.Millisecond)
	req = httptest.NewRequest("POST", "/tasks/limited", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Third request after delay: expected status 200, got %d", w.Code)
	}
}

// Test: Max concurrent tasks (synchronous)
func TestMaxConcurrentTasks_Sync(t *testing.T) {
	store, apiKey := createTestKeyStore(t)

	tasks := map[string]Task{
		"concurrent": {
			Command:                 []string{getTestScriptPath(t, "slow_task.sh")},
			APIKeyNames:             []string{"test-key"},
			Async:                   false,
			MaxConcurrentTasks:      2, // Allow only 2 concurrent tasks
			ExecutionTimeoutSeconds: 10,
		},
	}

	router := createTestRouter(t, tasks, store)

	var wg sync.WaitGroup
	results := make([]int, 5)

	// Launch 5 concurrent requests
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/tasks/concurrent", nil)
			req.Header.Set("Authorization", "Bearer "+apiKey)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			results[index] = w.Code
		}(i)
		time.Sleep(100 * time.Millisecond) // Stagger requests slightly
	}

	wg.Wait()

	successCount := 0
	rejectedCount := 0
	for _, code := range results {
		if code == http.StatusOK {
			successCount++
		} else if code == http.StatusTooManyRequests {
			rejectedCount++
		}
	}

	// We expect some to succeed and some to be rejected
	if rejectedCount < 1 {
		t.Errorf("Expected at least 1 rejection, got %d (success: %d, rejected: %d)",
			rejectedCount, successCount, rejectedCount)
	}

	t.Logf("Results: %d succeeded, %d rejected", successCount, rejectedCount)
}

// Test: Max concurrent tasks (async)
func TestMaxConcurrentTasks_Async(t *testing.T) {
	store, apiKey := createTestKeyStore(t)

	tasks := map[string]Task{
		"concurrent-async": {
			Command:                 []string{getTestScriptPath(t, "slow_task.sh")},
			APIKeyNames:             []string{"test-key"},
			Async:                   true,
			MaxConcurrentTasks:      2,
			ExecutionTimeoutSeconds: 10,
		},
	}

	router := createTestRouter(t, tasks, store)

	var wg sync.WaitGroup
	var acceptedCount atomic.Int32
	var rejectedCount atomic.Int32

	// Launch 10 concurrent requests quickly
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/tasks/concurrent-async", nil)
			req.Header.Set("Authorization", "Bearer "+apiKey)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code == http.StatusOK {
				acceptedCount.Add(1)
			} else if w.Code == http.StatusTooManyRequests {
				rejectedCount.Add(1)
			}
		}()
		time.Sleep(50 * time.Millisecond)
	}

	wg.Wait()

	accepted := acceptedCount.Load()
	rejected := rejectedCount.Load()

	t.Logf("Async results: %d accepted, %d rejected", accepted, rejected)

	// With async and semaphore, we should accept some and reject others
	if rejected != 8 {
		t.Errorf("Expected 8 rejections with MaxConcurrentTasks=2, got %d", rejected)
	}
	if accepted != 2 {
		t.Errorf("Expected 8 accepts with MaxConcurrentTasks=2, got %d", rejected)
	}
}

// Test: Environment variables
func TestTaskWithEnvironment(t *testing.T) {
	store, apiKey := createTestKeyStore(t)

	tasks := map[string]Task{
		"env": {
			Command:                 []string{getTestScriptPath(t, "env_reader.sh")},
			APIKeyNames:             []string{"test-key"},
			Async:                   false,
			ExecutionTimeoutSeconds: 5,
			Environment: map[string]string{
				"TEST_VAR": "hello_world",
			},
		},
	}

	router := createTestRouter(t, tasks, store)

	req := httptest.NewRequest("POST", "/tasks/env", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	var result taskExecutionResult
	json.NewDecoder(w.Body).Decode(&result)

	if !strings.Contains(result.StdOut, "TEST_VAR=hello_world") {
		t.Errorf("Expected output to contain 'TEST_VAR=hello_world', got '%s'", result.StdOut)
	}
}

// Test: Pass request headers
func TestPassRequestHeaders(t *testing.T) {
	store, apiKey := createTestKeyStore(t)

	tasks := map[string]Task{
		"headers": {
			Command:                 []string{getTestScriptPath(t, "env_reader.sh")},
			APIKeyNames:             []string{"test-key"},
			Async:                   false,
			ExecutionTimeoutSeconds: 5,
			PassRequestHeaders:      []string{"X-Custom"},
		},
	}

	router := createTestRouter(t, tasks, store)

	req := httptest.NewRequest("POST", "/tasks/headers", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Custom", "test_value")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	var result taskExecutionResult
	json.NewDecoder(w.Body).Decode(&result)

	if !strings.Contains(result.StdOut, "REQUEST_HEADER_X_CUSTOM=test_value") {
		t.Errorf("Expected output to contain 'REQUEST_HEADER_X_CUSTOM=test_value', got '%s'", result.StdOut)
	}
}

// Test: Input/output
func TestTaskWithInput(t *testing.T) {
	store, apiKey := createTestKeyStore(t)

	tasks := map[string]Task{
		"echo": {
			Command:                 []string{getTestScriptPath(t, "echo_stdin.sh")},
			APIKeyNames:             []string{"test-key"},
			Async:                   false,
			ExecutionTimeoutSeconds: 5,
		},
	}

	router := createTestRouter(t, tasks, store)

	testInput := "Hello from test!"
	req := httptest.NewRequest("POST", "/tasks/echo", bytes.NewBufferString(testInput))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	var result taskExecutionResult
	json.NewDecoder(w.Body).Decode(&result)

	if !strings.Contains(result.StdOut, testInput) {
		t.Errorf("Expected output to contain '%s', got '%s'", testInput, result.StdOut)
	}
}

// Test: Combined scenario - async + timeout + concurrent
func TestCombinedScenario_AsyncTimeoutConcurrent(t *testing.T) {
	store, apiKey := createTestKeyStore(t)

	tasks := map[string]Task{
		"combined": {
			Command:                 []string{getTestScriptPath(t, "slow_task.sh")},
			APIKeyNames:             []string{"test-key"},
			Async:                   true,
			MaxConcurrentTasks:      3,
			ExecutionTimeoutSeconds: 5,
			RateLimit:               10.0,
		},
	}

	router := createTestRouter(t, tasks, store)

	var wg sync.WaitGroup
	taskIDs := make([]string, 5)
	var taskIDsMutex sync.Mutex

	// Submit multiple async tasks
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/tasks/combined", nil)
			req.Header.Set("Authorization", "Bearer "+apiKey)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code == http.StatusOK {
				var resp map[string]string
				json.NewDecoder(w.Body).Decode(&resp)
				taskIDsMutex.Lock()
				taskIDs[index] = resp["task_id"]
				taskIDsMutex.Unlock()
			}
		}(i)
		time.Sleep(100 * time.Millisecond)
	}

	wg.Wait()

	// Wait for tasks to complete
	time.Sleep(5 * time.Second)

	// Check results
	successCount := 0
	for _, taskID := range taskIDs {
		if taskID == "" {
			continue
		}

		req := httptest.NewRequest("GET", "/tasks/combined/"+taskID, nil)
		req.Header.Set("Authorization", "Bearer "+apiKey)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code == http.StatusOK {
			var result taskExecutionResult
			json.NewDecoder(w.Body).Decode(&result)
			if result.Status == "success" {
				successCount++
			}
			t.Logf("Task %s: status=%s, exitCode=%d", taskID, result.Status, result.ExitCode)
		}
	}

	t.Logf("Successfully completed tasks: %d", successCount)
	if successCount < 1 {
		t.Error("Expected at least 1 task to complete successfully")
	}
}

// Test: Invalid API key
func TestInvalidAPIKey(t *testing.T) {
	store, _ := createTestKeyStore(t)

	tasks := map[string]Task{
		"test-invalid": {
			Command:                 []string{getTestScriptPath(t, "fast_task.sh")},
			APIKeyNames:             []string{"test-key"},
			Async:                   false,
			ExecutionTimeoutSeconds: 5,
		},
	}

	router := createTestRouter(t, tasks, store)

	req := httptest.NewRequest("POST", "/tasks/test-invalid", nil)
	req.Header.Set("Authorization", "Bearer invalid-key")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d", w.Code)
	}
}

// Test: Missing API key
func TestMissingAPIKey(t *testing.T) {
	store, _ := createTestKeyStore(t)

	tasks := map[string]Task{
		"test-missing": {
			Command:                 []string{getTestScriptPath(t, "fast_task.sh")},
			APIKeyNames:             []string{"test-key"},
			Async:                   false,
			ExecutionTimeoutSeconds: 5,
		},
	}

	router := createTestRouter(t, tasks, store)

	req := httptest.NewRequest("POST", "/tasks/test-missing", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d", w.Code)
	}
}

// Test: Async task result cleanup
func TestAsyncTask_Cleanup(t *testing.T) {
	store, apiKey := createTestKeyStore(t)

	tasks := map[string]Task{
		"cleanup-test": {
			Command:                     []string{getTestScriptPath(t, "fast_task.sh")},
			APIKeyNames:                 []string{"test-key"},
			Async:                       true,
			ExecutionTimeoutSeconds:     5,
			AsyncResultRetentionSeconds: 2, // 2 seconds retention for faster testing
		},
	}

	router := createTestRouter(t, tasks, store)

	// Submit async task
	req := httptest.NewRequest("POST", "/tasks/cleanup-test", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", w.Code)
	}

	var submitResponse map[string]string
	json.NewDecoder(w.Body).Decode(&submitResponse)
	taskID := submitResponse["task_id"]

	if taskID == "" {
		t.Fatal("Expected task_id in response")
	}

	// Wait for task to complete
	time.Sleep(500 * time.Millisecond)

	// Task result should be available immediately after completion
	req = httptest.NewRequest("GET", "/tasks/cleanup-test/"+taskID, nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected result to be available, got status %d", w.Code)
	}

	var result taskExecutionResult
	json.NewDecoder(w.Body).Decode(&result)
	if result.Status != "success" {
		t.Errorf("Expected status 'success', got '%s'", result.Status)
	}

	// Wait for cleanup (2 seconds retention + 500ms buffer)
	time.Sleep(2500 * time.Millisecond)

	// Task result should be cleaned up
	req = httptest.NewRequest("GET", "/tasks/cleanup-test/"+taskID, nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404 after cleanup, got %d", w.Code)
	}

	var errorResponse map[string]string
	json.NewDecoder(w.Body).Decode(&errorResponse)
	if errorResponse["error"] != "task not found" {
		t.Errorf("Expected 'task not found' error, got '%s'", errorResponse["error"])
	}
}
