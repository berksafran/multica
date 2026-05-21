package secret

import (
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func newKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestAESGCM_RoundTrip(t *testing.T) {
	c, err := NewAESGCMFromBase64(newKey(t))
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	cases := []string{
		"",
		"hello",
		"xoxb-1234567890-abcdef",
		"unicode: ışığın değeri ✓",
		// long string
		string(make([]byte, 4096)),
	}
	for _, plaintext := range cases {
		ct, err := c.Encrypt(plaintext)
		if err != nil {
			t.Fatalf("encrypt %q: %v", plaintext, err)
		}
		pt, err := c.Decrypt(ct)
		if err != nil {
			t.Fatalf("decrypt %q: %v", plaintext, err)
		}
		if pt != plaintext {
			t.Errorf("round-trip mismatch: got %q want %q", pt, plaintext)
		}
	}
}

func TestAESGCM_NoncesAreUnique(t *testing.T) {
	c, err := NewAESGCMFromBase64(newKey(t))
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	// Same plaintext encrypted twice should produce different
	// ciphertexts because the nonce is fresh per Encrypt. If they
	// match, the implementation is reusing nonces — a fatal AES-GCM
	// security flaw.
	a, _ := c.Encrypt("same-plaintext")
	b, _ := c.Encrypt("same-plaintext")
	if a == b {
		t.Fatal("identical ciphertexts for same plaintext — nonce not randomized")
	}
}

func TestAESGCM_TamperDetected(t *testing.T) {
	c, err := NewAESGCMFromBase64(newKey(t))
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	ct, err := c.Encrypt("secret")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(ct)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Flip the last byte of the ciphertext (auth tag region) — GCM
	// must reject this on Open.
	raw[len(raw)-1] ^= 0x01
	tampered := base64.StdEncoding.EncodeToString(raw)
	if _, err := c.Decrypt(tampered); err == nil {
		t.Fatal("expected error decrypting tampered ciphertext")
	}
}

func TestAESGCM_WrongKey(t *testing.T) {
	c1, _ := NewAESGCMFromBase64(newKey(t))
	c2, _ := NewAESGCMFromBase64(newKey(t))
	ct, err := c1.Encrypt("for-c1")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := c2.Decrypt(ct); err == nil {
		t.Fatal("expected error decrypting with wrong key")
	}
}

func TestNewAESGCMFromBase64_Errors(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{"empty returns ErrKeyNotConfigured", ""},
		{"invalid base64", "not!base64!"},
		{"wrong key length", base64.StdEncoding.EncodeToString([]byte("too-short"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewAESGCMFromBase64(tt.key); err == nil {
				t.Errorf("expected error")
			}
		})
	}
}

func TestAESGCM_DecryptInvalidBase64(t *testing.T) {
	c, _ := NewAESGCMFromBase64(newKey(t))
	if _, err := c.Decrypt("not!base64!"); err == nil {
		t.Fatal("expected base64 decode error")
	}
}

func TestAESGCM_DecryptTooShort(t *testing.T) {
	c, _ := NewAESGCMFromBase64(newKey(t))
	// Empty ciphertext is shorter than the GCM nonce — must error,
	// never return the empty plaintext (which would be a subtle bug
	// since "" is also a valid plaintext from Encrypt).
	if _, err := c.Decrypt(""); err == nil {
		t.Fatal("expected error for empty ciphertext")
	}
}
