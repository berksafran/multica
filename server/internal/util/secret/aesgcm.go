// Package secret provides symmetric encryption helpers for storing
// integration tokens (Slack bot tokens, future OAuth refresh tokens) at
// rest. Keys are loaded from a process-wide env var (e.g.
// SLACK_TOKEN_ENC_KEY) and must be exactly 32 bytes once base64-decoded —
// the wrong size is an operator misconfiguration that should fail loud at
// startup, never silently downgrade to plaintext.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// ErrKeyNotConfigured is returned when the encryption key env var is empty.
// Handlers translate this into a 503 so the Connect button is disabled
// rather than the deployment writing plaintext tokens to the DB.
var ErrKeyNotConfigured = errors.New("encryption key not configured")

// AESGCM wraps a 32-byte key for AES-256-GCM seal/open.
type AESGCM struct {
	aead cipher.AEAD
}

// NewAESGCMFromBase64 decodes a base64-encoded 32-byte key into an AESGCM.
// Empty input returns ErrKeyNotConfigured so callers can distinguish the
// "deployment didn't set it" case from "key is broken".
func NewAESGCMFromBase64(keyB64 string) (*AESGCM, error) {
	if keyB64 == "" {
		return nil, ErrKeyNotConfigured
	}
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return nil, fmt.Errorf("decode key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("key must be 32 bytes (got %d)", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	return &AESGCM{aead: aead}, nil
}

// Encrypt produces base64(nonce || ciphertext) so the result is a single
// opaque TEXT column value safe for Postgres.
func (a *AESGCM) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, a.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}
	ct := a.aead.Seal(nil, nonce, []byte(plaintext), nil)
	out := make([]byte, 0, len(nonce)+len(ct))
	out = append(out, nonce...)
	out = append(out, ct...)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Decrypt reverses Encrypt. Any tamper / wrong-key surfaces as a non-nil
// error; callers should treat decryption failure as "token unavailable",
// never as "use empty string".
func (a *AESGCM) Decrypt(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	ns := a.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("ciphertext too short")
	}
	nonce, ct := raw[:ns], raw[ns:]
	pt, err := a.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("open: %w", err)
	}
	return string(pt), nil
}
