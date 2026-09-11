package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

func randomToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return hex.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	}
	return hex.EncodeToString(buf)
}
