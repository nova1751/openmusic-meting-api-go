package nativeadmin

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nova1751/openmusic-meting-api-go/internal/migrate"
	"golang.org/x/crypto/scrypt"
)

func newNodePassword(password string) (string, error) {
	raw := make([]byte, 16)
	rand.Read(raw)
	salt := hex.EncodeToString(raw)
	result, err := scrypt.Key([]byte(password), []byte(salt), 16384, 8, 1, 32)
	if err != nil {
		return "", err
	}
	return "scrypt:v1:" + salt + ":" + hex.EncodeToString(result), nil
}

func validUsername(name string) bool {
	return len(name) > 0 && len(name) <= 128 && name == strings.TrimSpace(name) && !strings.ContainsAny(name, "/\\\r\n\t\x00")
}

func validRole(role string) bool { return role == "admin" || role == "user" }

func (a *Admin) userEntry(username string) (user, error) {
	var users map[string]user
	err := readFileJSON(filepath.Join(a.dataDir, "users.json"), &users)
	return users[username], err
}

func (u user) passwordKey(username string) string {
	if u.AuthKey != "" {
		return u.AuthKey
	}
	return username
}

func (a *Admin) userRoutes(w http.ResponseWriter, r *http.Request, s *session, current [32]byte) {
	target := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/admin/users"), "/")
	self := r.URL.Path == "/admin/profile"
	if self {
		target = s.username
	}
	if r.Method == http.MethodGet && !self && target == "" {
		users, err := migrate.UserRecords(a.dataDir)
		if accountError(w, err) {
			return
		}
		items := make([]map[string]any, 0, len(users))
		for name, record := range users {
			items = append(items, map[string]any{"username": name, "role": record["role"], "createdAt": record["createdAt"], "lastLogin": record["lastLogin"]})
		}
		slices.SortFunc(items, func(a, b map[string]any) int { return strings.Compare(a["username"].(string), b["username"].(string)) })
		reply(w, 200, map[string]any{"success": true, "data": items})
		return
	}
	create := r.Method == http.MethodPost && target == "" && !self
	update := r.Method == http.MethodPut && target != ""
	remove := r.Method == http.MethodDelete && target != "" && !self
	if !create && !update && !remove {
		reject(w, 404, "接口不存在")
		return
	}
	var input struct{ Username, NewUsername, Password, OldPassword, Role, Code string }
	if !remove && (decode(r, &input) != nil || create && (!validUsername(input.Username) || input.Password == "") ||
		input.NewUsername != "" && !validUsername(input.NewUsername) || input.Role != "" && !validRole(input.Role) ||
		input.Password != "" && (utf8.RuneCountInString(input.Password) < 8 || len(input.Password) > 1024) ||
		self && input.Role != "") {
		reject(w, 400, "用户信息无效")
		return
	}
	if create {
		target = input.Username
	}
	a.credentialMu.Lock()
	defer a.credentialMu.Unlock()
	if !a.sessionValid(current, s) {
		reject(w, 401, "会话已失效")
		return
	}
	entry, err := a.userEntry(target)
	if accountError(w, err) {
		return
	}
	if remove && target == s.username {
		reject(w, 400, "请使用另一管理员删除当前账号")
		return
	}
	change := &migrate.UserChange{Username: input.NewUsername, Role: input.Role, Creator: s.username}
	if create && change.Role == "" {
		change.Role = "user"
	}
	if change.Role == "" {
		change.Role = entry.Role
	}
	if self && (input.Password != "" || change.Username != "") {
		valid, _, err := a.authenticate(target, input.OldPassword, input.Code)
		if err != nil || !valid {
			reject(w, 401, "请输入当前密码和双重验证码")
			return
		}
	}
	if input.Password != "" {
		change.PasswordHash, err = newNodePassword(input.Password)
		if accountError(w, err) {
			return
		}
		var random string
		random, err = secureRandom(16)
		if accountError(w, err) {
			return
		}
		change.AuthKey = "id:" + random
	} else if !create && change.Username != "" && target != change.Username {
		change.AuthKey = entry.passwordKey(target)
	}
	if remove {
		change = nil
	}
	a.writeMu.Lock()
	err = migrate.MutateUser(a.dataDir, target, change, create)
	a.writeMu.Unlock()
	switch {
	case errors.Is(err, migrate.ErrUserExists):
		reject(w, 400, "用户名已存在")
	case errors.Is(err, migrate.ErrLastAdmin):
		reject(w, 400, "必须保留一个管理员")
	case err != nil:
		accountError(w, err)
	default:
		if !create {
			a.revokeOtherSessions(target, [32]byte{})
		}
		data := map[string]any{}
		if change != nil {
			name := target
			if change.Username != "" {
				name = change.Username
			}
			data["username"], data["role"] = name, change.Role
			if name != target {
				data["renamed"] = true
			}
		}
		reply(w, 200, map[string]any{"success": true, "data": data})
	}
}
