package main

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/fxamacker/cbor/v2"
	"golang.org/x/crypto/argon2"
)

func addKey(name, keyFile string) error {
	store, err := NewStore(keyFile)
	if err != nil {
		return err
	}

	strKey, err := store.AddKey(name)
	if err != nil {
		return err
	}

	fmt.Println("API-KEY:", strKey)
	return nil
}

type KeyID = int

type APIKeyStore struct {
	filename    string
	encodedKeys map[string]string
	keyIDs      map[string]KeyID
	storedKeys  map[KeyID]*storedKey
}

func NewStore(filename string) (*APIKeyStore, error) {
	s := &APIKeyStore{
		filename:    filename,
		encodedKeys: make(map[string]string),
		keyIDs:      make(map[string]KeyID),
		storedKeys:  make(map[KeyID]*storedKey),
	}

	_, err := toml.DecodeFile(filename, &s.encodedKeys)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to decode TOML file (%s): %w", filename, err)
	}

	for name, content := range s.encodedKeys {
		storedKey, err := decodeKey[storedKey](content)
		if err != nil {
			return nil, fmt.Errorf("failed to decode hash for key %s: %w", name, err)
		}

		s.storedKeys[storedKey.ID] = storedKey
		s.keyIDs[name] = storedKey.ID
	}
	slog.Info("Loaded keys", "count", len(s.keyIDs))

	return s, nil
}

func (s *APIKeyStore) save() error {
	f, err := os.OpenFile(s.filename, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("failed to open file (%s): %w", s.filename, err)
	}
	defer f.Close()
	slog.Debug("Saving API keys", "count", len(s.keyIDs))
	return toml.NewEncoder(f).Encode(s.encodedKeys)
}

func (s *APIKeyStore) freeID() KeyID {
	for i := range 1000 {
		if s.storedKeys[i] == nil {
			slog.Debug("found free API ID", "id", i)
			return i
		}
	}
	panic("no free ID below 1000")
}

// AddKey generates a new key and adds it to the store
func (s *APIKeyStore) AddKey(name string) (string, error) {
	uk := newUserKey(s.freeID(), 40)
	sk := uk.hash()

	storedString, err := encodeKey(sk)
	if err != nil {
		return "", fmt.Errorf("failed to encode stored key: %w", err)
	}
	s.encodedKeys[name] = storedString
	s.storedKeys[sk.ID] = sk
	s.keyIDs[name] = sk.ID
	s.save()

	return encodeKey(uk)
}

func (s *APIKeyStore) KeyVerifier(allowedKeyNames []string) (*KeyVerifier, error) {
	kv := &KeyVerifier{
		allowedKeys: make(map[KeyID]AllowedKey),
	}
	for _, name := range allowedKeyNames {
		id, found := s.keyIDs[name]
		if !found {
			return nil, fmt.Errorf("no such key: %s", name)
		}
		sk := s.storedKeys[id]
		if sk == nil {
			panic("missing stored key for name: " + name)
		}

		slog.Info("added allowed key", "name", name)
		kv.allowedKeys[id] = AllowedKey{
			name:      name,
			storedKey: sk,
		}
	}
	return kv, nil
}

type AllowedKey struct {
	name      string
	storedKey *storedKey
}

type KeyVerifier struct {
	allowedKeys map[KeyID]AllowedKey
}

func (v *KeyVerifier) Verify(strKey string) *VerificationResult {
	result := &VerificationResult{Success: false}
	uk, err := decodeKey[userKey](strKey)
	if err != nil {
		slog.Debug("invalid key encoding", "key", strKey, "error", err)
		result.FailureReason = "invalid_key_encoding"
		return result
	}

	key, found := v.allowedKeys[uk.ID]
	if !found {
		result.FailureReason = "key_not_allowed"
		return result
	}
	result.KeyName = key.name

	if !uk.verify(key.storedKey) {
		slog.Debug("invalid key", "key", strKey, "sk", key.storedKey)
		result.FailureReason = "invalid_key"
		return result
	}

	result.Success = true
	return result
}

type VerificationResult struct {
	// Can be empty if case of invalid key
	KeyName string

	Success       bool
	FailureReason string
}

type storedKey struct {
	ID int `cbor:"1,keyasint,omitempty"`

	// === argon2 params

	Salt    []byte `cbor:"2,keyasint,omitempty"`
	Time    uint32 `cbor:"3,keyasint,omitempty"`
	Memory  uint32 `cbor:"4,keyasint,omitempty"`
	Threads uint8  `cbor:"5,keyasint,omitempty"`
	Hash    []byte `cbor:"6,keyasint,omitempty"`
}

type userKey struct {
	ID  int    `cbor:"1,keyasint,omitempty"`
	Key []byte `cbor:"2,keyasint,omitempty"`
}

func newUserKey(id int, length int) *userKey {
	k := &userKey{
		ID:  id,
		Key: make([]byte, length),
	}

	_, err := rand.Read(k.Key)
	if err != nil {
		panic(err)
	}
	return k
}

func (k *userKey) hash() *storedKey {
	sk := &storedKey{
		ID:      k.ID,
		Salt:    make([]byte, 16),
		Time:    uint32(1),
		Memory:  uint32(64),
		Threads: uint8(4),
	}

	_, err := rand.Read(sk.Salt)
	if err != nil {
		panic(err)
	}

	sk.Hash = argon2.IDKey(k.Key, sk.Salt, sk.Time, sk.Memory*1024, sk.Threads, uint32(len(k.Key)))
	return sk
}

func (k *userKey) verify(sk *storedKey) bool {
	computedHash := argon2.IDKey(k.Key, sk.Salt, sk.Time, sk.Memory*1024, sk.Threads, uint32(len(k.Key)))
	return subtle.ConstantTimeCompare(sk.Hash, computedHash) == 1
}

func decodeKey[T any](str string) (*T, error) {
	data, err := base64.RawURLEncoding.DecodeString(str)
	if err != nil {
		return nil, fmt.Errorf("failed to base64 decode key: %w", err)
	}
	var k T
	err = cbor.NewDecoder(bytes.NewReader(data)).Decode(&k)
	if err != nil {
		return nil, fmt.Errorf("failed to CBOR decode key: %w", err)
	}
	return &k, nil
}

func encodeKey(k any) (string, error) {
	var buf bytes.Buffer
	err := cbor.NewEncoder(&buf).Encode(k)
	if err != nil {
		return "", fmt.Errorf("failed to encode key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}
