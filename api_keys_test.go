package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewStore_EmptyKeystore(t *testing.T) {
	tempDir := t.TempDir()
	keyFile := filepath.Join(tempDir, "test-keys.toml")

	store, err := NewStore(keyFile)
	if err != nil {
		t.Fatalf("Failed to create empty keystore: %v", err)
	}

	if store == nil {
		t.Fatal("Expected non-nil store")
	}

	if len(store.keyIDs) != 0 {
		t.Errorf("Expected empty keyIDs map, got %d entries", len(store.keyIDs))
	}

	if len(store.storedKeys) != 0 {
		t.Errorf("Expected empty storedKeys map, got %d entries", len(store.storedKeys))
	}

	if len(store.encodedKeys) != 0 {
		t.Errorf("Expected empty encodedKeys map, got %d entries", len(store.encodedKeys))
	}
}

func TestNewStore_NonExistentFile(t *testing.T) {
	tempDir := t.TempDir()
	keyFile := filepath.Join(tempDir, "nonexistent.toml")

	store, err := NewStore(keyFile)
	if err != nil {
		t.Fatalf("Expected no error for non-existent file, got: %v", err)
	}

	if store == nil {
		t.Fatal("Expected non-nil store")
	}
}

func TestAddKey(t *testing.T) {
	tempDir := t.TempDir()
	keyFile := filepath.Join(tempDir, "test-keys.toml")

	store, err := NewStore(keyFile)
	if err != nil {
		t.Fatalf("Failed to create keystore: %v", err)
	}

	keyName := "test-key"
	strKey, err := store.AddKey(keyName)
	if err != nil {
		t.Fatalf("Failed to add key: %v", err)
	}

	if strKey == "" {
		t.Fatal("Expected non-empty key string")
	}

	if len(store.keyIDs) != 1 {
		t.Errorf("Expected 1 key in keyIDs, got %d", len(store.keyIDs))
	}

	if len(store.storedKeys) != 1 {
		t.Errorf("Expected 1 key in storedKeys, got %d", len(store.storedKeys))
	}

	if len(store.encodedKeys) != 1 {
		t.Errorf("Expected 1 key in encodedKeys, got %d", len(store.encodedKeys))
	}

	keyID, exists := store.keyIDs[keyName]
	if !exists {
		t.Errorf("Expected key %s to exist in keyIDs", keyName)
	}

	if store.storedKeys[keyID] == nil {
		t.Errorf("Expected stored key for ID %d to exist", keyID)
	}

	if _, exists := store.encodedKeys[keyName]; !exists {
		t.Errorf("Expected encoded key %s to exist", keyName)
	}

	if _, err := os.Stat(keyFile); os.IsNotExist(err) {
		t.Error("Expected key file to be created after AddKey")
	}
}

func TestAddKey_MultipleKeys(t *testing.T) {
	tempDir := t.TempDir()
	keyFile := filepath.Join(tempDir, "test-keys.toml")

	store, err := NewStore(keyFile)
	if err != nil {
		t.Fatalf("Failed to create keystore: %v", err)
	}

	key1, err := store.AddKey("key1")
	if err != nil {
		t.Fatalf("Failed to add key1: %v", err)
	}

	key2, err := store.AddKey("key2")
	if err != nil {
		t.Fatalf("Failed to add key2: %v", err)
	}

	if key1 == key2 {
		t.Error("Expected different keys to be generated")
	}

	if len(store.keyIDs) != 2 {
		t.Errorf("Expected 2 keys, got %d", len(store.keyIDs))
	}
}

func TestKeyVerifier_EmptyAllowedKeys(t *testing.T) {
	tempDir := t.TempDir()
	keyFile := filepath.Join(tempDir, "test-keys.toml")

	store, err := NewStore(keyFile)
	if err != nil {
		t.Fatalf("Failed to create keystore: %v", err)
	}

	verifier, err := store.KeyVerifier([]string{})
	if err != nil {
		t.Fatalf("Failed to create verifier: %v", err)
	}

	if verifier == nil {
		t.Fatal("Expected non-nil verifier")
	}

	if len(verifier.allowedKeys) != 0 {
		t.Errorf("Expected 0 allowed keys, got %d", len(verifier.allowedKeys))
	}
}

