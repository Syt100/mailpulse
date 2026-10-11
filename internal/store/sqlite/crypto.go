package sqlite

import (
	"fmt"
	"github.com/Syt100/mailpulse/internal/security"
)

func (s *Store) encrypt(id string, p []byte) ([]byte, error) {
	if len(s.key) != 32 {
		return nil, fmt.Errorf("master key not bound")
	}
	return security.Encrypt(s.key, p, []byte(id))
}
