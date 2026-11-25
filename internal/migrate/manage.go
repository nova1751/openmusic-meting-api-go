package migrate

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

func entries(path string) (map[string]map[string]json.RawMessage, error) {
	result := make(map[string]map[string]json.RawMessage)
	err := readJSON(path, &result)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("invalid Node account data")
	}
	return result, nil
}

type AccountChange struct {
	Cookie, Platform, Username, MatchUserID string
	Note                                    *string
	Active                                  *bool
	Profile                                 *music.AccountProfile
	Invalid                                 bool
}

// SaveAccount preserves fields that are not being edited. The caller holds the
// writer lock for the data directory, including any preceding credential read.
func SaveAccount(dataDir, keyFile, keyValue, id string, change AccountChange) (string, error) {
	path := filepath.Join(dataDir, "cookies.json")
	accounts, err := entries(path)
	if err != nil {
		return "", err
	}
	if id == "" && change.MatchUserID != "" {
		for key, record := range accounts {
			var savedPlatform string
			var savedProfile struct {
				UserID json.RawMessage `json:"userId"`
			}
			_ = json.Unmarshal(record["platform"], &savedPlatform)
			_ = json.Unmarshal(record["userInfo"], &savedProfile)
			var userID string
			if json.Unmarshal(savedProfile.UserID, &userID) != nil {
				userID = string(savedProfile.UserID)
			}
			if savedPlatform == change.Platform && userID == change.MatchUserID {
				if id != "" {
					return "", errors.New("multiple matching accounts")
				}
				id = key
			}
		}
	}
	now := time.Now().UnixMilli()
	platform := change.Platform
	entry := accounts[id]
	values := map[string]any{"updatedAt": now}
	if id != "" {
		if entry == nil {
			return "", os.ErrNotExist
		}
		if json.Unmarshal(entry["platform"], &platform) != nil {
			return "", errors.New("invalid saved account platform")
		}
	} else {
		if !music.KnownPlatform(platform) || change.Cookie == "" {
			return "", errors.New("invalid new account")
		}
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		id = hex.EncodeToString(random[:])
		entry = make(map[string]json.RawMessage)
		accounts[id] = entry
		values["id"], values["platform"], values["source"] = id, platform, "admin"
		values["createdAt"], values["createdBy"], values["isActive"] = now, change.Username, true
	}
	if change.Note != nil {
		values["note"] = *change.Note
	}
	if change.Active != nil {
		values["isActive"] = *change.Active
	}
	if change.Cookie != "" {
		if strings.ContainsAny(change.Cookie, "\r\n") {
			return "", errors.New("invalid account cookie")
		}
		key, err := encryptionKey(keyValue, keyFile, dataDir)
		if err != nil {
			return "", err
		}
		sealed, err := encryptCookie(change.Cookie, id, platform, key)
		if err != nil {
			return "", err
		}
		values["cookie"] = sealed
		// Replacing the login invalidates any membership from the old login.
		values["isValid"], values["userInfo"], values["validatedAt"], values["validationError"] = nil, nil, nil, nil
	}
	if change.Profile != nil {
		values["isValid"], values["userInfo"], values["validatedAt"], values["validationError"] = true, change.Profile, now, nil
	} else if change.Invalid {
		values["isValid"], values["userInfo"], values["validatedAt"], values["validationError"] = false, nil, now, "登录凭证无效"
	}
	for name, value := range values {
		if err := putJSON(entry, name, value); err != nil {
			return "", err
		}
	}
	return id, atomicJSON(path, accounts)
}

func AccountCredential(dataDir, keyFile, keyValue, id string) (platform, cookie string, err error) {
	accounts, err := entries(filepath.Join(dataDir, "cookies.json"))
	if err != nil {
		return "", "", err
	}
	item := accounts[id]
	if item == nil {
		return "", "", os.ErrNotExist
	}
	if json.Unmarshal(item["platform"], &platform) != nil || json.Unmarshal(item["cookie"], &cookie) != nil {
		return "", "", errors.New("invalid Node account")
	}
	if strings.HasPrefix(cookie, "enc:v1:") {
		key, err := encryptionKey(keyValue, keyFile, dataDir)
		if err != nil {
			return "", "", err
		}
		cookie, err = decrypt(cookie, id, platform, key)
		if err != nil {
			return "", "", err
		}
	}
	return platform, cookie, nil
}

func DeleteAccount(dataDir, id string) error {
	path := filepath.Join(dataDir, "cookies.json")
	accounts, err := entries(path)
	if err != nil {
		return err
	}
	if accounts[id] == nil {
		return os.ErrNotExist
	}
	delete(accounts, id)
	return atomicJSON(path, accounts)
}
