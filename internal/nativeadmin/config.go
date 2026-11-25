package nativeadmin

import (
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/nova1751/openmusic-meting-api-go/internal/migrate"
)

func (a *Admin) configRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/admin/config")
	if r.Method == http.MethodGet && path == "" {
		data, err := migrate.Config(a.dataDir)
		if !accountError(w, err) {
			reply(w, 200, map[string]any{"success": true, "data": data})
		}
		return
	}
	if r.Method != http.MethodPut {
		reject(w, 404, "接口不存在")
		return
	}
	var err error
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	switch path {
	case "/openmusic-auth", "/backup":
		var input struct{ Enabled *bool }
		if decode(r, &input) != nil || input.Enabled == nil {
			reject(w, 400, "开关必须为布尔值")
			return
		}
		field := "backupEnabled"
		if path == "/openmusic-auth" {
			field = "openmusicAuthEnabled"
		}
		err = migrate.WriteSetting(a.dataDir, field, *input.Enabled)
	case "/qishui-signer", "/loudness-service":
		var input struct{ URL *string }
		if decode(r, &input) != nil {
			reject(w, 400, "无效的签名服务地址")
			return
		}
		var address any
		if input.URL != nil && strings.TrimSpace(*input.URL) != "" {
			u, parseErr := url.Parse(strings.TrimSpace(*input.URL))
			if parseErr != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
				reject(w, 400, "签名服务需要有效的 HTTP 或 HTTPS 地址")
				return
			}
			address = u.String()
		}
		field := "qishuiSignerUrl"
		if path == "/loudness-service" {
			field = "loudnessServiceUrl"
		}
		err = migrate.WriteSetting(a.dataDir, field, address)
	case "/admin-path":
		var input struct{ AdminPath string }
		if decode(r, &input) != nil {
			reject(w, 400, "无效的后台路径")
			return
		}
		name := strings.Trim(input.AdminPath, "/")
		if !validAdminPath(name) {
			reject(w, 400, "后台路径无效或与 API 冲突")
			return
		}
		err = migrate.WriteSetting(a.dataDir, "adminPath", name)
	case "/fm-priority":
		var input struct {
			Platform string
			CookieID string
		}
		if decode(r, &input) != nil {
			reject(w, 400, "账号数据无效")
			return
		}
		err = migrate.SetFMPriority(a.dataDir, input.Platform, input.CookieID)
	default:
		reject(w, 404, "接口不存在")
		return
	}
	if accountError(w, err) {
		return
	}
	data, err := migrate.Config(a.dataDir)
	if !accountError(w, err) {
		reply(w, 200, map[string]any{"success": true, "data": data})
	}
}

var adminPathPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{3,128}$`)

func validAdminPath(path string) bool {
	switch path {
	case "api", "test", "audio", "livez", "readyz", "__meting_go":
		return false
	default:
		return adminPathPattern.MatchString(path)
	}
}

func (a *Admin) PagePath(path string) bool {
	name := strings.Trim(path, "/")
	if !validAdminPath(name) {
		return false
	}
	cfg, err := migrate.Config(a.dataDir)
	if err == nil && validAdminPath(cfg.AdminPath) {
		return name == cfg.AdminPath
	}
	env := strings.Trim(os.Getenv("ADMIN_PATH"), "/")
	if validAdminPath(env) {
		return name == env
	}
	return name == "admin"
}
