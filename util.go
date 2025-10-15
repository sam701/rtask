package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base32"
	"strings"
)

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

// OutputCollector wraps an io.Writer and limits the number of bytes written to it.
// Once the limit is reached, subsequent writes are discarded (no-op).
type OutputCollector struct {
	Buffer       bytes.Buffer
	MaxRemaining int64 // max bytes remaining
}

func (oc *OutputCollector) Write(p []byte) (n int, err error) {
	if oc.MaxRemaining <= 0 {
		// Limit reached, discard but report success
		return len(p), nil
	}

	if int64(len(p)) > oc.MaxRemaining {
		// Partial write up to the limit
		n, err = oc.Buffer.Write(p[:oc.MaxRemaining])
		oc.MaxRemaining -= int64(n)

		// Report full length as written
		return len(p), err
	}

	n, err = oc.Buffer.Write(p)
	oc.MaxRemaining -= int64(n)
	return n, err
}

func sanitizeEnvVarName(s string) string {
	var result strings.Builder
	result.Grow(len(s))
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			result.WriteRune(r)
		} else {
			result.WriteRune('_')
		}
	}
	return strings.ToUpper(result.String())
}

type TaskID = string

var taskIDEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func newTaskID() TaskID {
	// Generate 10 random bytes (80 bits of entropy)
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(err) // unreachable
	}

	return taskIDEncoding.EncodeToString(b)
}
