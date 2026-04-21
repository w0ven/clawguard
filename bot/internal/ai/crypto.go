package ai

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"go.uber.org/zap"
)

var (
	cryptoMu  sync.RWMutex
	cryptoKey []byte
)

func ConfigureEncryption(keyFromConfig string, logger *zap.Logger) error {
	keyMaterial := strings.TrimSpace(os.Getenv("ENCRYPTION_KEY"))
	if keyMaterial == "" {
		keyMaterial = strings.TrimSpace(keyFromConfig)
	}
	if keyMaterial == "" {
		buf := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, buf); err != nil {
			return fmt.Errorf("generate temporary encryption key: %w", err)
		}
		setCryptoKey(buf)
		if logger != nil {
			logger.Warn("ENCRYPTION_KEY is empty; using temporary key, encrypted DB API keys will not be decryptable after restart")
		}
		return nil
	}
	sum := sha256.Sum256([]byte(keyMaterial))
	setCryptoKey(sum[:])
	return nil
}

func EncryptAPIKey(plain string) (string, error) {
	block, err := aes.NewCipher(currentCryptoKey())
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("read nonce: %w", err)
	}
	payload := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(payload), nil
}

func DecryptAPIKey(ciphertext string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(ciphertext))
	if err != nil {
		return "", fmt.Errorf("decode api key: %w", err)
	}
	block, err := aes.NewCipher(currentCryptoKey())
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create gcm: %w", err)
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce := raw[:gcm.NonceSize()]
	enc := raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, enc, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt api key: %w", err)
	}
	return string(plain), nil
}

func setCryptoKey(key []byte) {
	cryptoMu.Lock()
	defer cryptoMu.Unlock()
	cryptoKey = append([]byte(nil), key...)
}

func currentCryptoKey() []byte {
	cryptoMu.RLock()
	defer cryptoMu.RUnlock()
	if len(cryptoKey) == 0 {
		sum := sha256.Sum256(nil)
		return sum[:]
	}
	return append([]byte(nil), cryptoKey...)
}
