package magiclink

import (
	"crypto/rand"
	"fmt"
)

func generateSixDigit() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random: %w", err)
	}
	n := (int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])) & 0x7FFFFFFF
	return fmt.Sprintf("%06d", n%1000000), nil
}
