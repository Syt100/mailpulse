package security

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestWrongKeyFailsClosed(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	c, e := Encrypt(key, []byte("secret"), []byte("account-a"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Decrypt(bytes.Repeat([]byte{2}, 32), c, []byte("account-a")); e == nil {
		t.Fatal("wrong key accepted")
	}
}
func TestAccountSecretsBoundToAccountID(t *testing.T) {
	key := make([]byte, 32)
	c, _ := Encrypt(key, []byte("secret"), []byte("a"))
	if _, e := Decrypt(key, c, []byte("b")); e == nil {
		t.Fatal("account swap accepted")
	}
	p, e := Decrypt(key, c, []byte("a"))
	if e != nil || string(p) != "secret" {
		t.Fatalf("%q %v", p, e)
	}
}
func TestLoadKeyRejectsMissingAndUnsafe(t *testing.T) {
	p := filepath.Join(t.TempDir(), "key")
	if _, e := LoadKey(p); e == nil {
		t.Fatal("missing accepted")
	}
	os.WriteFile(p, make([]byte, 32), 0644)
	if _, e := LoadKey(p); e == nil {
		t.Fatal("unsafe accepted")
	}
	os.Chmod(p, 0600)
	if _, e := LoadKey(p); e != nil {
		t.Fatal(e)
	}
}
