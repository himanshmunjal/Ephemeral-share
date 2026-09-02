package handlers

import (
	"context"
	"crypto/rand"
	"fmt"

	"ephemeral-share/internal/redisclient"
)

// crockfordAlphabet avoids ambiguous characters (0/O, 1/I/L) for easy manual entry.
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func randomCode(length int) (string, error) {
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, length)
	for i, v := range b {
		out[i] = crockfordAlphabet[int(v)%len(crockfordAlphabet)]
	}
	return string(out), nil
}

// GenerateUniqueCode retries against Redis to avoid collisions with active sessions.
func GenerateUniqueCode(ctx context.Context, store *redisclient.Store, length int) (string, error) {
	for attempt := 0; attempt < 5; attempt++ {
		code, err := randomCode(length)
		if err != nil {
			return "", err
		}
		exists, err := store.CodeExists(ctx, code)
		if err != nil {
			return "", err
		}
		if !exists {
			return code, nil
		}
	}
	return "", fmt.Errorf("could not generate unique code after retries")
}
