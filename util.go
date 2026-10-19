package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

func newTaskID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x%s", time.Now().UnixNano(), hex.EncodeToString(b))
}
