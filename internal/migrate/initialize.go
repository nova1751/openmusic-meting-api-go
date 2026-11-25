package migrate

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// InitializeCookieKey creates the standard key file for a new directory or a
// plaintext-only legacy directory. It preserves the key of an encrypted store.
func InitializeCookieKey(dataDir, keyFile, keyValue string) error {
	if keyValue != "" || keyFile != "" {
		_, err := encryptionKey(keyValue, keyFile, dataDir)
		return err
	}
	path := filepath.Join(dataDir, "cookie-encryption.key")
	if _, err := encryptionKey("", "", dataDir); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	items, err := entries(filepath.Join(dataDir, "cookies.json"))
	if err != nil {
		return err
	}
	for _, item := range items {
		var value string
		if json.Unmarshal(item["cookie"], &value) == nil && strings.HasPrefix(value, "enc:v1:") {
			return errors.New("the original cookie encryption key is required")
		}
	}
	var key [32]byte
	rand.Read(key[:])
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = io.WriteString(file, hex.EncodeToString(key[:])+"\n")
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
