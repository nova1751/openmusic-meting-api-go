package migrate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

var ErrUserExists = errors.New("that username already exists")
var ErrLastAdmin = errors.New("at least one administrator is required")

type UserChange struct {
	Username, PasswordHash, Role, Creator, AuthKey string
}

func UserRecords(dataDir string) (map[string]map[string]json.RawMessage, error) {
	return entries(filepath.Join(dataDir, "users.json"))
}

func MutateUser(dataDir, username string, update *UserChange, create bool) error {
	users, err := UserRecords(dataDir)
	if err != nil {
		return err
	}
	entry := users[username]
	if create && entry != nil {
		return ErrUserExists
	}
	if !create && entry == nil {
		return os.ErrNotExist
	}
	if create {
		entry = make(map[string]json.RawMessage)
		users[username] = entry
	}
	if update == nil {
		delete(users, username)
	} else {
		values := map[string]any{}
		target := update.Username
		if target != "" && target != username {
			if users[target] != nil {
				return ErrUserExists
			}
			delete(users, username)
			users[target] = entry
			values["username"] = target
		}
		if create {
			values["username"], values["createdAt"], values["createdBy"] = username, time.Now().UnixMilli(), update.Creator
		}
		if update.AuthKey != "" {
			values["goAuthKey"] = update.AuthKey
		}
		if update.Role != "" {
			values["role"] = update.Role
		}
		if update.PasswordHash != "" {
			values["password"] = update.PasswordHash
		}
		delete(entry, "token")
		delete(entry, "tokenExpiresAt")
		for name, value := range values {
			if err := putJSON(entry, name, value); err != nil {
				return err
			}
		}
	}
	for _, record := range users {
		var role string
		if json.Unmarshal(record["role"], &role) == nil && role == "admin" {
			return atomicJSON(filepath.Join(dataDir, "users.json"), users)
		}
	}
	return ErrLastAdmin
}
