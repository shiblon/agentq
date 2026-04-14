package models

import (
	"crypto/rand"
	"fmt"
)

// generateID creates a short random ID suitable for tasks and artifacts.
// Format: 12 random hex chars (48 bits), readable and git-friendly.
func generateID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%012x", b)
}
