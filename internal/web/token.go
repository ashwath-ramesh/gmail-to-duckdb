package web

import (
	"crypto/rand"
	"encoding/hex"
)

func NewToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}