func TestKeyVerifier_WithValidKey(t *testing.T) {
	tempDir := t.TempDir()
	keyFile := filepath.Join(tempDir, "test-keys.toml")

	store, err := NewStore(keyFile)
	if err != nil {
		t.Fatalf("Failed to create keystore: %v", err)
	}

	keyName := "test-key"
	strKey, err := store.AddKey(keyName)
	if err != nil {
		t.Fatalf("Failed to add key: %v", err)
	}

	verifier, err := store.KeyVerifier([]string{keyName})
	if err != nil {
		t.Fatalf("Failed to create verifier: %v", err)
	}

	result := verifier.Verify(strKey)
	if result == nil {
		t.Fatal("Expected non-nil verification result")
	}

	if !result.Success {
		t.Errorf("Expected successful verification, got failure: %s", result.FailureReason)
	}

	if result.KeyName != keyName {
		t.Errorf("Expected key name %s, got %s", keyName, result.KeyName)
	}
}

func TestKeyVerifier_WithInvalidKey(t *testing.T) {
	tempDir := t.TempDir()
	keyFile := filepath.Join(tempDir, "test-keys.toml")

	store, err := NewStore(keyFile)
	if err != nil {
		t.Fatalf("Failed to create keystore: %v", err)
	}

	keyName := "test-key"
	_, err = store.AddKey(keyName)
	if err != nil {
		t.Fatalf("Failed to add key: %v", err)
	}

	verifier, err := store.KeyVerifier([]string{keyName})
	if err != nil {
		t.Fatalf("Failed to create verifier: %v", err)
	}

	result := verifier.Verify("invalid-key")
	if result == nil {
		t.Fatal("Expected non-nil verification result")
	}

	if result.Success {
		t.Error("Expected verification to fail for invalid key")
	}

	if result.FailureReason == "" {
		t.Error("Expected failure reason to be set")
	}
}

func TestKeyVerifier_WithWrongKey(t *testing.T) {
	tempDir := t.TempDir()
	keyFile := filepath.Join(tempDir, "test-keys.toml")

	store, err := NewStore(keyFile)
	if err != nil {
		t.Fatalf("Failed to create keystore: %v", err)
	}

	_, err = store.AddKey("key1")
	if err != nil {
		t.Fatalf("Failed to add key1: %v", err)
	}

	key2, err := store.AddKey("key2")
	if err != nil {
		t.Fatalf("Failed to add key2: %v", err)
	}

	verifier, err := store.KeyVerifier([]string{"key1"})
	if err != nil {
		t.Fatalf("Failed to create verifier: %v", err)
	}

	result := verifier.Verify(key2)
	if result == nil {
		t.Fatal("Expected non-nil verification result")
	}

	if result.Success {
		t.Error("Expected verification to fail when using wrong key")
	}

	if result.FailureReason != "key_not_allowed" {
		t.Errorf("Expected failure reason 'key_not_allowed', got %s", result.FailureReason)
	}
}

func TestKeyStorePersistence(t *testing.T) {
	tempDir := t.TempDir()
	keyFile := filepath.Join(tempDir, "test-keys.toml")

	store1, err := NewStore(keyFile)
	if err != nil {
		t.Fatalf("Failed to create first keystore: %v", err)
	}

	keyName := "persistent-key"
	strKey, err := store1.AddKey(keyName)
	if err != nil {
		t.Fatalf("Failed to add key: %v", err)
	}

	store2, err := NewStore(keyFile)
	if err != nil {
		t.Fatalf("Failed to create second keystore: %v", err)
	}

	if len(store2.keyIDs) != 1 {
		t.Errorf("Expected 1 key in reloaded store, got %d", len(store2.keyIDs))
	}

	verifier, err := store2.KeyVerifier([]string{keyName})
	if err != nil {
		t.Fatalf("Failed to create verifier from reloaded store: %v", err)
	}

	result := verifier.Verify(strKey)
	if result == nil {
		t.Fatal("Expected non-nil verification result")
	}

	if !result.Success {
		t.Errorf("Expected successful verification with reloaded store, got failure: %s", result.FailureReason)
	}
}
