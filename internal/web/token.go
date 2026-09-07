package web

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

func NewToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	if tok == "" {
		return "", fmt.Errorf("empty session token")
	}
	return tok, nil
}
