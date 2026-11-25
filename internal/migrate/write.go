package migrate

import (
	"cmp"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/netease"
)

type AccountSummary struct {
	ID       string `json:"id"`
	Platform string `json:"platform"`
	Nickname string `json:"nickname"`
	Note     string `json:"note"`
	IsActive bool   `json:"isActive"`
	IsValid  bool   `json:"isValid"`
	IsVIP    bool   `json:"isVip"`
}

func List(dataDir string) ([]AccountSummary, error) {
	var source map[string]struct {
		Platform string `json:"platform"`
		IsActive bool   `json:"isActive"`
		IsValid  *bool  `json:"isValid"`
		Note     string `json:"note"`
		UserInfo struct {
			Nickname   string `json:"nickname"`
			IsVIP      bool   `json:"isVip"`
			IsSVIP     bool   `json:"isSvip"`
			CanPlayVIP bool   `json:"canPlayVip"`
		} `json:"userInfo"`
	}
	if err := readJSON(filepath.Join(dataDir, "cookies.json"), &source); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []AccountSummary{}, nil
		}
		return nil, err
	}
	result := make([]AccountSummary, 0, len(source))
	for id, item := range source {
		result = append(result, AccountSummary{ID: id, Platform: item.Platform, Nickname: item.UserInfo.Nickname, Note: item.Note,
			IsActive: item.IsActive, IsValid: item.IsValid == nil || *item.IsValid,
			IsVIP: item.UserInfo.IsVIP || item.UserInfo.IsSVIP || item.UserInfo.CanPlayVIP})
	}
	slices.SortFunc(result, func(a, b AccountSummary) int { return cmp.Compare(a.Platform+"\x00"+a.ID, b.Platform+"\x00"+b.ID) })
	return result, nil
}

func encryptCookie(value, id, platform string, key []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, nonce, []byte(value), []byte(id+":"+platform))
	end := len(sealed) - gcm.Overhead()
	return "enc:v1:" + base64.StdEncoding.EncodeToString(nonce) + ":" +
		base64.StdEncoding.EncodeToString(sealed[end:]) + ":" +
		base64.StdEncoding.EncodeToString(sealed[:end]), nil
}

func putJSON(object map[string]json.RawMessage, name string, value any) error {
	encoded, err := json.Marshal(value)
	if err == nil {
		object[name] = encoded
	}
	return err
}

func atomicJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".accounts-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// SaveNetease updates the same account ID on re-login and preserves all other
// Node fields. Only one process may write this directory at a time.
func SaveNetease(dataDir, keyFile, keyValue, username, cookie string, profile netease.AccountProfile) error {
	cookie = strings.TrimSpace(cookie)
	if cookie == "" || strings.ContainsAny(cookie, "\r\n") ||
		profile.UserID == "" {
		return errors.New("invalid netease account")
	}
	if _, err := strconv.ParseUint(string(profile.UserID), 10, 64); err != nil {
		return errors.New("invalid netease account identifier")
	}
	key, err := encryptionKey(keyValue, keyFile, dataDir)
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, "cookies.json")
	accounts, err := entries(path)
	if err != nil {
		return err
	}
	id := ""
	for candidate, object := range accounts {
		var existing struct {
			Platform string `json:"platform"`
			UserInfo struct {
				UserID json.Number `json:"userId"`
			} `json:"userInfo"`
		}
		raw, _ := json.Marshal(object)
		if json.Unmarshal(raw, &existing) == nil && existing.Platform == "netease" && existing.UserInfo.UserID == profile.UserID {
			if id != "" {
				return errors.New("multiple matching netease accounts")
			}
			id = candidate
		}
	}
	created := id == ""
	if created {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return err
		}
		id = hex.EncodeToString(random[:])
		accounts[id] = make(map[string]json.RawMessage)
	}
	sealed, err := encryptCookie(cookie, id, "netease", key)
	if err != nil {
		return err
	}
	object := accounts[id]
	now := time.Now().UnixMilli()
	values := map[string]any{
		"id": id, "platform": "netease", "cookie": sealed, "updatedAt": now,
		"isActive": true, "isValid": true, "validatedAt": now, "validationError": nil, "userInfo": profile,
	}
	if created {
		values["createdAt"], values["createdBy"], values["source"] = now, username, "admin"
		values["note"] = "网易云扫码登录"
	}
	for name, value := range values {
		if err := putJSON(object, name, value); err != nil {
			return fmt.Errorf("serialize account: %w", err)
		}
	}
	return atomicJSON(path, accounts)
}

// SetActive preserves encrypted credentials and unrelated account fields.
func SetActive(dataDir, id string, active bool) error {
	path := filepath.Join(dataDir, "cookies.json")
	var accounts map[string]map[string]json.RawMessage
	if err := readJSON(path, &accounts); err != nil {
		return err
	}
	entry, ok := accounts[id]
	if !ok || entry == nil {
		return os.ErrNotExist
	}
	if err := putJSON(entry, "isActive", active); err != nil {
		return err
	}
	if err := putJSON(entry, "updatedAt", time.Now().UnixMilli()); err != nil {
		return err
	}
	return atomicJSON(path, accounts)
}

// SetTwoFactor keeps the original user-file format for compatibility.
func SetTwoFactor(dataDir, username, secret string, enabled bool) error {
	path := filepath.Join(dataDir, "users.json")
	var users map[string]map[string]json.RawMessage
	if err := readJSON(path, &users); err != nil {
		return err
	}
	user, ok := users[username]
	if !ok || user == nil {
		return os.ErrNotExist
	}
	if enabled && secret == "" {
		return errors.New("two-factor secret is required")
	}
	if err := putJSON(user, "twoFactorEnabled", enabled); err != nil {
		return err
	}
	if enabled {
		if err := putJSON(user, "twoFactorSecret", secret); err != nil {
			return err
		}
	} else {
		delete(user, "twoFactorSecret")
	}
	delete(user, "twoFactorTempSecret")
	return atomicJSON(path, users)
}

func ReadLogs(dataDir, name string) ([]json.RawMessage, error) {
	var items []json.RawMessage
	err := readJSON(filepath.Join(dataDir, name), &items)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return items, err
}

func AppendMonitorLog(dataDir string, entry map[string]any) error {
	items, err := ReadLogs(dataDir, "monitor_logs.json")
	if err != nil {
		return err
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	items = append(items, payload)
	items = items[max(0, len(items)-500):]
	return atomicJSON(filepath.Join(dataDir, "monitor_logs.json"), items)
}

func AppendLogs(dataDir, name string, newEntries []json.RawMessage) error {
	items, err := ReadLogs(dataDir, name)
	if err != nil {
		return err
	}
	items = append(items, newEntries...)
	items = items[max(0, len(items)-1000):]
	return atomicJSON(filepath.Join(dataDir, name), items)
}
