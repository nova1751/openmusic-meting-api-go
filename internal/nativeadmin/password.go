package nativeadmin

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"unicode/utf8"
)

func (a *Admin) setPassword(username, password string) error {
	a.authMu.Lock()
	defer a.authMu.Unlock()
	path := filepath.Join(a.dataDir, "go-admin-auth.json")
	records := map[string]passwordRecord{}
	if err := readFileJSON(path, &records); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if records == nil {
		return errors.New("invalid administrator password data")
	}
	entry, err := a.userEntry(username)
	if err != nil {
		return err
	}
	var salt [16]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return err
	}
	derived, err := pbkdf2.Key(sha256.New, password, salt[:], passwordIterations, 32)
	if err != nil {
		return err
	}
	records[entry.passwordKey(username)] = passwordRecord{Salt: base64.RawURLEncoding.EncodeToString(salt[:]),
		Hash: base64.RawURLEncoding.EncodeToString(derived), Iterations: passwordIterations}
	return writePasswords(path, records)
}

func (a *Admin) changePassword(w http.ResponseWriter, r *http.Request, s *session, current [32]byte) {
	var input struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
		Code        string `json:"code"`
	}
	if decode(r, &input) != nil || input.OldPassword == "" || len(input.OldPassword) > 1024 ||
		len(input.NewPassword) > 1024 || utf8.RuneCountInString(input.NewPassword) < 8 {
		reject(w, 400, "新密码至少需要 8 个字符")
		return
	}
	a.credentialMu.Lock()
	defer a.credentialMu.Unlock()
	if !a.sessionValid(current, s) {
		reject(w, 401, "会话已失效")
		return
	}
	ok, needsCode, err := a.authenticate(s.username, input.OldPassword, input.Code)
	if err != nil || needsCode || !ok {
		reject(w, 401, "当前密码或验证码无效")
		return
	}
	if err := a.setPassword(s.username, input.NewPassword); err != nil {
		reject(w, 503, "无法更新密码")
		return
	}
	a.revokeOtherSessions(s.username, current)
	if err := a.backup(); err != nil {
		slog.Warn("music account backup failed", "error", err)
	}
	reply(w, 200, map[string]any{"success": true})
}
