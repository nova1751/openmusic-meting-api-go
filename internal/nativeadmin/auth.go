package nativeadmin

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/nova1751/openmusic-meting-api-go/internal/netease"
)

const passwordIterations = 310000

type user struct {
	Password         string `json:"password"`
	Role             string `json:"role"`
	TwoFactorEnabled bool   `json:"twoFactorEnabled"`
	TwoFactorSecret  string `json:"twoFactorSecret"`
}

type passwordRecord struct {
	Salt       string `json:"salt"`
	Hash       string `json:"hash"`
	Iterations int    `json:"iterations"`
}

type session struct {
	mu           sync.Mutex
	username     string
	csrf         string
	expires      time.Time
	qr           *netease.QRSession
	qrSaved      bool
	pendingTOTP  string
	totpAttempts int
}

type loginAttempt struct {
	count       int
	lockedUntil time.Time
}

func readFileJSON(path string, value any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}

func secureRandom(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func legacyHash(password string) string {
	var hash int32
	for _, code := range utf16.Encode([]rune(password)) {
		hash = hash*31 + int32(code)
	}
	return strconv.FormatInt(int64(hash), 16)
}

func equal(a, b string) bool {
	aHash, bHash := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(aHash[:], bHash[:]) == 1
}

func totpValid(secret, code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.TrimRight(strings.ToUpper(secret), "="))
	if err != nil || len(key) == 0 {
		return false
	}
	match := 0
	for delta := int64(-1); delta <= 1; delta++ {
		var counter [8]byte
		binary.BigEndian.PutUint64(counter[:], uint64(time.Now().Unix()/30+delta))
		mac := hmac.New(sha1.New, key)
		_, _ = mac.Write(counter[:])
		digest := mac.Sum(nil)
		offset := digest[len(digest)-1] & 15
		value := (binary.BigEndian.Uint32(digest[offset:offset+4]) & 0x7fffffff) % 1000000
		expected := fmt.Sprintf("%06d", value)
		match |= subtle.ConstantTimeCompare([]byte(expected), []byte(code))
	}
	return match == 1
}

func writePasswords(path string, records map[string]passwordRecord) error {
	data, err := json.Marshal(records)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".admin-auth-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
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
	return os.Rename(name, path)
}

func (a *Admin) authenticate(username, password, code string) (bool, bool, error) {
	a.authMu.Lock()
	defer a.authMu.Unlock()
	var users map[string]user
	if err := readFileJSON(filepath.Join(a.dataDir, "users.json"), &users); err != nil {
		return false, false, err
	}
	entry, exists := users[username]
	key := sha256.Sum256([]byte(username))
	if !exists {
		key = sha256.Sum256([]byte("unknown administrator"))
	}
	state := a.attempts[key]
	if state.lockedUntil.After(time.Now()) {
		return false, false, nil
	}
	passwordPath := filepath.Join(a.dataDir, "go-admin-auth.json")
	records := map[string]passwordRecord{}
	if err := readFileJSON(passwordPath, &records); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, false, err
	}
	if records == nil {
		return false, false, errors.New("invalid administrator password data")
	}
	valid := false
	if record, ok := records[username]; ok {
		salt, saltErr := base64.RawURLEncoding.DecodeString(record.Salt)
		expected, hashErr := base64.RawURLEncoding.DecodeString(record.Hash)
		if saltErr != nil || hashErr != nil || len(salt) != 16 || len(expected) != 32 ||
			record.Iterations < 100000 || record.Iterations > 1000000 {
			return false, false, errors.New("invalid administrator password record")
		}
		derived, err := pbkdf2.Key(sha256.New, password, salt, record.Iterations, len(expected))
		if err != nil {
			return false, false, err
		}
		valid = subtle.ConstantTimeCompare(derived, expected) == 1
	} else {
		valid = equal(legacyHash(password), entry.Password) && exists
	}
	valid = valid && exists && entry.Role == "admin"
	if valid && entry.TwoFactorEnabled && code == "" {
		return false, true, nil
	}
	if valid && entry.TwoFactorEnabled {
		valid = totpValid(entry.TwoFactorSecret, code)
	}
	if !valid {
		state.count++
		if state.count >= 5 {
			state.lockedUntil = time.Now().Add(15 * time.Minute)
			state.count = 0
		}
		a.attempts[key] = state
		return false, false, nil
	}
	delete(a.attempts, key)
	if _, ok := records[username]; !ok {
		salt := make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return false, false, err
		}
		derived, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
		if err != nil {
			return false, false, err
		}
		records[username] = passwordRecord{Salt: base64.RawURLEncoding.EncodeToString(salt),
			Hash: base64.RawURLEncoding.EncodeToString(derived), Iterations: passwordIterations}
		if err := writePasswords(passwordPath, records); err != nil {
			return false, false, err
		}
	}
	return true, false, nil
}
