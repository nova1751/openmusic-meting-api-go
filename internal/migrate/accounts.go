// Package migrate reads existing Node account data without modifying it or
// writing decrypted cookies to disk.
package migrate

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
)

type nodeAccount struct {
	Platform        string `json:"platform"`
	Cookie          string `json:"cookie"`
	IsActive        bool   `json:"isActive"`
	IsValid         *bool  `json:"isValid"`
	UpdatedAt       int64  `json:"updatedAt"`
	CreatedAt       int64  `json:"createdAt"`
	Source          string `json:"source"`
	Note            string `json:"note"`
	ContributionKey string `json:"contributionKey"`
	UserInfo        struct {
		IsVIP       bool        `json:"isVip"`
		CanPlayVIP  bool        `json:"canPlayVip"`
		IsSVIP      bool        `json:"isSvip"`
		CanPlaySVIP bool        `json:"canPlaySvip"`
		SVIPType    json.Number `json:"svipType"`
	} `json:"userInfo"`
}

func accountRank(account nodeAccount) int {
	svipType, _ := strconv.Atoi(string(account.UserInfo.SVIPType))
	if account.UserInfo.IsSVIP || account.UserInfo.CanPlaySVIP || svipType > 0 {
		return 300
	}
	if account.Source != "contribution" && account.ContributionKey == "" && !strings.HasPrefix(account.Note, "contribution:") {
		return 200
	}
	return 100
}

func readJSON(path string, out any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	const maxSize = 16 << 20
	body, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return err
	}
	if len(body) > maxSize {
		return errors.New("account data exceeds size limit")
	}
	return json.Unmarshal(body, out)
}

func encryptionKey(configured, keyFile, dataDir string) ([]byte, error) {
	if configured == "" {
		if keyFile == "" {
			keyFile = filepath.Join(dataDir, "cookie-encryption.key")
		}
		data, err := os.ReadFile(keyFile)
		if err != nil {
			return nil, fmt.Errorf("read cookie key: %w", err)
		}
		configured = string(data)
	}
	raw := strings.TrimSpace(configured)
	if len(raw) == 64 {
		if decoded, err := hex.DecodeString(raw); err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	return nil, errors.New("invalid cookie encryption key")
}

func decrypt(value, id, platform string, key []byte) (string, error) {
	const prefix = "enc:v1:"
	if !strings.HasPrefix(value, prefix) {
		return value, nil
	}
	parts := strings.Split(strings.TrimPrefix(value, prefix), ":")
	if len(parts) != 3 {
		return "", errors.New("invalid encrypted cookie")
	}
	nonce, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		return "", errors.New("invalid encrypted cookie nonce")
	}
	tag, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("invalid encrypted cookie tag")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return "", errors.New("invalid encrypted cookie body")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil || len(nonce) != aead.NonceSize() || len(tag) != aead.Overhead() {
		return "", errors.New("invalid encrypted cookie parameters")
	}
	plaintext, err := aead.Open(nil, nonce, append(ciphertext, tag...), []byte(id+":"+platform))
	if err != nil {
		return "", errors.New("cannot decrypt cookie with supplied key")
	}
	return string(plaintext), nil
}

// Accounts returns only active, valid accounts; priorities match Node's saved
// FM configuration. The data and key files remain untouched.
func Accounts(dataDir, keyFile, keyValue string) (map[string][]music.Account, error) {
	key, err := encryptionKey(keyValue, keyFile, dataDir)
	if err != nil {
		return nil, err
	}
	var source map[string]nodeAccount
	if err := readJSON(filepath.Join(dataDir, "cookies.json"), &source); err != nil {
		return nil, fmt.Errorf("read Node accounts: %w", err)
	}
	var config struct {
		FM map[string]string `json:"fmPriorityCookieIds"`
	}
	var configErr error
	for attempt := 0; attempt < 3; attempt++ {
		configErr = readJSON(filepath.Join(dataDir, "config.json"), &config)
		if configErr == nil || errors.Is(configErr, os.ErrNotExist) {
			break
		}
		var syntax *json.SyntaxError
		if !errors.As(configErr, &syntax) && !errors.Is(configErr, io.EOF) && !errors.Is(configErr, io.ErrUnexpectedEOF) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if configErr != nil && !errors.Is(configErr, os.ErrNotExist) {
		return nil, fmt.Errorf("read Node account settings: %w", configErr)
	}
	ids := make([]string, 0, len(source))
	for id := range source {
		ids = append(ids, id)
	}
	sort.SliceStable(ids, func(i, j int) bool {
		a, b := source[ids[i]], source[ids[j]]
		if aRank, bRank := accountRank(a), accountRank(b); aRank != bRank {
			return aRank > bRank
		}
		aTime, bTime := a.UpdatedAt, b.UpdatedAt
		if aTime == 0 {
			aTime = a.CreatedAt
		}
		if bTime == 0 {
			bTime = b.CreatedAt
		}
		if aTime == bTime {
			return ids[i] < ids[j]
		}
		return aTime > bTime
	})
	result := map[string][]music.Account{}
	seen := map[string]bool{}
	for _, id := range ids {
		entry := source[id]
		if !entry.IsActive || (entry.IsValid != nil && !*entry.IsValid) ||
			(entry.Platform != "netease" && entry.Platform != "tencent") {
			continue
		}
		cookie, err := decrypt(entry.Cookie, id, entry.Platform, key)
		if err != nil {
			return nil, fmt.Errorf("import %s account: %w", entry.Platform, err)
		}
		cookie = strings.TrimSpace(cookie)
		if cookie == "" || strings.ContainsAny(cookie, "\r\n") {
			return nil, errors.New("invalid imported cookie")
		}
		if seen[entry.Platform+"\x00"+cookie] {
			continue
		}
		seen[entry.Platform+"\x00"+cookie] = true
		membership := music.Free
		info := entry.UserInfo
		svipType, _ := strconv.Atoi(string(info.SVIPType))
		if info.IsSVIP || info.CanPlaySVIP || svipType > 0 {
			membership = music.SVIP
		} else if info.IsVIP || info.CanPlayVIP {
			membership = music.VIP
		}
		result[entry.Platform] = append(result[entry.Platform], music.Account{
			Cookie: cookie, Membership: membership, FMPriority: config.FM[entry.Platform] == id,
		})
	}
	return result, nil
}

// Token imports one explicitly named service token without exposing it in
// command arguments or writing a duplicate plaintext file.
func Token(dataDir, name string) (string, error) {
	if name == "" {
		return "", errors.New("Node token name is required")
	}
	var tokens map[string]struct {
		Name  string `json:"name"`
		Token string `json:"token"`
	}
	if err := readJSON(filepath.Join(dataDir, "api_tokens.json"), &tokens); err != nil {
		return "", fmt.Errorf("read Node API tokens: %w", err)
	}
	var selected string
	for _, token := range tokens {
		if token.Name != name {
			continue
		}
		if selected != "" {
			return "", errors.New("Node token name is ambiguous")
		}
		selected = token.Token
	}
	if len(selected) < 32 || strings.ContainsAny(selected, " \r\n\t") {
		return "", errors.New("named Node token missing or invalid")
	}
	return selected, nil
}
