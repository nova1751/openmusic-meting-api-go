package migrate

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const tokenFile = "api_tokens.json"

func ValidToken(dataDir, value string) bool {
	items, err := entries(filepath.Join(dataDir, tokenFile))
	if err != nil || value == "" {
		return false
	}
	hash := sha256.Sum256([]byte(value))
	match := 0
	for _, item := range items {
		var token string
		if json.Unmarshal(item["token"], &token) == nil && token != "" {
			digest := sha256.Sum256([]byte(token))
			match |= subtle.ConstantTimeCompare(hash[:], digest[:])
		}
	}
	return match == 1
}

func ListTokens(dataDir string) (map[string]map[string]json.RawMessage, error) {
	items, err := entries(filepath.Join(dataDir, tokenFile))
	for _, item := range items {
		delete(item, "token")
	}
	return items, err
}

func WriteToken(dataDir, id string, changes map[string]any, create bool) (map[string]json.RawMessage, error) {
	path := filepath.Join(dataDir, tokenFile)
	items, err := entries(path)
	if err != nil {
		return nil, err
	}
	item := items[id]
	if create {
		if item != nil {
			return nil, errors.New("duplicate token identifier")
		}
		item = make(map[string]json.RawMessage)
		items[id] = item
	}
	if item == nil {
		return nil, os.ErrNotExist
	}
	if changes == nil {
		delete(items, id)
		return nil, atomicJSON(path, items)
	}
	for key, value := range changes {
		if err := putJSON(item, key, value); err != nil {
			return nil, err
		}
	}
	if err := atomicJSON(path, items); err != nil {
		return nil, err
	}
	if !create {
		delete(item, "token")
	}
	return item, nil
}

func HasTokens(dataDir string) (bool, error) {
	items, err := entries(filepath.Join(dataDir, tokenFile))
	if err != nil {
		return false, err
	}
	for _, item := range items {
		var token string
		if json.Unmarshal(item["token"], &token) == nil && len(token) >= 32 && !strings.ContainsAny(token, " \r\n\t") {
			return true, nil
		}
	}
	return false, nil
}
