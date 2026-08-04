package app

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"strings"

	"golang.org/x/crypto/curve25519"
)

func GeneratePrivateKey() (string, error) {
	key := make([]byte, curve25519.ScalarSize)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	clamp(key)
	return base64.StdEncoding.EncodeToString(key), nil
}

func PublicKey(private string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(private))
	if err != nil {
		return "", err
	}
	if len(raw) != curve25519.ScalarSize {
		return "", errors.New("private key must decode to 32 bytes")
	}
	clamp(raw)
	public, err := curve25519.X25519(raw, curve25519.Basepoint)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(public), nil
}

func PubkeyFromReader(r io.Reader) (string, error) {
	key, err := io.ReadAll(io.LimitReader(r, 4096))
	if err != nil {
		return "", err
	}
	return PublicKey(string(key))
}

func clamp(key []byte) {
	key[0] &= 248
	key[31] &= 127
	key[31] |= 64
}
