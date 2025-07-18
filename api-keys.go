package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"golang.org/x/crypto/argon2"
)

func addKey(name, keyFile string) error {
	keys := make(map[string]string)
	_, err := toml.DecodeFile(keyFile, &keys)
	if err != nil {
		return fmt.Errorf("failed to decode TOML file (%s): %w", keyFile, err)
	}

	buf := make([]byte, len(name)+1+32)
	buf[0] = byte(len(name))
	copy(buf[1:], []byte(name))
	rawKey := buf[len(name)+1:]
	_, err = rand.Read(rawKey)
	if err != nil {
		return fmt.Errorf("failed to generate random bytes: %w", err)
	}

	secret := base64.RawURLEncoding.EncodeToString(buf)
	fmt.Println("API-KEY:", secret)

	hash := hashKey(rawKey)
	encodedHash := base64.StdEncoding.EncodeToString(hash)
	keys[name] = encodedHash

	f, err := os.OpenFile(keyFile, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("failed to open file (%s): %w", keyFile, err)
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(keys)
}

type parsedKey struct {
	Name string
	Key  []byte
}

func parseKey(key string) (*parsedKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil {
		return nil, fmt.Errorf("failed to decode key: %w", err)
	}

	if len(raw) < 1 {
		return nil, fmt.Errorf("invalid key length")
	}

	nameLen := int(raw[0])
	if nameLen > len(raw)-1 {
		return nil, fmt.Errorf("invalid name length")
	}

	name := string(raw[1 : nameLen+1])
	rawKey := raw[nameLen+1:]

	return &parsedKey{Name: name, Key: rawKey}, nil
}

func hashKey(rawKey []byte) []byte {
	salt := make([]byte, 16)
	_, err := rand.Read(salt)
	if err != nil {
		return nil
	}

	time := uint32(1)
	memory := uint32(64 * 1024)
	threads := uint8(4)
	keyLen := uint32(32)

	hash := argon2.IDKey(rawKey, salt, time, memory, threads, keyLen)

	// Format: salt(16) + time(4) + memory(4) + threads(1) + keyLen(4) + hash(32)
	result := make([]byte, 0, 16+4+4+1+4+32)
	result = append(result, salt...)
	result = binary.BigEndian.AppendUint32(result, time)
	result = binary.BigEndian.AppendUint32(result, memory)
	result = append(result, threads)
	result = binary.BigEndian.AppendUint32(result, keyLen)
	result = append(result, hash...)

	return result
}

func verifyKey(key, hash []byte) bool {
	if len(hash) < 29 {
		return false
	}

	salt := hash[0:16]
	time := binary.BigEndian.Uint32(hash[16:20])
	memory := binary.BigEndian.Uint32(hash[20:24])
	threads := hash[24]
	keyLen := binary.BigEndian.Uint32(hash[25:29])

	if len(hash) != 29+int(keyLen) {
		return false
	}

	storedHash := hash[29:]
	computedHash := argon2.IDKey(key, salt, time, memory, threads, keyLen)

	if len(storedHash) != len(computedHash) {
		return false
	}

	for i := range storedHash {
		if storedHash[i] != computedHash[i] {
			return false
		}
	}

	return true
}
