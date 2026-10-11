package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"os"
)

func LoadKey(path string) ([]byte, error) {
	info, e := os.Stat(path)
	if e != nil {
		return nil, fmt.Errorf("key file unavailable")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("key file must be private (0600 or 0400)")
	}
	b, e := os.ReadFile(path)
	if e != nil || len(b) != 32 {
		return nil, fmt.Errorf("key file must contain 32 raw bytes")
	}
	return b, nil
}
func Encrypt(key, plaintext, aad []byte) ([]byte, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return nil, e
	}
	nonce := make([]byte, g.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return nil, e
	}
	return g.Seal(nonce, nonce, plaintext, aad), nil
}
func Decrypt(key, data, aad []byte) ([]byte, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return nil, e
	}
	if len(data) < g.NonceSize() {
		return nil, fmt.Errorf("invalid encrypted secret")
	}
	p, e := g.Open(nil, data[:g.NonceSize()], data[g.NonceSize():], aad)
	if e != nil {
		return nil, fmt.Errorf("secret decryption failed")
	}
	return p, nil
}
