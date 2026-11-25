package nativeadmin

import (
	"errors"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/nova1751/openmusic-meting-api-go/internal/migrate"
)

// Initialize creates the first administrator only when the user store is absent.
func Initialize(dataDir, username, password string) error {
	if _, err := os.Stat(filepath.Join(dataDir, "users.json")); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if username == "" {
		username = "admin"
	}
	if !validUsername(username) || utf8.RuneCountInString(password) < 8 || len(password) > 1024 {
		return errors.New("new native admin requires ADMIN_PASSWORD with at least 8 characters")
	}
	hash, err := newNodePassword(password)
	if err != nil {
		return err
	}
	key, err := secureRandom(16)
	if err != nil {
		return err
	}
	return migrate.MutateUser(dataDir, username, &migrate.UserChange{PasswordHash: hash, Role: "admin", Creator: "system", AuthKey: "id:" + key}, true)
}
