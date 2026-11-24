package nativeadmin

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/nova1751/openmusic-meting-api-go/internal/migrate"
	qrcode "github.com/skip2/go-qrcode"
)

func (a *Admin) twoFactorEnabled(username string) (bool, error) {
	a.authMu.Lock()
	defer a.authMu.Unlock()
	var users map[string]user
	if err := readFileJSON(filepath.Join(a.dataDir, "users.json"), &users); err != nil {
		return false, err
	}
	entry, ok := users[username]
	if !ok || entry.Role != "admin" {
		return false, errors.New("administrator not found")
	}
	return entry.TwoFactorEnabled, nil
}

func (a *Admin) setTwoFactor(username, secret string, enabled bool) error {
	a.authMu.Lock()
	defer a.authMu.Unlock()
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return migrate.SetTwoFactor(a.dataDir, username, secret, enabled)
}

func (a *Admin) setupTwoFactor(w http.ResponseWriter, s *session) {
	enabled, err := a.twoFactorEnabled(s.username)
	if err != nil {
		reject(w, 503, "无法读取双重验证状态")
		return
	}
	if enabled {
		reject(w, 400, "双重验证已启用")
		return
	}
	var bytes [20]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		reject(w, 503, "无法创建双重验证密钥")
		return
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(bytes[:])
	s.mu.Lock()
	s.pendingTOTP, s.totpAttempts = secret, 0
	s.mu.Unlock()
	reply(w, 200, map[string]any{"success": true, "secret": secret})
}

func (a *Admin) twoFactorImage(w http.ResponseWriter, s *session) {
	s.mu.Lock()
	secret := s.pendingTOTP
	s.mu.Unlock()
	if secret == "" {
		reject(w, 404, "请先创建双重验证密钥")
		return
	}
	uri := &url.URL{Scheme: "otpauth", Host: "totp", Path: "/OpenMusic:" + s.username}
	uri.RawQuery = url.Values{"secret": {secret}, "issuer": {"OpenMusic"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}.Encode()
	png, err := qrcode.Encode(uri.String(), qrcode.Medium, 256)
	if err != nil {
		reject(w, 500, "无法生成双重验证二维码")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(png)
}

func (a *Admin) enableTwoFactor(w http.ResponseWriter, r *http.Request, s *session, current [32]byte) {
	var input struct {
		Code string `json:"code"`
	}
	if decode(r, &input) != nil {
		reject(w, 400, "请输入验证码")
		return
	}
	a.credentialMu.Lock()
	defer a.credentialMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !a.sessionValid(current, s) {
		reject(w, 401, "会话已失效")
		return
	}
	if s.pendingTOTP == "" {
		reject(w, 400, "请先创建双重验证密钥")
		return
	}
	if !totpValid(s.pendingTOTP, input.Code) {
		s.totpAttempts++
		if s.totpAttempts >= 5 {
			s.pendingTOTP = ""
		}
		reject(w, 400, "验证码无效，请重试")
		return
	}
	enabled, err := a.twoFactorEnabled(s.username)
	if err != nil || enabled {
		reject(w, 400, "双重验证状态已变化，请刷新")
		return
	}
	if err := a.setTwoFactor(s.username, s.pendingTOTP, true); err != nil {
		reject(w, 503, "无法启用双重验证")
		return
	}
	s.pendingTOTP, s.totpAttempts = "", 0
	a.revokeOtherSessions(s.username, current)
	if err := a.backup(); err != nil {
		slog.Warn("music account backup failed", "error", err)
	}
	reply(w, 200, map[string]any{"success": true})
}

func (a *Admin) disableTwoFactor(w http.ResponseWriter, r *http.Request, s *session, current [32]byte) {
	var input struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if decode(r, &input) != nil || input.Password == "" || len(input.Password) > 1024 {
		reject(w, 400, "请输入密码和验证码")
		return
	}
	a.credentialMu.Lock()
	defer a.credentialMu.Unlock()
	if !a.sessionValid(current, s) {
		reject(w, 401, "会话已失效")
		return
	}
	enabled, err := a.twoFactorEnabled(s.username)
	if err != nil || !enabled {
		reject(w, 400, "双重验证未启用")
		return
	}
	ok, needsCode, err := a.authenticate(s.username, input.Password, input.Code)
	if err != nil || needsCode || !ok {
		reject(w, 401, "密码或验证码无效")
		return
	}
	if err := a.setTwoFactor(s.username, "", false); err != nil {
		reject(w, 503, "无法关闭双重验证")
		return
	}
	a.revokeOtherSessions(s.username, current)
	if err := a.backup(); err != nil {
		slog.Warn("music account backup failed", "error", err)
	}
	reply(w, 200, map[string]any{"success": true})
}

func (a *Admin) revokeOtherSessions(username string, current [32]byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, other := range a.sessions {
		if key != current && other.username == username {
			delete(a.sessions, key)
		}
	}
}

func (a *Admin) sessionValid(key [32]byte, expected *session) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[key] == expected && time.Now().Before(expected.expires)
}
